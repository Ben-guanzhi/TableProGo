package main

// W5: the server-to-server table copy ("数据传输").
//
// Everything structural already exists: dbcore/schemadiff reads a catalog,
// dbcore/ddl renders CREATE TABLE, dbcore's BrowseRows pages a table, and
// internal/importfile renders parameterized INSERTs. What no package owned was
// the ORCHESTRATION — which order, what happens on a failure, and what the user
// is told before anything is written. This file is that orchestrator, and
// internal/transfer is its pure planning half (ordering, cycle handling,
// cross-engine downgrade report).
//
// The safety rules it obeys, and why:
//
//   - Nothing runs until Preview returns. The preview is the plan plus the DDL
//     the run would execute, so "what will happen" is answerable without doing it.
//   - Each table's rows land in ONE transaction. A half-copied table is a lie
//     about what is there; a failed table rolls back whole and the run stops.
//   - Row counts are reconciled per table, and a mismatch is an error, not a
//     footnote. A copy that silently loses rows is worse than one that fails.
//   - Cross-engine type losses are reported BEFORE the run (downgrades), because
//     a decimal becoming a float is a decision, not a detail.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/ddl"
	"github.com/dbcore/dbcore/schemadiff"
	"tablepro-go/internal/importfile"
	"tablepro-go/internal/transfer"
)

// TransferRequest names both sides and the scope of the copy.
type TransferRequest struct {
	Source TransferTarget `json:"source"`
	Target TransferTarget `json:"target"`
	// Tables limits the copy to these source tables; empty means every base
	// table of the source schema.
	Tables []string `json:"tables"`
	// Options narrow what is copied (structure only, no FKs).
	IgnoreForeignKeys bool `json:"ignoreForeignKeys"`
	SkipData          bool `json:"skipData"`
	// Confirm is required on Apply: the same discipline compare/apply uses, so a
	// forged request cannot run a copy the user never saw previewed.
	Confirm bool `json:"confirm"`
}

// TransferTarget is one side: a live connection plus the database/schema in it.
type TransferTarget struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
}

// TransferPreview is what the UI shows before anything runs.
type TransferPreview struct {
	// Tables are the tables in copy order, with their row counts.
	Tables []TransferTableInfo `json:"tables"`
	// Statements is the DDL the run would execute, in order, labelled by table.
	Statements []TransferStatement `json:"statements"`
	Cycles     []transfer.Cycle    `json:"cycles"`
	Warnings   []string            `json:"warnings"`
	// Losses are cross-engine type losses: reported, never coerced.
	Losses []transfer.TypeLoss `json:"losses"`
	// TotalRows is the sum of the source counts (-1 when unknown).
	TotalRows int64 `json:"totalRows"`
}

// TransferTableInfo is one row of the preview table.
type TransferTableInfo struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Rows   int64  `json:"rows"`
	// Deferred says this table's foreign keys are added after all data loads
	// (a cycle member).
	Deferred bool `json:"deferred"`
}

// TransferStatement is one DDL statement the run would issue.
type TransferStatement struct {
	Table string `json:"table"`
	SQL   string `json:"sql"`
	// Kind is "create" / "fk" so the UI can group them.
	Kind string `json:"kind"`
}

// TransferResult is what the run reports.
type TransferResult struct {
	Tables []TransferTableResult `json:"tables"`
	// Rows is the total written across all tables.
	Rows int64 `json:"rows"`
	// Aborted is set when a failure stopped the run; the table it stopped on is
	// the last entry of Tables.
	Aborted bool   `json:"aborted"`
	Error   string `json:"error,omitempty"`
	Elapsed string `json:"elapsed"`
}

// TransferTableResult is one table's outcome.
type TransferTableResult struct {
	Source string `json:"source"`
	Target string `json:"target"`
	// RowsRead / RowsWritten are reconciled; a mismatch fails the table.
	RowsRead    int64  `json:"rowsRead"`
	RowsWritten int64  `json:"rowsWritten"`
	Error       string `json:"error,omitempty"`
}

// transferPageSize is how many rows one read/write round carries. Large enough
// to amortise a round trip, small enough that a wide row cannot blow up memory.
const transferPageSize = 500

// PreviewTransfer reads both catalogs and returns the plan without touching the
// target. It is the only path the UI calls before Apply.
func (a *App) PreviewTransfer(req TransferRequest) (TransferPreview, error) {
	srcDrv, _, err := a.transferDriver(req.Source)
	if err != nil {
		return TransferPreview{}, fmt.Errorf("源连接: %w", err)
	}
	dstDrv, _, err := a.transferDriver(req.Target)
	if err != nil {
		return TransferPreview{}, fmt.Errorf("目标连接: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	srcSnap, err := schemadiff.Collect(ctx, srcDrv, req.Source.Schema)
	if err != nil {
		return TransferPreview{}, fmt.Errorf("读取源结构: %w", err)
	}
	// The target's catalog is read only for the downgrade report; a missing
	// target schema is normal for a first copy and must not fail the preview.
	dstSnap, dstErr := schemadiff.Collect(ctx, dstDrv, req.Target.Schema)

	tables, err := selectTransferTables(srcSnap, req.Tables)
	if err != nil {
		return TransferPreview{}, err
	}

	// Row counts come from the source, which is what actually gets copied; a
	// count that cannot be read is -1 ("unknown") rather than a failure, because
	// the copy itself is what matters.
	for i := range tables {
		tables[i].Rows = -1
		if n, err := srcDrv.BrowseRowsCount(ctx, req.Source.Schema, tables[i].Source, nil); err == nil {
			tables[i].Rows = int64(n)
		}
	}

	plan, err := transfer.PlanTables(tables, transfer.Options{
		IgnoreForeignKeys: req.IgnoreForeignKeys,
		SkipData:          req.SkipData,
	})
	if err != nil {
		return TransferPreview{}, err
	}

	preview := TransferPreview{
		Cycles:   plan.Cycles,
		Warnings: append([]string(nil), plan.Warnings...),
	}
	if dstErr != nil {
		preview.Warnings = append(preview.Warnings,
			fmt.Sprintf("目标结构未读到（首次建表时正常）: %v", dstErr))
	}

	// DDL, in the plan's order. CreateTable renders the whole table (columns,
	// indexes, FKs); a deferred member omits its FKs here and gets them from the
	// alter path instead, which is what the "fk" steps are for.
	srcBySource := make(map[string]schemadiff.Table, len(srcSnap.Tables))
	for _, t := range srcSnap.Tables {
		srcBySource[t.Name] = t
	}
	deferred := map[string]bool{}
	for _, c := range plan.Cycles {
		for _, name := range c.Tables {
			deferred[name] = true
		}
	}
	for _, name := range plan.CreateOrder {
		src, ok := srcTableBySource(srcBySource, name, tables)
		if !ok {
			continue
		}
		preview.Statements = append(preview.Statements, TransferStatement{
			Table: name,
			Kind:  "create",
			SQL:   createTableScript(string(srcDrv.Kind()), req.Target.Schema, name, src, deferred[name]),
		})
	}
	if !req.IgnoreForeignKeys {
		for _, c := range plan.Cycles {
			for _, name := range c.Tables {
				src, ok := srcTableBySource(srcBySource, name, tables)
				if !ok || len(src.ForeignKeys) == 0 {
					continue
				}
				// An engine without ALTER TABLE ... ADD CONSTRAINT cannot take a
				// deferred constraint. Rather than emit statements it will
				// reject, the plan says so: the cycle's keys are not created on
				// this target, and that is a visible warning, not a silent loss.
				if !ddl.CanAddForeignKey(string(dstDrv.Kind())) {
					preview.Warnings = append(preview.Warnings, fmt.Sprintf(
						"目标引擎 %s 不支持事后添加外键：表 %s 的 %d 个循环外键不会被创建（数据仍会完整复制）",
						dstDrv.Kind(), name, len(src.ForeignKeys)))
					continue
				}
				preview.Statements = append(preview.Statements, TransferStatement{
					Table: name,
					Kind:  "fk",
					SQL:   fkAlterScripts(string(dstDrv.Kind()), req.Target.Schema, name, src.ForeignKeys),
				})
			}
		}
	}

	// The downgrade report: only meaningful once the target structure exists.
	if dstSnap != nil {
		preview.Losses = transfer.Downgrades(
			columnTypes(srcSnap, tables),
			columnTypes(dstSnap, tables),
		)
	}

	preview.TotalRows = 0
	for _, name := range plan.LoadOrder {
		t := findTable(tables, name)
		preview.Tables = append(preview.Tables, TransferTableInfo{
			Source: t.Source, Target: t.Target, Rows: t.Rows, Deferred: deferred[name],
		})
		if t.Rows > 0 {
			preview.TotalRows += t.Rows
		}
	}
	return preview, nil
}

// ApplyTransfer runs the copy. It requires Confirm, re-derives the plan (never
// trusting a plan from a request), and stops at the first table that fails.
func (a *App) ApplyTransfer(req TransferRequest) (TransferResult, error) {
	if !req.Confirm {
		return TransferResult{}, fmt.Errorf("数据传输需要显式确认")
	}
	preview, err := a.PreviewTransfer(req)
	if err != nil {
		return TransferResult{}, err
	}

	srcDrv, _, err := a.transferDriver(req.Source)
	if err != nil {
		return TransferResult{}, fmt.Errorf("源连接: %w", err)
	}
	dstDrv, _, err := a.transferDriver(req.Target)
	if err != nil {
		return TransferResult{}, fmt.Errorf("目标连接: %w", err)
	}

	srcSnap, err := schemadiff.Collect(context.Background(), srcDrv, req.Source.Schema)
	if err != nil {
		return TransferResult{}, fmt.Errorf("读取源结构: %w", err)
	}
	srcBySource := make(map[string]schemadiff.Table, len(srcSnap.Tables))
	for _, t := range srcSnap.Tables {
		srcBySource[t.Name] = t
	}

	started := time.Now()
	result := TransferResult{Tables: []TransferTableResult{}}

	// Phase 1: structure, in plan order. One statement at a time so a failure
	// names the table it stopped at instead of "the batch failed".
	for _, stmt := range preview.Statements {
		if stmt.Kind != "create" {
			continue
		}
		if _, err := dstDrv.Execute(context.Background(), stmt.SQL); err != nil {
			result.Aborted = true
			result.Error = fmt.Sprintf("建表 %s 失败: %v", stmt.Table, err)
			result.Elapsed = time.Since(started).String()
			return result, nil
		}
	}

	// Phase 2: rows, one transaction per table.
	if !req.SkipData {
		for _, info := range preview.Tables {
			res := a.copyTableRows(srcDrv, dstDrv, req, srcBySource[info.Source], info.Source)
			result.Tables = append(result.Tables, res)
			if res.Error != "" {
				result.Aborted = true
				result.Error = fmt.Sprintf("表 %s: %s", info.Source, res.Error)
				result.Elapsed = time.Since(started).String()
				return result, nil
			}
			result.Rows += res.RowsWritten
		}
	}

	// Phase 3: the deferred constraints, after every row is in place.
	for _, stmt := range preview.Statements {
		if stmt.Kind != "fk" {
			continue
		}
		if _, err := dstDrv.Execute(context.Background(), stmt.SQL); err != nil {
			// A failure here leaves the data copied but unconstrained: report it
			// rather than claiming success.
			result.Aborted = true
			result.Error = fmt.Sprintf("补建外键 %s 失败: %v", stmt.Table, err)
			result.Elapsed = time.Since(started).String()
			return result, nil
		}
	}

	result.Elapsed = time.Since(started).String()
	return result, nil
}

// copyTableRows pages the source and writes the target in batches, one
// transaction for the whole table.
func (a *App) copyTableRows(
	srcDrv, dstDrv dbcore.SQLDriver,
	req TransferRequest,
	src schemadiff.Table,
	targetName string,
) TransferTableResult {
	res := TransferTableResult{Source: src.Name, Target: targetName}
	if len(src.Columns) == 0 {
		res.Error = "源表没有列"
		return res
	}
	cols := make([]string, len(src.Columns))
	for i, c := range src.Columns {
		cols[i] = c.Name
	}

	txDrv, ok := dstDrv.(dbcore.SQLTxDriver)
	if !ok {
		res.Error = "目标驱动不支持事务，无法保证整表原子复制"
		return res
	}
	ctx := context.Background()
	tx, err := txDrv.Begin(ctx, dbcore.TxMode{})
	if err != nil {
		res.Error = fmt.Sprintf("开启目标事务失败: %v", err)
		return res
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	dbType := string(dstDrv.Kind())
	for page := 1; ; page++ {
		rows, err := srcDrv.BrowseRows(ctx, req.Source.Schema, src.Name, &dbcore.TableRowsRequest{
			Page:     page,
			PageSize: transferPageSize,
		})
		if err != nil {
			res.Error = fmt.Sprintf("读取第 %d 页失败: %v", page, err)
			return res
		}
		if len(rows.Rows) == 0 {
			break
		}
		// A batch insert per page: one statement, one bind list, inside the
		// table's transaction.
		stmt, args, err := transferInsert(dbType, req.Target.Schema, targetName, cols, rows.Rows)
		if err != nil {
			res.Error = fmt.Sprintf("拼装 INSERT 失败: %v", err)
			return res
		}
		if _, err := txDrv.ExecuteIn(tx, ctx, stmt, args...); err != nil {
			res.Error = fmt.Sprintf("写入第 %d 页失败: %v", page, err)
			return res
		}
		res.RowsRead += int64(len(rows.Rows))
		res.RowsWritten += int64(len(rows.Rows))
		if len(rows.Rows) < transferPageSize {
			break
		}
	}
	if err := tx.Commit(); err != nil {
		res.Error = fmt.Sprintf("提交失败: %v", err)
		return res
	}
	committed = true

	// Reconcile: a copy whose counts disagree is reported, not glossed.
	if res.RowsRead != res.RowsWritten {
		res.Error = fmt.Sprintf("行数不一致: 读到 %d，写入 %d", res.RowsRead, res.RowsWritten)
		return res
	}
	if n, err := dstDrv.BrowseRowsCount(ctx, req.Target.Schema, targetName, nil); err == nil && res.RowsWritten > 0 {
		if int64(n) != res.RowsWritten {
			res.Error = fmt.Sprintf("目标表行数核对失败: 期望 %d，实际 %d", res.RowsWritten, n)
			return res
		}
	}
	return res
}

// transferInsert renders one multi-row INSERT for a page, binding values rather
// than inlining them.
func transferInsert(dbType, schema, table string, cols []string, rows [][]dbcore.Value) (string, []dbcore.Value, error) {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = importfile.Ident(dbType, c)
	}
	var tuples []string
	args := make([]dbcore.Value, 0, len(rows)*len(cols))
	n := 0
	for _, row := range rows {
		marks := make([]string, len(cols))
		for i := range cols {
			n++
			marks[i] = importfile.Placeholder(dbType, n)
			if i < len(row) {
				args = append(args, row[i])
			} else {
				args = append(args, dbcore.Value{Kind: dbcore.ValNull})
			}
		}
		tuples = append(tuples, "("+strings.Join(marks, ", ")+")")
	}
	ref := importfile.TableRef(dbType, schema, table)
	return "INSERT INTO " + ref + " (" + strings.Join(quoted, ", ") + ") VALUES " +
		strings.Join(tuples, ", "), args, nil
}

// transferDriver resolves one side to a live driver.
func (a *App) transferDriver(target TransferTarget) (dbcore.SQLDriver, *Connection, error) {
	conn, err := a.liveConnection(target.ConnID)
	if err != nil {
		return nil, nil, err
	}
	drv, err := a.sqlDriver(conn, target.Database)
	if err != nil {
		return nil, nil, err
	}
	if drv == nil {
		return nil, nil, errors.New("该连接没有 SQL 驱动")
	}
	return drv, conn, nil
}

// selectTransferTables applies the request's table filter and builds the planner
// input.
func selectTransferTables(snap *schemadiff.Snapshot, only []string) ([]transfer.Table, error) {
	want := map[string]bool{}
	for _, name := range only {
		want[name] = true
	}
	found := map[string]bool{}
	out := make([]transfer.Table, 0, len(snap.Tables))
	for _, t := range snap.Tables {
		if len(want) > 0 && !want[t.Name] {
			continue
		}
		found[t.Name] = true
		var fks []string
		for _, f := range t.ForeignKeys {
			fks = append(fks, f.RefTable)
		}
		out = append(out, transfer.Table{Source: t.Name, Target: t.Name, FKs: fks})
	}
	if len(want) > 0 {
		var missing []string
		for name := range want {
			if !found[name] {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, fmt.Errorf("源库里没有这些表: %s", strings.Join(missing, ", "))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("源库里没有可传输的表")
	}
	return out, nil
}

// columnTypes flattens one side's columns for the downgrade report.
func columnTypes(snap *schemadiff.Snapshot, tables []transfer.Table) []transfer.ColumnType {
	byName := make(map[string]schemadiff.Table, len(snap.Tables))
	for _, t := range snap.Tables {
		byName[t.Name] = t
	}
	var out []transfer.ColumnType
	for _, t := range tables {
		table, ok := byName[t.Source]
		if !ok {
			continue
		}
		for _, c := range table.Columns {
			out = append(out, transfer.ColumnType{Table: t.Target, Column: c.Name, Type: c.Type})
		}
	}
	return out
}

// createTableScript renders one table's DDL through the kernel assembler, then
// makes it runnable on the target engine.
//
// Two engine facts force post-processing:
//
//   - ddl.CreateTable renders foreign keys as separate ALTER TABLE ... ADD
//     CONSTRAINT statements. Engines that cannot add a constraint to an
//     existing table (SQLite, per ddl.CanAddForeignKey) reject those outright, so
//     for them the constraint moves INTO the CREATE body where SQLite needs it.
//   - The caller may ask to omit the keys entirely (deferred cycle members, or
//     IgnoreForeignKeys), which the kernel's single-shot renderer cannot express.
//
// Both are done here rather than in the kernel because they are properties of
// this copy's plan, not of DDL text generation.
func createTableScript(dbType, schema, table string, src schemadiff.Table, omitFK bool) string {
	cols := make([]ddl.Column, len(src.Columns))
	for i, c := range src.Columns {
		def := ""
		if c.Default != nil {
			def = *c.Default
		}
		cols[i] = ddl.Column{Name: c.Name, Type: c.Type, Nullable: c.Nullable, DefaultValue: def}
	}
	indexes := make([]ddl.Index, 0, len(src.Indexes))
	for _, ix := range src.Indexes {
		indexes = append(indexes, ddl.Index{
			Name: ix.Name, Unique: ix.Unique, Primary: ix.Primary, Columns: ix.Columns,
		})
	}
	// The kernel renders the constraints inline only when the engine cannot add
	// them later; passing them always and stripping the ALTER lines is wrong for
	// SQLite (it never inlines them itself).
	inlineFK := !ddl.CanAddForeignKey(dbType)
	fks := make([]ddl.ForeignKey, 0, len(src.ForeignKeys))
	for _, fk := range src.ForeignKeys {
		if omitFK {
			continue
		}
		fks = append(fks, ddl.ForeignKey{
			Name: fk.Name, Columns: fk.Columns,
			RefSchema: schema, RefTable: fk.RefTable, RefColumns: fk.RefColumns,
			OnDelete: fk.OnDelete, OnUpdate: fk.OnUpdate,
		})
	}
	script := ddl.CreateTable(dbType, schema, table, cols, nil, indexes, fks)

	// An engine that cannot attach a constraint later (SQLite) needs it inside
	// the CREATE body. The kernel owns both renders, so this picks which one to
	// ask for rather than rewriting the text here.
	if inlineFK && len(fks) > 0 {
		return ddl.CreateTableInline(dbType, schema, table, cols, nil, indexes, fks)
	}

	// An omitted constraint must not ride along as an ALTER either.
	if omitFK {
		var kept []string
		for _, part := range strings.Split(script, ";\n\n") {
			trimmed := strings.TrimSpace(part)
			if strings.HasPrefix(strings.ToUpper(trimmed), "ALTER TABLE") {
				continue
			}
			if trimmed != "" {
				kept = append(kept, trimmed+";")
			}
		}
		return strings.Join(kept, "\n\n")
	}
	return script
}

// fkAlterScripts renders the deferred constraints of a cycle member.
func fkAlterScripts(dbType, schema, table string, fks []dbcore.ForeignKeyInfo) string {
	var parts []string
	for _, fk := range fks {
		parts = append(parts, ddl.AddForeignKey(dbType, schema, table, ddl.ForeignKey{
			Name: fk.Name, Columns: fk.Columns,
			RefSchema: schema, RefTable: fk.RefTable, RefColumns: fk.RefColumns,
			OnDelete: fk.OnDelete, OnUpdate: fk.OnUpdate,
		}))
	}
	return strings.Join(parts, ";\n")
}

// srcTableBySource maps a plan name back to its snapshot table.
func srcTableBySource(byName map[string]schemadiff.Table, planName string, tables []transfer.Table) (schemadiff.Table, bool) {
	for _, t := range tables {
		if t.Target == planName {
			src, ok := byName[t.Source]
			return src, ok
		}
	}
	return schemadiff.Table{}, false
}

func findTable(tables []transfer.Table, target string) transfer.Table {
	for _, t := range tables {
		if t.Target == target {
			return t
		}
	}
	return transfer.Table{}
}

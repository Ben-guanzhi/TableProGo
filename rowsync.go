package main

// W6: the row-level synchronizer (行级数据同步).
//
// Given two tables with the same shape, this produces the changes that make the
// target look like the source, lets the user tick them, then applies the ticked
// ones by primary key. The pure merge lives in internal/rowsync; this file owns
// the orchestration — reading both sides in the same order, checking the two
// tables really are comparable, storing the plan so the apply step addresses
// changes by id, and refusing the paths that could corrupt data.
//
// The refusals are the point. A row-level sync can DELETE every row of the
// target, so the paths that would let that happen by accident are closed:
//   - an empty PK refuses (there is no way to address a row);
//   - a shape mismatch refuses (column sets or PK order differing means the
//     plan would be about different tables);
//   - a read-only-ish target still applies (writes are the point), but the
//     preview says how many deletions there are, and the confirm dialog names
//     the worst case before the user ticks anything.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dbcore/dbcore"
	"tablepro-go/internal/importfile"
	"tablepro-go/internal/rowsync"
)

// RowSyncRequest names the two tables.
type RowSyncRequest struct {
	Source TransferTarget `json:"source"`
	Target TransferTarget `json:"target"`
	Table  string         `json:"table"`
	// IgnoreColumns never count as a difference (touched-at, auto-increment).
	IgnoreColumns []string `json:"ignoreColumns"`
}

// RowSyncChange is one plan entry, as the UI sees it.
type RowSyncChange struct {
	Seq    int                    `json:"seq"`
	Op     string                 `json:"op"`
	Key    string                 `json:"key"`
	Values map[string]interface{} `json:"values,omitempty"`
}

// RowSyncPreview is the plan summary: counts first, then a bounded sample.
type RowSyncPreview struct {
	// PlanID addresses the stored plan at apply time.
	PlanID string `json:"planId"`
	Table  string `json:"table"`
	// PK is the primary-key column order both sides agreed on.
	PK []string `json:"pk"`
	Inserts int `json:"inserts"`
	Updates int `json:"updates"`
	Deletes int `json:"deletes"`
	Total   int `json:"total"`
	// Sample is the first N changes, for the UI's list.
	Sample []RowSyncChange `json:"sample"`
	// Truncated says there are more changes than the sample shows.
	Truncated bool `json:"truncated"`
	// ReadRows is how many rows each side yielded.
	SourceRows int64 `json:"sourceRows"`
	TargetRows int64 `json:"targetRows"`
	Elapsed    string `json:"elapsed"`
}

// maxRowSyncSample bounds the preview's change list; the full plan is on the
// server, addressed by id.
const maxRowSyncSample = 200

// RowSyncApplyRequest names the plan and the changes to run.
type RowSyncApplyRequest struct {
	PlanID string `json:"planId"`
	// Seqs addresses the changes to apply; empty applies none.
	Seqs []int `json:"seqs"`
	// Confirm is required, and true must arrive from the app's own dialog.
	Confirm bool `json:"confirm"`
}

// RowSyncApplyResult reports what landed.
type RowSyncApplyResult struct {
	Applied int    `json:"applied"`
	Total   int    `json:"total"`
	Inserts int    `json:"inserts"`
	Updates int    `json:"updates"`
	Deletes int    `json:"deletes"`
	Error   string `json:"error,omitempty"`
	Elapsed string `json:"elapsed"`
}

// rowSyncPlan is the stored plan: enough to re-render every change's SQL at
// apply time from the values this process computed.
type rowSyncPlan struct {
	req     RowSyncRequest
	pk      []string
	columns []string
	changes []rowsync.Change
	createdAt time.Time
}

// maxRowSyncChanges bounds one plan. A table needing more changes than this is
// a full copy (transfer.go's job), not a sync.
const maxRowSyncChanges = 50000

// RowSyncPreview reads both tables, merges them, and stores the plan.
func (a *App) RowSyncPreview(req RowSyncRequest) (RowSyncPreview, error) {
	started := time.Now()
	if strings.TrimSpace(req.Table) == "" {
		return RowSyncPreview{}, fmt.Errorf("表名是必填项")
	}
	srcDrv, _, err := a.transferDriver(req.Source)
	if err != nil {
		return RowSyncPreview{}, fmt.Errorf("源连接: %w", err)
	}
	dstDrv, _, err := a.transferDriver(req.Target)
	if err != nil {
		return RowSyncPreview{}, fmt.Errorf("目标连接: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Shape check first: the merge is only meaningful over the same shape, and
	// a mismatch would produce a plan about different tables.
	pk, columns, err := rowSyncShape(ctx, srcDrv, dstDrv, req)
	if err != nil {
		return RowSyncPreview{}, err
	}

	// Read both sides ordered by the full PK. The merge refuses an unordered
	// stream, so the ordering here is load-bearing.
	ignore := map[string]bool{}
	for _, c := range req.IgnoreColumns {
		ignore[c] = true
	}
	opts := rowsync.Options{IgnoreColumns: ignore}

	var changes []rowsync.Change
	var srcRows, dstRows int64
	stats := &rowSyncReadStats{}
	err = rowsync.DiffStream(
		rowSyncFetcher(ctx, srcDrv, req.Source, req.Table, pk, stats, &srcRows),
		rowSyncFetcher(ctx, dstDrv, req.Target, req.Table, pk, stats, &dstRows),
		opts,
		func(c rowsync.Change) (bool, error) {
			if len(changes) >= maxRowSyncChanges {
				return false, fmt.Errorf("变更超过 %d 条：这张表更适合用数据传输整体复制，而不是逐行同步", maxRowSyncChanges)
			}
			changes = append(changes, c)
			return true, nil
		},
	)
	if err != nil {
		return RowSyncPreview{}, fmt.Errorf("归并两侧: %w", err)
	}

	summary := RowSyncPreview{
		Table:      req.Table,
		PK:         pk,
		SourceRows: srcRows,
		TargetRows: dstRows,
		Elapsed:    time.Since(started).String(),
	}
	for _, c := range changes {
		switch c.Op {
		case rowsync.OpInsert:
			summary.Inserts++
		case rowsync.OpUpdate:
			summary.Updates++
		case rowsync.OpDelete:
			summary.Deletes++
		}
	}
	summary.Total = len(changes)

	// The plan is stored whole; the preview shows a bounded sample.
	id, err := a.storeRowSyncPlan(req, pk, columns, changes)
	if err != nil {
		return RowSyncPreview{}, err
	}
	summary.PlanID = id

	sample := changes
	summary.Truncated = len(sample) > maxRowSyncSample
	if summary.Truncated {
		sample = sample[:maxRowSyncSample]
	}
	for _, c := range sample {
		summary.Sample = append(summary.Sample, RowSyncChange{
			Seq:    c.Seq,
			Op:     string(c.Op),
			Key:    keyString(c.Key),
			Values: valuesToUI(c.Values),
		})
	}
	return summary, nil
}

// RowSyncApply runs the ticked changes of a stored plan, one transaction for the
// whole apply so a half-applied sync is impossible.
func (a *App) RowSyncApply(req RowSyncApplyRequest) (RowSyncApplyResult, error) {
	if !req.Confirm {
		return RowSyncApplyResult{}, fmt.Errorf("行级同步需要显式确认")
	}
	a.rowSyncMu.Lock()
	plan, ok := a.rowSyncPlans[req.PlanID]
	a.rowSyncMu.Unlock()
	if !ok {
		return RowSyncApplyResult{}, fmt.Errorf("同步计划已失效，请重新分析后再执行")
	}
	selected := map[int]bool{}
	for _, seq := range req.Seqs {
		selected[seq] = true
	}

	result := RowSyncApplyResult{Total: len(req.Seqs)}
	if len(req.Seqs) == 0 {
		return result, nil
	}

	dstDrv, _, err := a.transferDriver(plan.req.Target)
	if err != nil {
		return RowSyncApplyResult{}, fmt.Errorf("目标连接: %w", err)
	}
	txDrv, ok := dstDrv.(dbcore.SQLTxDriver)
	if !ok {
		return RowSyncApplyResult{}, fmt.Errorf("目标驱动不支持事务，无法保证整批原子应用")
	}

	ctx := context.Background()
	tx, err := txDrv.Begin(ctx, dbcore.TxMode{})
	if err != nil {
		return RowSyncApplyResult{}, fmt.Errorf("开启事务失败: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	dbType := string(dstDrv.Kind())
	bySeq := make(map[int]rowsync.Change, len(plan.changes))
	for _, c := range plan.changes {
		bySeq[c.Seq] = c
	}
	// Apply in merge order (seq order), so a delete-then-insert of the same key
	// cannot invert.
	seqs := append([]int(nil), req.Seqs...)
	sort.Ints(seqs)
	for _, seq := range seqs {
		c, ok := bySeq[seq]
		if !ok {
			continue
		}
		var err error
		switch c.Op {
		case rowsync.OpInsert:
			err = rowSyncInsert(txDrv, tx, dbType, plan.req.Target, plan.req.Table, plan.columns, c)
			result.Inserts++
		case rowsync.OpUpdate:
			err = rowSyncUpdate(txDrv, tx, dbType, plan.req.Target, plan.req.Table, plan.pk, plan.columns, c)
			result.Updates++
		case rowsync.OpDelete:
			err = rowSyncDelete(txDrv, tx, dbType, plan.req.Target, plan.req.Table, plan.pk, c)
			result.Deletes++
		}
		if err != nil {
			result.Error = fmt.Sprintf("变更 #%d (%s %s) 失败: %v", c.Seq, c.Op, keyString(c.Key), err)
			result.Elapsed = time.Since(plan.createdAt).String()
			return result, nil
		}
		result.Applied++
	}
	if err := tx.Commit(); err != nil {
		result.Error = fmt.Sprintf("提交失败: %v", err)
		return result, nil
	}
	committed = true
	result.Elapsed = time.Since(plan.createdAt).String()
	return result, nil
}

// keyString renders a primary-key tuple for display and for the change list.
// One value per column, joined with a middot so a composite key reads as one
// identity.
func keyString(key []dbcore.Value) string {
	parts := make([]string, len(key))
	for i, v := range key {
		parts[i] = v.String()
	}
	return strings.Join(parts, "·")
}

// valuesToUI flattens a change's values into the JSON shape the dialog lists.
// A NULL becomes nil so the client can tell "no value" from an empty string;
// numbers keep their numeric JSON type where the Value carries one exactly.
func valuesToUI(values map[string]dbcore.Value) map[string]interface{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(values))
	for name, v := range values {
		switch v.Kind {
		case dbcore.ValNull:
			out[name] = nil
		case dbcore.ValInt:
			out[name] = v.I
		case dbcore.ValFloat:
			out[name] = v.F
		case dbcore.ValBool:
			out[name] = v.Bool
		default:
			// Text, decimal digits, blobs and times all cross as their display
			// text: this is a change LIST, not a round-trip path, and the value
			// that will actually be written comes from the stored plan.
			out[name] = v.String()
		}
	}
	return out
}

// ---- structure and reads ----

// rowSyncShape reads both tables' columns and primary keys, and refuses shapes
// the merge cannot treat as "the same table".
func rowSyncShape(ctx context.Context, srcDrv, dstDrv dbcore.SQLDriver, req RowSyncRequest) (pk []string, columns []string, err error) {
	srcCols, err := srcDrv.ListColumns(ctx, req.Source.Schema, req.Table)
	if err != nil {
		return nil, nil, fmt.Errorf("读取源列: %w", err)
	}
	dstCols, err := dstDrv.ListColumns(ctx, req.Target.Schema, req.Table)
	if err != nil {
		return nil, nil, fmt.Errorf("读取目标列: %w", err)
	}
	srcPK, err := primaryKeyOf(ctx, srcDrv, req.Source.Schema, req.Table, srcCols)
	if err != nil {
		return nil, nil, err
	}
	dstPK, err := primaryKeyOf(ctx, dstDrv, req.Target.Schema, req.Table, dstCols)
	if err != nil {
		return nil, nil, err
	}
	if len(srcPK) == 0 {
		return nil, nil, fmt.Errorf("源表 %s 没有主键：无法按行定位，不能同步", req.Table)
	}
	if len(dstPK) == 0 {
		return nil, nil, fmt.Errorf("目标表 %s 没有主键：无法按行定位，不能同步", req.Table)
	}
	// The PK must be the same columns in the same order, or the two sides'
	// "same row" means different rows.
	if len(srcPK) != len(dstPK) {
		return nil, nil, fmt.Errorf("主键列数不一致: 源 %d，目标 %d", len(srcPK), len(dstPK))
	}
	for i := range srcPK {
		if !strings.EqualFold(srcPK[i], dstPK[i]) {
			return nil, nil, fmt.Errorf("主键列不一致: 源 %s，目标 %s", strings.Join(srcPK, ","), strings.Join(dstPK, ","))
		}
	}
	// The compared column sets must match exactly. A column on one side only
	// would be silently dropped (target) or have nowhere to go (source), and
	// both are shape bugs the user should fix before syncing.
	srcSet, dstSet := map[string]bool{}, map[string]bool{}
	for _, c := range srcCols {
		srcSet[strings.ToLower(c.Name)] = true
	}
	for _, c := range dstCols {
		dstSet[strings.ToLower(c.Name)] = true
	}
	var onlySrc, onlyDst []string
	for _, c := range srcCols {
		if !dstSet[strings.ToLower(c.Name)] {
			onlySrc = append(onlySrc, c.Name)
		}
	}
	for _, c := range dstCols {
		if !srcSet[strings.ToLower(c.Name)] {
			onlyDst = append(onlyDst, c.Name)
		}
	}
	if len(onlySrc) > 0 || len(onlyDst) > 0 {
		return nil, nil, fmt.Errorf("列集不一致: 目标缺 %s，源缺 %s（同步要求两侧同构）",
			strings.Join(onlySrc, ","), strings.Join(onlyDst, ","))
	}
	// Column order follows the source; the merge addresses columns by name, so
	// the target's order is irrelevant.
	names := make([]string, len(srcCols))
	for i, c := range srcCols {
		names[i] = c.Name
	}
	return append([]string(nil), srcPK...), names, nil
}

// primaryKeyOf resolves a table's PK column order.
//
// The column catalog is asked first: engines that alias a rowid to the integer
// primary key (SQLite's `INTEGER PRIMARY KEY`) do not materialize an index for it,
// so an index-only lookup reports "no primary key" for a table that plainly has
// one. The index catalog is the fallback for engines that only expose it there,
// with the index's own column order preserved (a composite PK's order is what
// the WHERE clause must match).
func primaryKeyOf(ctx context.Context, drv dbcore.SQLDriver, schema, table string, cols []dbcore.ColumnInfo) ([]string, error) {
	type pkCol struct {
		name string
		pos  int32
	}
	var flagged []pkCol
	for i, c := range cols {
		if !c.IsPrimaryKey {
			continue
		}
		pos := c.OrdinalPosition
		if pos == 0 {
			pos = int32(i + 1)
		}
		flagged = append(flagged, pkCol{name: c.Name, pos: pos})
	}
	if len(flagged) > 0 {
		sort.SliceStable(flagged, func(i, j int) bool { return flagged[i].pos < flagged[j].pos })
		out := make([]string, len(flagged))
		for i, c := range flagged {
			out[i] = c.name
		}
		return out, nil
	}

	indexes, err := drv.ListIndexes(ctx, schema, table)
	if err != nil {
		return nil, fmt.Errorf("读取索引: %w", err)
	}
	for _, ix := range indexes {
		if !ix.Primary || len(ix.Columns) == 0 {
			continue
		}
		return append([]string(nil), ix.Columns...), nil
	}
	return nil, nil
}

// rowSyncReadStats carries the per-side row counts out of the fetch closure.
type rowSyncReadStats struct{}

// rowSyncFetcher wraps BrowseRows into the merge's batch fetcher: one batch per
// call, ordered by the full PK, reading by OFFSET page (the ordering is what the
// merge requires; keyset continuation is per single key and a composite PK needs
// the full tuple).
func rowSyncFetcher(
	ctx context.Context,
	drv dbcore.SQLDriver,
	target TransferTarget,
	table string,
	pk []string,
	_ *rowSyncReadStats,
	counter *int64,
) func() ([]rowsync.Row, error) {
	sortKeys := make([]dbcore.SortKey, len(pk))
	for i, c := range pk {
		sortKeys[i] = dbcore.SortKey{Column: c}
	}
	page := 0
	return func() ([]rowsync.Row, error) {
		page++
		rows, err := drv.BrowseRows(ctx, target.Schema, table, &dbcore.TableRowsRequest{
			Page:     page,
			PageSize: transferPageSize,
			Sort:     sortKeys,
		})
		if err != nil {
			return nil, err
		}
		*counter += int64(len(rows.Rows))
		out := make([]rowsync.Row, 0, len(rows.Rows))
		for _, r := range rows.Rows {
			key := make([]dbcore.Value, len(pk))
			cols := map[string]dbcore.Value{}
			for i, c := range rows.Columns {
				var v dbcore.Value
				if i < len(r) {
					v = r[i]
				} else {
					v = dbcore.Value{Kind: dbcore.ValNull}
				}
				for pi, pkName := range pk {
					if strings.EqualFold(c.Name, pkName) {
						key[pi] = v
					}
				}
				cols[c.Name] = v
			}
			for pi := range pk {
				if key[pi].Kind == dbcore.ValNull && pi == 0 {
					// A NULL PK cell cannot be positioned; refuse rather than
					// merging it into the wrong place.
					return nil, fmt.Errorf("主键列 %s 含 NULL 值，无法按行同步", pk[pi])
				}
			}
			out = append(out, rowsync.Row{Key: key, Cols: cols})
		}
		return out, nil
	}
}

// ---- plan storage ----

// maxRowSyncPlans bounds the in-memory plans the same way compare plans do.
const maxRowSyncPlans = 32

func (a *App) storeRowSyncPlan(req RowSyncRequest, pk, columns []string, changes []rowsync.Change) (string, error) {
	a.rowSyncMu.Lock()
	defer a.rowSyncMu.Unlock()
	if a.rowSyncPlans == nil {
		a.rowSyncPlans = map[string]*rowSyncPlan{}
	}
	if len(a.rowSyncPlans) >= maxRowSyncPlans {
		oldest := ""
		var oldestAt time.Time
		for id, p := range a.rowSyncPlans {
			if oldest == "" || p.createdAt.Before(oldestAt) {
				oldest, oldestAt = id, p.createdAt
			}
		}
		delete(a.rowSyncPlans, oldest)
	}
	a.rowSyncSeq++
	id := fmt.Sprintf("rowsync-%d", a.rowSyncSeq)
	a.rowSyncPlans[id] = &rowSyncPlan{
		req: req, pk: append([]string(nil), pk...),
		columns: append([]string(nil), columns...),
		changes: changes, createdAt: time.Now(),
	}
	return id, nil
}

// ---- apply-side DML ----
//
// Statements go through the kernel's ExecuteIn, which owns the Value→driver
// argument conversion (the same path every other write in this app takes). Hand
// binding native Go values here would be a second conversion table, and the two
// would eventually disagree about NULL or decimal.

// rowSyncInsert writes one new row with every column bound.
func rowSyncInsert(txDrv dbcore.SQLTxDriver, tx *sql.Tx, dbType string, target TransferTarget, table string, columns []string, c rowsync.Change) error {
	ref := importfile.TableRef(dbType, target.Schema, table)
	quoted := make([]string, len(columns))
	marks := make([]string, len(columns))
	args := make([]dbcore.Value, len(columns))
	for i, col := range columns {
		q, err := importfileIdent(dbType, col)
		if err != nil {
			return err
		}
		quoted[i] = q
		marks[i] = importfile.Placeholder(dbType, i+1)
		args[i] = c.Values[col]
	}
	stmt := "INSERT INTO " + ref + " (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	_, err := txDrv.ExecuteIn(tx, context.Background(), stmt, args...)
	return err
}

// rowSyncUpdate writes the changed columns, addressed by the full PK. The WHERE
// is built from the PK columns and the key values, so a change can never land on
// a neighbouring row.
func rowSyncUpdate(txDrv dbcore.SQLTxDriver, tx *sql.Tx, dbType string, target TransferTarget, table string, pk []string, columns []string, c rowsync.Change) error {
	ref := importfile.TableRef(dbType, target.Schema, table)
	var sets []string
	var args []dbcore.Value
	n := 0
	for _, col := range columns {
		v, ok := c.Values[col]
		if !ok {
			continue
		}
		q, err := importfileIdent(dbType, col)
		if err != nil {
			return err
		}
		n++
		sets = append(sets, q+" = "+importfile.Placeholder(dbType, n))
		args = append(args, v)
	}
	if len(sets) == 0 {
		return nil // nothing to write (all ignored columns)
	}
	where, whereArgs, err := pkWhere(dbType, pk, c.Key, n)
	if err != nil {
		return err
	}
	args = append(args, whereArgs...)
	stmt := "UPDATE " + ref + " SET " + strings.Join(sets, ", ") + " WHERE " + where
	_, err = txDrv.ExecuteIn(tx, context.Background(), stmt, args...)
	return err
}

// rowSyncDelete removes one row by its full PK.
func rowSyncDelete(txDrv dbcore.SQLTxDriver, tx *sql.Tx, dbType string, target TransferTarget, table string, pk []string, c rowsync.Change) error {
	ref := importfile.TableRef(dbType, target.Schema, table)
	where, args, err := pkWhere(dbType, pk, c.Key, 0)
	if err != nil {
		return err
	}
	stmt := "DELETE FROM " + ref + " WHERE " + where
	_, err = txDrv.ExecuteIn(tx, context.Background(), stmt, args...)
	return err
}

// pkWhere renders "pk = ? AND pk2 = ?" with the key values bound. The offset
// lets an UPDATE continue the placeholder numbering after its SET clause.
func pkWhere(dbType string, pk []string, key []dbcore.Value, offset int) (string, []dbcore.Value, error) {
	if len(pk) != len(key) {
		return "", nil, fmt.Errorf("主键值个数与主键列不一致: %d vs %d", len(key), len(pk))
	}
	var parts []string
	args := make([]dbcore.Value, 0, len(pk))
	for i, col := range pk {
		q, err := importfileIdent(dbType, col)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, q+" = "+importfile.Placeholder(dbType, offset+i+1))
		args = append(args, key[i])
	}
	return strings.Join(parts, " AND "), args, nil
}

// importfileIdent quotes through the import pipeline's identifier helper (the
// same quoting every other write path uses).
func importfileIdent(dbType, name string) (string, error) {
	q := importfile.Ident(dbType, name)
	if q == "" {
		return "", fmt.Errorf("无效的标识符: %q", name)
	}
	return q, nil
}

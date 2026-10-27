package main

// 结构可视编辑（P2-7）：列级的加/删/改，走内核 ddl 包渲染 ALTER 语句。
// 客户端不做方言判断——能不能改列、默认值怎么拼，全由 ddl 包的语句构造与
// Can* 谓词回答；SQLite 不能表达时给出明确的 warning 而不是假语句。

import (
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/ddl"
	"tablepro-go/internal/catalogmodel"
)

// ColumnEdit is one row of the structure editor's form.
type ColumnEdit struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	DefaultValue string `json:"defaultValue"`
	// IsNew marks rows the user added; Original holds the pre-edit type for
	// change detection on existing rows.
	IsNew bool `json:"isNew"`
}

// IndexEdit is one row of the structure editor's index tab.
type IndexEdit struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Columns []string `json:"columns"`
	// IsNew marks indexes the user added; existing ones are matched by name,
	// case-insensitively, like the columns.
	IsNew bool `json:"isNew"`
}

// ForeignKeyEdit is one row of the structure editor's foreign key tab.
type ForeignKeyEdit struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete"`
	OnUpdate   string   `json:"onUpdate"`
	IsNew      bool     `json:"isNew"`
}

// StructureEditRequest is the API payload: the full desired column list plus
// (P2-7) the desired indexes and foreign keys. A nil Indexes/ForeignKeys means
// "this editor does not touch them" — only a non-nil list diffs against the
// catalog. That keeps the column-only dialog, which knows nothing about the new
// fields, exactly as safe as it was.
type StructureEditRequest struct {
	ConnID      string           `json:"connId"`
	Database    string           `json:"database"`
	Schema      string           `json:"schema"`
	Table       string           `json:"table"`
	Columns     []ColumnEdit     `json:"columns"`
	Indexes     []IndexEdit      `json:"indexes,omitempty"`
	ForeignKeys []ForeignKeyEdit `json:"foreignKeys,omitempty"`
}

// StructureEditResult carries the rendered statements and (on apply) what ran.
type StructureEditResult struct {
	Statements []string `json:"statements"`
	Warnings   []string `json:"warnings"`
}

// buildStructureEdits diffs the desired column list against the catalog and
// renders statements through the kernel ddl package. Name matching is
// case-insensitive: Oracle folds case, so renaming by case is not an edit.
func (a *App) buildStructureEdits(req StructureEditRequest) (StructureEditResult, error) {
	res := StructureEditResult{Warnings: []string{}}
	conn, err := a.connection(req.ConnID)
	if err != nil {
		return res, err
	}
	if req.Table == "" {
		return res, fmt.Errorf("没有目标表")
	}
	dbType := conn.Type
	schema := defaultSchema(dbType, req.Schema)
	if dbType == "mysql" && schema == "" {
		schema = req.Database
	}

	current, err := a.GetColumns(req.ConnID, req.Database, schema, req.Table)
	if err != nil {
		return res, err
	}
	currentByName := make(map[string]catalogmodel.ColumnInfo, len(current))
	for _, c := range current {
		currentByName[strings.ToLower(c.Name)] = c
	}

	kept := map[string]bool{}
	for i, want := range req.Columns {
		name := strings.TrimSpace(want.Name)
		if name == "" {
			return res, fmt.Errorf("第 %d 行的列名为空", i+1)
		}
		key := strings.ToLower(name)
		if kept[key] {
			return res, fmt.Errorf("列 %s 重复出现", name)
		}
		kept[key] = true
		before, exists := currentByName[key]
		if !exists {
			col := ddl.Column{Name: name, Type: want.Type, Nullable: want.Nullable, DefaultValue: want.DefaultValue}
			res.Statements = append(res.Statements, ddl.AddColumn(dbType, schema, req.Table, col))
			continue
		}
		typeChanged := !strings.EqualFold(before.Type, want.Type)
		nullableChanged := before.Nullable != want.Nullable
		defaultChanged := before.DefaultValue != want.DefaultValue
		if !typeChanged && !nullableChanged && !defaultChanged {
			continue
		}
		if !ddl.CanAlterColumn(dbType) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s 不能直接修改列 %s（SQLite 需要重建表），该改动被跳过", engineName(dbType), name))
			continue
		}
		from := ddl.Column{Name: before.Name, Type: before.Type, Nullable: before.Nullable, DefaultValue: before.DefaultValue}
		to := ddl.Column{Name: name, Type: want.Type, Nullable: want.Nullable, DefaultValue: want.DefaultValue}
		res.Statements = append(res.Statements, ddl.AlterColumn(dbType, schema, req.Table, from, to)...)
	}
	// Drops: catalog columns the form no longer lists.
	for _, c := range current {
		if !kept[strings.ToLower(c.Name)] {
			res.Statements = append(res.Statements, ddl.DropColumn(dbType, schema, req.Table, c.Name))
		}
	}
	if req.Indexes != nil {
		if err := a.buildIndexEdits(&res, conn, req, schema, dbType); err != nil {
			return res, err
		}
	}
	if req.ForeignKeys != nil {
		if err := a.buildForeignKeyEdits(&res, conn, req, schema, dbType); err != nil {
			return res, err
		}
	}
	return res, nil
}

// buildIndexEdits diffs the desired index list against the catalog. A primary
// key is never touched: it is a table constraint, not an index the editor owns,
// and dropping it would silently un-key the table. A changed index comes out as
// DROP + CREATE — no engine edits an index in place.
func (a *App) buildIndexEdits(res *StructureEditResult, conn *Connection, req StructureEditRequest, schema, dbType string) error {
	current, err := a.tableIndexes(conn, req.Database, schema, req.Table)
	if err != nil {
		return err
	}
	currentByName := make(map[string]IndexInfo, len(current))
	for _, idx := range current {
		currentByName[strings.ToLower(idx.Name)] = idx
	}
	kept := map[string]bool{}
	for i, want := range req.Indexes {
		name := strings.TrimSpace(want.Name)
		if name == "" {
			return fmt.Errorf("第 %d 行的索引名为空", i+1)
		}
		if len(want.Columns) == 0 {
			return fmt.Errorf("索引 %s 没有列", name)
		}
		key := strings.ToLower(name)
		if kept[key] {
			return fmt.Errorf("索引 %s 重复出现", name)
		}
		kept[key] = true
		before, exists := currentByName[key]
		if exists && before.Primary {
			// A primary key stays exactly as it is; the editor cannot see it
			// as a secondary index.
			continue
		}
		spec := ddl.Index{Name: name, Unique: want.Unique, Columns: want.Columns}
		if !exists {
			res.Statements = append(res.Statements, ddl.CreateIndex(dbType, schema, req.Table, spec))
			continue
		}
		if sameIndexSpec(before, spec) {
			continue
		}
		res.Statements = append(res.Statements,
			ddl.DropIndex(dbType, schema, req.Table, before.Name),
			ddl.CreateIndex(dbType, schema, req.Table, spec))
	}
	for _, idx := range current {
		// SQLite's autoindexes back UNIQUE/PK constraints the editor has no
		// say in; they cannot be dropped and are not the user's to remove.
		if idx.Primary || strings.HasPrefix(idx.Name, "sqlite_autoindex_") || kept[strings.ToLower(idx.Name)] {
			continue
		}
		res.Statements = append(res.Statements, ddl.DropIndex(dbType, schema, req.Table, idx.Name))
	}
	return nil
}

// sameIndexSpec compares an edited index with what the catalog reports. Only
// uniqueness and the key columns are comparable across engines: expression
// parts, methods and predicates are display-only on this form, and the editor
// re-creates an index the moment it drops those, so a round-trip that keeps the
// name, unique flag and plain columns must stay a no-op.
func sameIndexSpec(info IndexInfo, spec ddl.Index) bool {
	if info.Unique != spec.Unique || len(info.Columns) != len(spec.Columns) {
		return false
	}
	for i, c := range spec.Columns {
		if !strings.EqualFold(strings.TrimSpace(c), strings.TrimSpace(info.Columns[i])) {
			return false
		}
	}
	return true
}

// buildForeignKeyEdits diffs the desired foreign keys against the catalog.
// SQLite supports neither ADD CONSTRAINT nor DROP CONSTRAINT, so any change
// there is reported as a warning rather than rendered as a statement it would
// reject.
func (a *App) buildForeignKeyEdits(res *StructureEditResult, conn *Connection, req StructureEditRequest, schema, dbType string) error {
	current, err := a.tableForeignKeys(conn, req.Database, schema, req.Table)
	if err != nil {
		return err
	}
	currentByName := make(map[string]ForeignKeyInfo, len(current))
	for _, fk := range current {
		currentByName[strings.ToLower(fk.Name)] = fk
	}
	kept := map[string]bool{}
	for i, want := range req.ForeignKeys {
		name := strings.TrimSpace(want.Name)
		if name == "" {
			return fmt.Errorf("第 %d 行的外键名为空", i+1)
		}
		if len(want.Columns) == 0 || strings.TrimSpace(want.RefTable) == "" || len(want.RefColumns) == 0 {
			return fmt.Errorf("外键 %s 缺少列或引用目标", name)
		}
		if len(want.RefColumns) != len(want.Columns) {
			return fmt.Errorf("外键 %s 的列数与引用列数不一致", name)
		}
		key := strings.ToLower(name)
		if kept[key] {
			return fmt.Errorf("外键 %s 重复出现", name)
		}
		kept[key] = true
		spec := ddl.ForeignKey{
			Name: name, Columns: want.Columns, RefSchema: want.RefSchema,
			RefTable: want.RefTable, RefColumns: want.RefColumns,
			OnDelete: dbcore.NormalizeFKAction(want.OnDelete),
			OnUpdate: dbcore.NormalizeFKAction(want.OnUpdate),
		}
		before, exists := currentByName[key]
		if exists && sameForeignKeySpec(before, spec) {
			continue
		}
		if !ddl.CanAddForeignKey(dbType) || (exists && !ddl.CanDropForeignKey(dbType)) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s 不能在已有表上修改外键 %s（SQLite 需要重建表），该改动被跳过", engineName(dbType), name))
			continue
		}
		if exists {
			res.Statements = append(res.Statements, ddl.DropForeignKey(dbType, schema, req.Table, before.Name))
		}
		res.Statements = append(res.Statements, ddl.AddForeignKey(dbType, schema, req.Table, spec))
	}
	for _, fk := range current {
		if kept[strings.ToLower(fk.Name)] {
			continue
		}
		if !ddl.CanDropForeignKey(dbType) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s 不能删除外键 %s（SQLite 需要重建表），该改动被跳过", engineName(dbType), fk.Name))
			continue
		}
		res.Statements = append(res.Statements, ddl.DropForeignKey(dbType, schema, req.Table, fk.Name))
	}
	return nil
}

// sameForeignKeySpec compares the edited key with the catalog's. The actions
// compare as normalized standard spellings, so the round-trip of an untouched
// key is a no-op even where engines report "NO_ACTION" or "SET NULL".
func sameForeignKeySpec(info ForeignKeyInfo, spec ddl.ForeignKey) bool {
	return strings.EqualFold(strings.TrimSpace(info.RefTable), strings.TrimSpace(spec.RefTable)) &&
		columnListsEqual(info.Columns, spec.Columns) &&
		columnListsEqual(info.RefColumns, spec.RefColumns) &&
		dbcore.NormalizeFKAction(info.OnDelete) == spec.OnDelete &&
		dbcore.NormalizeFKAction(info.OnUpdate) == spec.OnUpdate
}

func columnListsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(strings.TrimSpace(a[i]), strings.TrimSpace(b[i])) {
			return false
		}
	}
	return true
}

// PreviewStructureEdit renders the statements without executing.
func (a *App) PreviewStructureEdit(req StructureEditRequest) (StructureEditResult, error) {
	return a.buildStructureEdits(req)
}

// ApplyStructureEdit executes the rendered statements in one transaction, each
// through the Safe Mode guard the same way a typed DDL runs.
func (a *App) ApplyStructureEdit(req StructureEditRequest) (StructureEditResult, error) {
	res, err := a.buildStructureEdits(req)
	if err != nil {
		return res, err
	}
	if len(res.Statements) == 0 {
		return res, nil
	}
	mode := a.GetSettings().SafeMode
	for _, statement := range res.Statements {
		if guard := guardWrite(mode, statement); guard.Blocked {
			res.Warnings = append(res.Warnings, guard.Reason)
			return res, fmt.Errorf("%s", guard.Reason)
		}
	}
	conn, err := a.connection(req.ConnID)
	if err != nil {
		return res, err
	}
	drv, err := a.execDriver(conn, req.Database)
	if err != nil {
		return res, err
	}
	txDrv, ok := drv.(dbcore.SQLTxDriver)
	if !ok {
		return res, fmt.Errorf("unsupported type: %s", conn.Type)
	}
	ctx, done := a.beginRun(req.ConnID)
	defer done()
	tx, err := txDrv.Begin(ctx, dbcore.TxMode{})
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	for i, statement := range res.Statements {
		if _, err := txDrv.ExecuteIn(tx, ctx, statement); err != nil {
			return res, fmt.Errorf("语句 %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	// The DDL lands in the history as the statements the user reviewed.
	joined := strings.Join(res.Statements, ";\n")
	a.recordStatement(req.ConnID, req.Database, joined, dbcoreQueryResult(len(res.Statements)))
	return res, nil
}

// dbcoreQueryResult packages a statement count into the history entry shape.
func dbcoreQueryResult(statements int) QueryResult {
	return QueryResult{AffectedRows: int64(statements)}
}

// engineName renders the user-facing engine label in warnings.
func engineName(dbType string) string {
	switch dbType {
	case "sqlite":
		return "SQLite"
	case "mysql":
		return "MySQL"
	case "postgresql":
		return "PostgreSQL"
	case "sqlserver":
		return "SQL Server"
	case "oracle":
		return "Oracle"
	}
	return dbType
}

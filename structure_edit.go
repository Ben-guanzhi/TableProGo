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

// StructureEditRequest is the API payload: the full desired column list.
type StructureEditRequest struct {
	ConnID   string       `json:"connId"`
	Database string       `json:"database"`
	Schema   string       `json:"schema"`
	Table    string       `json:"table"`
	Columns  []ColumnEdit `json:"columns"`
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
	return res, nil
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

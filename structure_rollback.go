package main

// 结构编辑回滚（B）：撤销一次已经应用的结构编辑。和正向编辑一样，反向语句
// 全部由内核 ddl 包渲染；引擎表达不了的反向（例如 SQLite 不能在已有表上加/删
// 外键）给出明确的中文警告，而不是一句会被引擎拒绝的假语句。
//
// 反向渲染需要编辑前的目录快照：请求里带上 Before（列/索引/外键三份清单），
// 服务端把它当作"编辑前"、把 StructureEditRequest 当作"编辑后"，逐节做反向
// diff。客户端在打开结构编辑器时本来就握着这份快照，回传它是零成本的。

import (
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/ddl"
)

// StructureEditSnapshot 是编辑前的目录快照，回滚据此渲染反向语句。
type StructureEditSnapshot struct {
	Columns     []ColumnEdit     `json:"columns"`
	Indexes     []IndexEdit      `json:"indexes,omitempty"`
	ForeignKeys []ForeignKeyEdit `json:"foreignKeys,omitempty"`
}

// StructureRollbackRequest 是 /api/structure-edit/rollback 的请求体：与结构
// 编辑相同的 StructureEditRequest（即被撤销的那次编辑所请求的目标状态），
// 加上编辑前的快照 Before；Statements 是那次编辑实际执行的语句清单，仅用于
// 留痕，不影响反向渲染。
type StructureRollbackRequest struct {
	StructureEditRequest
	Before     StructureEditSnapshot `json:"before"`
	Statements []string              `json:"statements,omitempty"`
}

// PreviewStructureRollback 只渲染反向语句，不执行。
func (a *App) PreviewStructureRollback(req StructureRollbackRequest) (StructureEditResult, error) {
	return a.buildStructureRollback(req)
}

// ApplyStructureRollback 执行反向语句，和正向编辑一样逐条过 Safe Mode 守卫。
func (a *App) ApplyStructureRollback(req StructureRollbackRequest) (StructureEditResult, error) {
	return a.applyStructureRollback(req, false)
}

// ApplyStructureRollbackConfirmed 是用户确认 Safe Mode 暂停之后的执行入口。
func (a *App) ApplyStructureRollbackConfirmed(req StructureRollbackRequest) (StructureEditResult, error) {
	return a.applyStructureRollback(req, true)
}

func (a *App) applyStructureRollback(req StructureRollbackRequest, force bool) (StructureEditResult, error) {
	res, err := a.buildStructureRollback(req)
	if err != nil {
		return res, err
	}
	res.Applied = req.Statements
	statements, warnings, err := a.executeStructureStatements(req.ConnID, req.Database, res.Statements, res.Warnings, force)
	res.Statements = statements
	res.Warnings = warnings
	// The undo is a write too: it lands in the history like the forward edit,
	// so the audit trail shows both sides of every applied edit.
	if err == nil && len(statements) > 0 {
		joined := strings.Join(statements, ";\n")
		a.recordStatement(req.ConnID, req.Database, joined, dbcoreQueryResult(len(statements)))
	}
	return res, err
}

// buildStructureRollback 按编辑前快照与编辑后请求逐节渲染反向语句。
func (a *App) buildStructureRollback(req StructureRollbackRequest) (StructureEditResult, error) {
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
	if isMySQLLike(dbType) && schema == "" {
		schema = req.Database
	}

	statements, err := renderColumnRollback(dbType, schema, req.Table, req.Columns, req.Before.Columns, &res)
	if err != nil {
		return res, err
	}
	res.Statements = append(res.Statements, statements...)

	// Indexes and foreign keys are only diffed when the applied edit carried
	// them: the request's nil list means "this editor did not touch them".
	if req.Indexes != nil {
		statements, err := renderIndexRollback(dbType, schema, req.Table, req.Indexes, req.Before.Indexes, &res)
		if err != nil {
			return res, err
		}
		res.Statements = append(res.Statements, statements...)
	}
	if req.ForeignKeys != nil {
		statements, err := renderForeignKeyRollback(dbType, schema, req.Table, req.ForeignKeys, req.Before.ForeignKeys, &res)
		if err != nil {
			return res, err
		}
		res.Statements = append(res.Statements, statements...)
	}
	return res, nil
}

// renderColumnRollback renders the statements that undo a column edit:
// a column the edit added comes back as DROP; a column it dropped is re-added
// with its original definition; a column it changed is restored through
// AlterColumn; a rename (which the editor renders as drop+add) comes back as
// drop-new + add-old.
func renderColumnRollback(dbType, schema, table string, applied, before []ColumnEdit, res *StructureEditResult) ([]string, error) {
	var out []string
	beforeByName := make(map[string]ColumnEdit, len(before))
	for _, c := range before {
		beforeByName[strings.ToLower(strings.TrimSpace(c.Name))] = c
	}
	appliedByName := map[string]bool{}
	for _, c := range applied {
		key := strings.ToLower(strings.TrimSpace(c.Name))
		if appliedByName[key] {
			continue
		}
		appliedByName[key] = true
		prev, ok := beforeByName[key]
		if !ok {
			out = append(out, ddl.DropColumn(dbType, schema, table, c.Name))
			continue
		}
		if sameColumnEdit(prev, c) {
			continue
		}
		if !ddl.CanAlterColumn(dbType) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s 不能原地修改列 %s（需重建表），该列的回滚被跳过", engineName(dbType), c.Name))
			continue
		}
		statements, gap := ddl.AlterColumn(dbType, schema, table, columnSpec(c), columnSpec(prev))
		if gap != "" {
			res.Warnings = append(res.Warnings, gap)
		}
		out = append(out, statements...)
	}
	for _, c := range before {
		if appliedByName[strings.ToLower(strings.TrimSpace(c.Name))] {
			continue
		}
		out = append(out, ddl.AddColumn(dbType, schema, table, columnSpec(c)))
	}
	return out, nil
}

// renderIndexRollback renders the reverse of an index edit. A changed index was
// DROP+CREATE in the forward direction, so its reverse is the same pair with the
// before-spec re-created; an added index comes back as DROP; a dropped index is
// re-created from its snapshot.
func renderIndexRollback(dbType, schema, table string, applied, before []IndexEdit, res *StructureEditResult) ([]string, error) {
	var out []string
	beforeByName := make(map[string]IndexEdit, len(before))
	for _, idx := range before {
		beforeByName[strings.ToLower(strings.TrimSpace(idx.Name))] = idx
	}
	appliedByName := map[string]bool{}
	for _, idx := range applied {
		name := strings.TrimSpace(idx.Name)
		key := strings.ToLower(name)
		if appliedByName[key] {
			continue
		}
		appliedByName[key] = true
		prev, ok := beforeByName[key]
		if !ok {
			out = append(out, ddl.DropIndex(dbType, schema, table, name))
			continue
		}
		if sameIndexEdit(prev, idx) {
			continue
		}
		out = append(out,
			ddl.DropIndex(dbType, schema, table, name),
			ddl.CreateIndex(dbType, schema, table, indexSpec(prev)))
	}
	for _, idx := range before {
		if appliedByName[strings.ToLower(strings.TrimSpace(idx.Name))] {
			continue
		}
		out = append(out, ddl.CreateIndex(dbType, schema, table, indexSpec(idx)))
	}
	return out, nil
}

// renderForeignKeyRollback renders the reverse of a foreign key edit. A key the
// edit added comes back as DROP; a key it dropped is re-added from its snapshot;
// a changed key is reversed as DROP(new)+ADD(old). SQLite cannot express either
// direction on an existing table, so those reversals become warnings.
func renderForeignKeyRollback(dbType, schema, table string, applied, before []ForeignKeyEdit, res *StructureEditResult) ([]string, error) {
	var out []string
	beforeByName := make(map[string]ForeignKeyEdit, len(before))
	for _, fk := range before {
		beforeByName[strings.ToLower(strings.TrimSpace(fk.Name))] = fk
	}
	appliedByName := map[string]bool{}
	for _, fk := range applied {
		name := strings.TrimSpace(fk.Name)
		key := strings.ToLower(name)
		if appliedByName[key] {
			continue
		}
		appliedByName[key] = true
		prev, ok := beforeByName[key]
		if !ok {
			if !ddl.CanDropForeignKey(dbType) {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s 不能删除外键 %s（需重建表），该外键的回滚被跳过", engineName(dbType), name))
				continue
			}
			out = append(out, ddl.DropForeignKey(dbType, schema, table, name))
			continue
		}
		if sameForeignKeyEdit(prev, fk) {
			continue
		}
		if !ddl.CanDropForeignKey(dbType) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s 不能删除外键 %s（需重建表），该外键的回滚被跳过", engineName(dbType), name))
			continue
		}
		spec, err := fkSpecFromEdit(dbType, prev)
		if err != nil {
			return out, err
		}
		if !ddl.CanAddForeignKey(dbType) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s 不能在已有表上添加外键 %s（需重建表），该外键的回滚被跳过", engineName(dbType), name))
			continue
		}
		out = append(out,
			ddl.DropForeignKey(dbType, schema, table, name),
			ddl.AddForeignKey(dbType, schema, table, spec))
	}
	for _, fk := range before {
		if appliedByName[strings.ToLower(strings.TrimSpace(fk.Name))] {
			continue
		}
		spec, err := fkSpecFromEdit(dbType, fk)
		if err != nil {
			return out, err
		}
		if !ddl.CanAddForeignKey(dbType) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s 不能在已有表上添加外键 %s（需重建表），该外键的回滚被跳过", engineName(dbType), fk.Name))
			continue
		}
		out = append(out, ddl.AddForeignKey(dbType, schema, table, spec))
	}
	return out, nil
}

// fkSpecFromEdit validates and projects one ForeignKeyEdit onto ddl.ForeignKey.
// The before-snapshot text is client-supplied, so it goes through the same
// identifier and action validation as an add.
func fkSpecFromEdit(dbType string, edit ForeignKeyEdit) (ddl.ForeignKey, error) {
	return erFKDefinition(dbType, ERFKEditRequest{
		Name:       edit.Name,
		Columns:    edit.Columns,
		RefSchema:  edit.RefSchema,
		RefTable:   edit.RefTable,
		RefColumns: edit.RefColumns,
		OnDelete:   edit.OnDelete,
		OnUpdate:   edit.OnUpdate,
	})
}

func columnSpec(c ColumnEdit) ddl.Column {
	return ddl.Column{
		Name:         strings.TrimSpace(c.Name),
		Type:         strings.TrimSpace(c.Type),
		Nullable:     c.Nullable,
		DefaultValue: c.DefaultValue,
	}
}

func indexSpec(idx IndexEdit) ddl.Index {
	return ddl.Index{
		Name:    strings.TrimSpace(idx.Name),
		Unique:  idx.Unique,
		Columns: trimStrings(idx.Columns),
	}
}

func sameColumnEdit(a, b ColumnEdit) bool {
	return strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(b.Name)) &&
		strings.EqualFold(strings.TrimSpace(a.Type), strings.TrimSpace(b.Type)) &&
		a.Nullable == b.Nullable &&
		strings.TrimSpace(a.DefaultValue) == strings.TrimSpace(b.DefaultValue)
}

func sameIndexEdit(a, b IndexEdit) bool {
	if a.Unique != b.Unique || len(a.Columns) != len(b.Columns) {
		return false
	}
	for i := range a.Columns {
		if !strings.EqualFold(strings.TrimSpace(a.Columns[i]), strings.TrimSpace(b.Columns[i])) {
			return false
		}
	}
	return true
}

func sameForeignKeyEdit(a, b ForeignKeyEdit) bool {
	return strings.EqualFold(strings.TrimSpace(a.RefTable), strings.TrimSpace(b.RefTable)) &&
		columnListsEqual(a.Columns, b.Columns) &&
		columnListsEqual(a.RefColumns, b.RefColumns) &&
		dbcore.NormalizeFKAction(a.OnDelete) == dbcore.NormalizeFKAction(b.OnDelete) &&
		dbcore.NormalizeFKAction(a.OnUpdate) == dbcore.NormalizeFKAction(b.OnUpdate)
}

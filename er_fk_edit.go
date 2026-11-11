package main

// ERD 外键可视编辑（A）：在关系图上给一张表加/删一条外键，或回滚上一次
// 已应用的加/删。语句全部由内核 ddl 包渲染——客户端不判断方言，引擎能不能
// 表达由 CanAddForeignKey / CanDropForeignKey 回答，表达不了就给出明确的
// 中文错误而不是一句会被引擎拒绝的假语句。

import (
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/ddl"
)

// ERFKEditRequest 是 /api/er/fk 的请求体：一次对关系图里某张表外键的编辑。
// Action 取 "add"（新增）、"drop"（删除）或 "rollback"（撤销上一次已应用的
// 编辑）；Preview 为真时只渲染语句、不执行。
type ERFKEditRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Action   string `json:"action"`
	Preview  bool   `json:"preview"`

	// add（以及回滚一次 drop）用的外键定义。
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete"`
	OnUpdate   string   `json:"onUpdate"`

	// drop（以及回滚一次 add）用的外键名。
	FKName string `json:"fkName"`

	// rollback 专用：这次撤销的是哪一种已应用的操作。留空时按当前目录推断
	// ——目录里还存在该键就只能是 add 的结果，否则按 drop 的重新增处理。
	RollbackOf string `json:"rollbackOf,omitempty"`
}

// ERFKEditResult 是一次外键编辑的结果：将要（或已经）执行的语句、引擎级
// 警告，以及撤销这次编辑所需的语句列表。
type ERFKEditResult struct {
	Statements []string `json:"statements"`
	Warnings   []string `json:"warnings"`
	Rollback   []string `json:"rollback"`
}

// PreviewERFKEdit 渲染这次外键编辑的语句与回滚语句，不执行。
func (a *App) PreviewERFKEdit(req ERFKEditRequest) (ERFKEditResult, error) {
	return a.buildERFKEdit(req)
}

// ApplyERFKEdit 执行这次外键编辑，和结构编辑一样逐条过 Safe Mode 守卫，
// 全部语句放在一个事务里。
func (a *App) ApplyERFKEdit(req ERFKEditRequest) (ERFKEditResult, error) {
	return a.applyERFKEdit(req, false)
}

// ApplyERFKEditConfirmed 是用户确认 Safe Mode 暂停之后的执行入口，和
// ApplyStructureEditConfirmed 一样让"跳过确认"在调用方显式可见。
func (a *App) ApplyERFKEditConfirmed(req ERFKEditRequest) (ERFKEditResult, error) {
	return a.applyERFKEdit(req, true)
}

func (a *App) applyERFKEdit(req ERFKEditRequest, force bool) (ERFKEditResult, error) {
	res, err := a.buildERFKEdit(req)
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
		} else if guard.NeedsConfirmation && !force {
			res.Warnings = append(res.Warnings, guard.Reason)
			return res, needsConfirmationError{reason: guard.Reason}
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
	a.recordStatement(req.ConnID, req.Database, strings.Join(res.Statements, ";\n"), dbcoreQueryResult(len(res.Statements)))
	return res, nil
}

// buildERFKEdit 读取当前目录，再交给与引擎无关的渲染函数。
func (a *App) buildERFKEdit(req ERFKEditRequest) (ERFKEditResult, error) {
	empty := ERFKEditResult{Warnings: []string{}, Rollback: []string{}}
	if req.Table == "" {
		return empty, fmt.Errorf("没有目标表")
	}
	conn, err := a.connection(req.ConnID)
	if err != nil {
		return empty, err
	}
	if conn.Type == "redis" {
		return empty, fmt.Errorf("ER 图不支持 Redis 连接")
	}
	dbType := conn.Type
	schema := defaultSchema(dbType, req.Schema)
	if isMySQLLike(dbType) && schema == "" {
		schema = req.Database
	}
	current, err := a.tableForeignKeys(conn, req.Database, schema, req.Table)
	if err != nil {
		return empty, err
	}
	return renderERFKEdit(dbType, schema, req, current)
}

// renderERFKEdit 是纯函数：给定引擎、schema、请求和当前目录，产出语句与
// 回滚语句。不碰连接池，便于按引擎逐条断言。
func renderERFKEdit(dbType, schema string, req ERFKEditRequest, current []ForeignKeyInfo) (ERFKEditResult, error) {
	res := ERFKEditResult{Warnings: []string{}, Rollback: []string{}}
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "add":
		spec, err := erFKDefinition(dbType, req)
		if err != nil {
			return res, err
		}
		if !ddl.CanAddForeignKey(dbType) {
			return res, fmt.Errorf("%s 不能在已有表上添加外键（需要重建表）", engineName(dbType))
		}
		res.Statements = append(res.Statements, ddl.AddForeignKey(dbType, schema, req.Table, spec))
		res.Rollback = append(res.Rollback, ddl.DropForeignKey(dbType, schema, req.Table, spec.Name))
	case "drop":
		name := strings.TrimSpace(req.FKName)
		if name == "" {
			name = strings.TrimSpace(req.Name)
		}
		if name == "" {
			return res, fmt.Errorf("删除外键需要提供外键名")
		}
		if _, err := ddl.ValidatedIdent(dbType, name); err != nil {
			return res, fmt.Errorf("外键名不合法：%w", err)
		}
		original, ok := findForeignKey(current, name)
		if !ok {
			return res, fmt.Errorf("表 %s 上找不到外键 %s", req.Table, name)
		}
		if !ddl.CanDropForeignKey(dbType) {
			return res, fmt.Errorf("%s 不能删除外键 %s（需要重建表）", engineName(dbType), name)
		}
		res.Statements = append(res.Statements, ddl.DropForeignKey(dbType, schema, req.Table, original.Name))
		res.Rollback = append(res.Rollback, ddl.AddForeignKey(dbType, schema, req.Table, foreignKeySpec(original)))
	case "rollback":
		if err := renderERFKRollback(dbType, schema, req, current, &res); err != nil {
			return res, err
		}
	default:
		return res, fmt.Errorf("未知的外键操作 %q（应为 add / drop / rollback）", req.Action)
	}
	return res, nil
}

// renderERFKRollback 渲染撤销一次已应用外键编辑的语句。方向由 RollbackOf
// 给出，缺省时按当前目录推断。
func renderERFKRollback(dbType, schema string, req ERFKEditRequest, current []ForeignKeyInfo, res *ERFKEditResult) error {
	name := strings.TrimSpace(req.FKName)
	if name == "" {
		name = strings.TrimSpace(req.Name)
	}
	if name == "" {
		return fmt.Errorf("回滚外键需要提供外键名")
	}
	undo := strings.ToLower(strings.TrimSpace(req.RollbackOf))
	if undo == "" {
		// A key still in the catalog can only be the product of an add; anything
		// else is the re-add of a key that was dropped.
		if _, ok := findForeignKey(current, name); ok {
			undo = "add"
		} else {
			undo = "drop"
		}
	}
	switch undo {
	case "add":
		existing, ok := findForeignKey(current, name)
		if !ok {
			return fmt.Errorf("表 %s 上找不到外键 %s，无法回滚这次新增", req.Table, name)
		}
		if !ddl.CanDropForeignKey(dbType) {
			return fmt.Errorf("%s 不能删除外键 %s（需要重建表）", engineName(dbType), name)
		}
		res.Statements = append(res.Statements, ddl.DropForeignKey(dbType, schema, req.Table, existing.Name))
		// Re-adding needs the definition only the caller has: render it when the
		// rollback request carries a complete one, so the undo can itself be undone.
		if spec, err := erFKDefinition(dbType, req); err == nil && spec.RefTable != "" && len(spec.Columns) > 0 {
			res.Rollback = append(res.Rollback, ddl.AddForeignKey(dbType, schema, req.Table, spec))
		}
	case "drop":
		spec, err := erFKDefinition(dbType, req)
		if err != nil {
			return err
		}
		if !ddl.CanAddForeignKey(dbType) {
			return fmt.Errorf("%s 不能在已有表上添加外键（需要重建表）", engineName(dbType))
		}
		res.Statements = append(res.Statements, ddl.AddForeignKey(dbType, schema, req.Table, spec))
		res.Rollback = append(res.Rollback, ddl.DropForeignKey(dbType, schema, req.Table, spec.Name))
	default:
		return fmt.Errorf("未知的回滚方向 %q（应为 add / drop）", req.RollbackOf)
	}
	return nil
}

// erFKDefinition 把请求里的外键定义整理成 ddl.ForeignKey，逐项做标识符与
// 动作校验——这些文本会原样进入会被执行的 ALTER 语句。
func erFKDefinition(dbType string, req ERFKEditRequest) (ddl.ForeignKey, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.FKName)
	}
	if name == "" {
		return ddl.ForeignKey{}, fmt.Errorf("外键名为空")
	}
	if _, err := ddl.ValidatedIdent(dbType, name); err != nil {
		return ddl.ForeignKey{}, fmt.Errorf("外键名不合法：%w", err)
	}
	columns := trimStrings(req.Columns)
	refColumns := trimStrings(req.RefColumns)
	if len(columns) == 0 {
		return ddl.ForeignKey{}, fmt.Errorf("外键 %s 没有本表列", name)
	}
	if len(columns) != len(refColumns) {
		return ddl.ForeignKey{}, fmt.Errorf("外键 %s 的本表列数与引用列数不一致", name)
	}
	for _, c := range columns {
		if _, err := ddl.ValidatedIdent(dbType, c); err != nil {
			return ddl.ForeignKey{}, fmt.Errorf("列名 %q 不合法：%w", c, err)
		}
	}
	refTable := strings.TrimSpace(req.RefTable)
	if refTable == "" {
		return ddl.ForeignKey{}, fmt.Errorf("外键 %s 没有引用表", name)
	}
	if _, err := ddl.ValidatedIdent(dbType, refTable); err != nil {
		return ddl.ForeignKey{}, fmt.Errorf("引用表名不合法：%w", err)
	}
	for _, c := range refColumns {
		if _, err := ddl.ValidatedIdent(dbType, c); err != nil {
			return ddl.ForeignKey{}, fmt.Errorf("引用列名 %q 不合法：%w", c, err)
		}
	}
	refSchema := strings.TrimSpace(req.RefSchema)
	if refSchema != "" {
		if _, err := ddl.ValidatedIdent(dbType, refSchema); err != nil {
			return ddl.ForeignKey{}, fmt.Errorf("引用 schema 不合法：%w", err)
		}
	}
	onDelete, err := erFKAction("ON DELETE", dbcore.NormalizeFKAction(req.OnDelete))
	if err != nil {
		return ddl.ForeignKey{}, err
	}
	onUpdate, err := erFKAction("ON UPDATE", dbcore.NormalizeFKAction(req.OnUpdate))
	if err != nil {
		return ddl.ForeignKey{}, err
	}
	return ddl.ForeignKey{
		Name:       name,
		Columns:    columns,
		RefSchema:  refSchema,
		RefTable:   refTable,
		RefColumns: refColumns,
		OnDelete:   onDelete,
		OnUpdate:   onUpdate,
	}, nil
}

// erFKAction 校验一个外键动作；空值表示引擎没有声明，是合法输入。
func erFKAction(label, action string) (string, error) {
	if action == "" {
		return "", nil
	}
	switch action {
	case dbcore.FKActionNoAction, dbcore.FKActionCascade, dbcore.FKActionSetNull,
		dbcore.FKActionSetDefault, dbcore.FKActionRestrict:
		return action, nil
	}
	return "", fmt.Errorf("%s 动作 %q 不是标准外键动作", label, action)
}

// findForeignKey 按名字（大小写不敏感，Oracle 会折叠大小写）在目录里找外键。
func findForeignKey(current []ForeignKeyInfo, name string) (ForeignKeyInfo, bool) {
	for _, fk := range current {
		if strings.EqualFold(strings.TrimSpace(fk.Name), strings.TrimSpace(name)) {
			return fk, true
		}
	}
	return ForeignKeyInfo{}, false
}

// foreignKeySpec 把目录里的外键投影成 ddl.ForeignKey，动作按标准拼写归一化。
func foreignKeySpec(info ForeignKeyInfo) ddl.ForeignKey {
	return ddl.ForeignKey{
		Name:       info.Name,
		Columns:    info.Columns,
		RefSchema:  info.RefSchema,
		RefTable:   info.RefTable,
		RefColumns: info.RefColumns,
		OnDelete:   dbcore.NormalizeFKAction(info.OnDelete),
		OnUpdate:   dbcore.NormalizeFKAction(info.OnUpdate),
	}
}

// trimStrings 去掉每个元素的首尾空白并丢掉空元素。
func trimStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	return out
}

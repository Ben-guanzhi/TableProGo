package main

// 保存结果到表（DBeaver 的 "Export to table"）：把当前网格的结果集写入
// 一张目标表。结果集由前端传回（只含已载入的行——结果被行数上限截断时
// 前端会提示，§283 的截断信号在这里再次消费）。
//
// 事务边界（§284 的铁律）：CREATE + 全部 INSERT 在**一个事务**里——要么
// 表和行都进，要么什么都不留。分批只为参数化复用，不拆事务。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/ddl"
	"github.com/Ben-guanzhi/dbcore/sqltext"
)

// resultCell 是前端传回的一个单元格：文本值 + NULL 掩码（空串与 NULL
// 在 JSON 里无法区分，掩码是 §235 的类型层面区分）。
type resultCell struct {
	Value string `json:"v"`
	Null  bool   `json:"n,omitempty"`
}

// resultColumn 是结果的一列：名字与推断的类型（前端按 cellText 渲染过，
// 类型只有粗分类——全部按文本入库，由目标列的类型决定转型）。
type resultColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// saveResultRequest 是保存结果到表的请求体。
type saveResultRequest struct {
	ConnID   string       `json:"connId"`
	Database string       `json:"database"`
	Table    string       `json:"table"`
	Create   bool         `json:"create"` // true = 先 CREATE TABLE（列类型按文本）
	Columns  []resultColumn `json:"columns"`
	Rows     [][]resultCell `json:"rows"`
}

// handleSaveResultToTable 把结果集写入目标表。
func (a *App) handleSaveResultToTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var body saveResultRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if body.Table == "" || len(body.Columns) == 0 {
		writeError(w, 400, "目标表名与列清单都要有")
		return
	}
	conn, err := a.liveConnection(body.ConnID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	drv, err := a.sqlDriver(conn, body.Database)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbType := string(drv.Kind())

	// 列名经内核校验（换行注入向量，与结构编辑器同一闸门）。
	names := make([]string, len(body.Columns))
	for i, c := range body.Columns {
		if _, err := ddl.ValidatedIdent(dbType, c.Name); err != nil {
			writeError(w, 400, fmt.Sprintf("第 %d 列名不合法：%v", i+1, err))
			return
		}
		names[i] = c.Name
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	// 事务写入走内核 SQLTxDriver.ExecuteIn（exec 传 *sql.Tx，args 是内核
	// Value——§254 的乐观锁不适用于无 PK 的结果集，这里用裸事务 + 参数化
	// INSERT，与 rowsync 的插入同模式）。
	txDrv, ok := drv.(dbcore.SQLTxDriver)
	if !ok {
		writeError(w, http.StatusInternalServerError, "该驱动不支持事务写入")
		return
	}
	tx, err := txDrv.Begin(ctx, dbcore.TxMode{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rollbackOnErr := func(code int, err error) {
		_ = tx.Rollback()
		writeError(w, code, err.Error())
	}

	if body.Create {
		cols := make([]ddl.Column, len(body.Columns))
		for i, c := range body.Columns {
			typ := c.Type
			if typ == "" {
				typ = "TEXT"
			}
			cols[i] = ddl.Column{Name: c.Name, Type: typ}
		}
		createStmt := ddl.CreateTable(dbType, "", body.Table, cols, nil, nil, nil)
		if _, err := txDrv.ExecuteIn(tx, ctx, createStmt); err != nil {
			rollbackOnErr(http.StatusInternalServerError, fmt.Errorf("建表失败: %w", err))
			return
		}
	}

	quoted := make([]string, len(names))
	marks := make([]string, len(names))
	for i, n := range names {
		q, err := ddl.ValidatedIdent(dbType, n)
		if err != nil {
			rollbackOnErr(400, err)
			return
		}
		quoted[i] = q
		marks[i] = sqltext.Placeholder(dbType, i+1)
	}
	quotedTable, err := sqltext.QualifiedName(dbType, body.Table)
	if err != nil {
		rollbackOnErr(400, err)
		return
	}
	stmt := "INSERT INTO " + quotedTable +
		" (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"

	inserted := int64(0)
	for _, row := range body.Rows {
		if len(row) != len(names) {
			continue // 不规则行跳过（前端按列渲染，理论到不了）
		}
		args := make([]dbcore.Value, len(row))
		for i, cell := range row {
			if cell.Null {
				args[i] = dbcore.Value{Kind: dbcore.ValNull}
			} else {
				args[i] = dbcore.Value{Kind: dbcore.ValText, S: cell.Value}
			}
		}
		if _, err := txDrv.ExecuteIn(tx, ctx, stmt, args...); err != nil {
			rollbackOnErr(http.StatusInternalServerError, fmt.Errorf("第 %d 行插入失败: %w", inserted+1, err))
			return
		}
		inserted++
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"rows": inserted, "table": body.Table})
}

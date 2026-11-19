package main

// 数据生成器 API（DBeaver Mock Data / Navicat Data Generator）：内核
// dbcore.GenerateInserts（v0.8.0）直驱——按表列类型生成测试 INSERT。
//
// 生成是纯逻辑（确定性：同 seed 同值），客户端只负责展示与让用户确认后
// 运行——与重命名/清空表/保存到表同一纪律：**内核生成文本，用户过目，
// 执行走既有闸门**。

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"net/http"

	"github.com/Ben-guanzhi/dbcore"
)

// generateDataRequest 是生成请求体。
type generateDataRequest struct {
	ConnID         string `json:"connId"`
	Database       string `json:"database"`
	Schema         string `json:"schema"`
	Table          string `json:"table"`
	Rows           int    `json:"rows"`
	Seed           uint64 `json:"seed"`
	SkipPrimaryKey bool   `json:"skipPrimaryKey"`
}

// generateDataResponse 返回按行一条的 INSERT 语句（供编辑器逐条审阅）。
type generateDataResponse struct {
	Statements []string `json:"statements"`
	Rows       int      `json:"rows"`
	Seed       uint64   `json:"seed"`
}

func (a *App) handleGenerateData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var body generateDataRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if body.Table == "" {
		writeError(w, 400, "目标表名不能为空")
		return
	}
	cols, err := a.GetColumns(body.ConnID, body.Database, body.Schema, body.Table)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(cols) == 0 {
		writeError(w, 400, "表没有已知列——先在结构视图加载列类型")
		return
	}
	if body.Rows <= 0 {
		body.Rows = 10
	}
	if body.Rows > 10000 {
		body.Rows = 10000 // GUI 上限（§内核 MaxCellRunes 之外的行数上限）
	}
	seed := body.Seed
	if seed == 0 {
		// 0 = 内核会自取随机种子，但生成的 SQL 无法回显实际 seed——用户
		// 想复现这批值就必须知道 seed。这里显式取一个非零种子回显。
		var b [8]byte
		_, _ = rand.Read(b[:])
		seed = binary.LittleEndian.Uint64(b[:])
		if seed == 0 {
			seed = 1
		}
	}
	conn, err := a.liveConnection(body.ConnID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 本地 catalogmodel.ColumnInfo → 内核 dbcore.ColumnInfo（生成器只认
	// 名字/类型/主键标记——其余业务字段不参与造数）。
	kcols := make([]dbcore.ColumnInfo, len(cols))
	for i, c := range cols {
		kcols[i] = dbcore.ColumnInfo{
			Name: c.Name, Type: c.Type, Nullable: c.Nullable, IsPrimaryKey: c.IsPrimaryKey,
		}
	}
	stmts := dbcore.GenerateInserts(kcols, dbcore.GenOptions{
		Rows:           body.Rows,
		Seed:           seed,
		Dialect:        conn.Type,
		Table:          body.Table,
		SkipPrimaryKey: body.SkipPrimaryKey,
	})
	writeJSON(w, generateDataResponse{Statements: stmts, Rows: len(stmts), Seed: seed})
}

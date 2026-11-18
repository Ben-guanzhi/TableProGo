package main

// 数据字典导出（TableProGo 接入）：内核 ExportDictionary（dbcore v0.13.0）。
// 后端路由 /api/export-dictionary，前端用 post() 调用，返回 Markdown 文本
// 由前端用浏览器 Blob 下载或 Wails 保存对话框落盘。

import (
	"context"
	"strings"
	"time"

	dbcore "github.com/Ben-guanzhi/dbcore"
)

// dictionaryRequest 是导出请求的 body。
type dictionaryRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
}

// dictionaryResponse 返回 Markdown 文本与表数。
type dictionaryResponse struct {
	Doc    string `json:"doc"`
	Tables int    `json:"tables"`
}

// exportDictionary 把 connID 连接的库结构导出为 Markdown。
func (a *App) exportDictionary(req dictionaryRequest) (any, error) {
	conn, err := a.liveConnection(req.ConnID)
	if err != nil {
		return nil, err
	}
	drv, err := a.sqlDriver(conn, req.Database)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	doc, err := dbcore.ExportDictionary(ctx, drv, "", nil)
	if err != nil {
		return nil, err
	}
	return dictionaryResponse{
		Doc:    doc,
		Tables: strings.Count(doc, "\n## "),
	}, nil
}

package main

// 服务器变量查看（Navicat「变量」/ HeidiSQL Variables 面板的对齐）：
//
// 内核可选能力 ServerVariablesReader（dbcore v0.6.0）。走已有的 sqlDriver()
// 访问路径（含插件分支），断言能力即用；引擎不支持返回明确的说明性错误。
// **只读**——内核接口没有改值，API 不提供 SET。

import (
	"context"
	"time"

	"github.com/Ben-guanzhi/dbcore"
)

// ServerVar 是前端展示的一条变量。
type ServerVar struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Default string `json:"default,omitempty"`
	Range   string `json:"range,omitempty"`
	Desc    string `json:"desc,omitempty"`
}

// ListServerVariables 列出当前服务器的系统变量（15s 超时；内核已按名排序）。
func (a *App) ListServerVariables(connID, database string) ([]ServerVar, error) {
	conn, err := a.liveConnection(connID)
	if err != nil {
		return nil, err
	}
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return nil, err
	}
	svr, ok := dbcore.AsServerVariables(drv)
	if !ok {
		return nil, errSessionsUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	vars, err := svr.ServerVariables(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ServerVar, 0, len(vars))
	for _, v := range vars {
		out = append(out, ServerVar{
			Name:    v.Name,
			Value:   v.Value,
			Default: v.Default,
			Range:   v.Range,
			Desc:    v.Desc,
		})
	}
	return out, nil
}

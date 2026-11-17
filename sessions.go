package main

// 服务器会话（Navicat「会话管理」/ DBeaver Sessions 的对齐）：
//
// 内核可选能力 ProcessLister（dbcore v0.5.0）。TableProGo 已有 sqlDriver()
// 访问路径（含插件分支），断言能力即用；引擎不支持（SQLite/插件未实现）
// 返回明确的错误字符串，前端展示。

import (
	"context"
	"errors"
	"time"

	"github.com/Ben-guanzhi/dbcore"
)

// ProcSession 是前端展示的一行会话。
type ProcSession struct {
	ID      string `json:"id"`
	User    string `json:"user"`
	Client  string `json:"client"`
	DB      string `json:"db"`
	State   string `json:"state"`
	Command string `json:"command"`
	Query   string `json:"query"`
	Time    string `json:"time"`
}

// ListServerSessions 列出当前服务器的其他会话（10s 超时；不含当前连接）。
func (a *App) ListServerSessions(connID, database string) ([]ProcSession, error) {
	conn, err := a.liveConnection(connID)
	if err != nil {
		return nil, err
	}
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return nil, err
	}
	pl, ok := dbcore.AsProcessLister(drv)
	if !ok {
		return nil, errSessionsUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	procs, err := pl.Processes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProcSession, 0, len(procs))
	for _, p := range procs {
		out = append(out, ProcSession{
			ID:      p.ID,
			User:    p.User,
			Client:  p.Client,
			DB:      p.DB,
			State:   p.State,
			Command: p.Command,
			Query:   p.Query,
			Time:    p.Time,
		})
	}
	return out, nil
}

// KillServerSession 终止一个会话（内核做 ID 校验；10s 超时）。
func (a *App) KillServerSession(connID, database, id string) error {
	conn, err := a.liveConnection(connID)
	if err != nil {
		return err
	}
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return err
	}
	pl, ok := dbcore.AsProcessLister(drv)
	if !ok {
		return errSessionsUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return pl.KillProcess(ctx, id)
}

// errSessionsUnsupported 是引擎不支持的说明（前端直接展示）。
var errSessionsUnsupported = errors.New("当前引擎不支持会话管理（需要 MySQL / PostgreSQL / SQL Server）")

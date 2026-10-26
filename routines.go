package main

// 函数 / 过程：schema 级对象（不属于某张表），因此不进结构页卡片，而是从库/模式
// 右键菜单打开「函数与过程」对话框：列表（名称 + 种类）加定义查看。
//
// 能力来自内核 dbcore.ExtraMetadataReader（P3-4），SQL 配方在 sqltext、各引擎驱动
// 只执行与映射。SQLite 没有例程目录；插件驱动尚未实现该能力——两种情况都给出
// 明确的中文说明，而不是空列表冒充"读到了但没有"。

import (
	"errors"
	"fmt"

	"github.com/dbcore/dbcore"

	"tablepro-go/internal/catalogmodel"
)

func (a *App) extraMetadata(connID, database string) (dbcore.ExtraMetadataReader, *Connection, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, nil, err
	}
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return nil, nil, err
	}
	x, ok := dbcore.AsExtraMetadata(drv)
	if !ok {
		return nil, nil, fmt.Errorf("该连接不支持读取函数/过程（插件驱动尚未实现该能力）")
	}
	return x, conn, nil
}

// GetRoutines lists one schema's stored functions and procedures.
func (a *App) GetRoutines(connID, database, schema string) ([]catalogmodel.RoutineInfo, error) {
	x, conn, err := a.extraMetadata(connID, database)
	if err != nil {
		return nil, err
	}
	ctx, done := a.beginRun(connID)
	defer done()

	found, err := x.Routines(ctx, resolveObjectSchema(conn, database, schema))
	if errors.Is(err, dbcore.ErrNotSupported) {
		return nil, fmt.Errorf("%s 没有函数/过程目录", conn.Type)
	}
	if err != nil {
		return nil, err
	}
	out := make([]catalogmodel.RoutineInfo, 0, len(found))
	for _, r := range found {
		out = append(out, catalogmodel.RoutineInfo{Name: r.Name, Kind: r.Kind})
	}
	return out, nil
}

// GetRoutineDefinition returns one routine's CREATE DDL. PostgreSQL answers one
// definition per overload, joined by a blank line.
func (a *App) GetRoutineDefinition(connID, database, schema, name string) (string, error) {
	x, conn, err := a.extraMetadata(connID, database)
	if err != nil {
		return "", err
	}
	ctx, done := a.beginRun(connID)
	defer done()

	def, err := x.RoutineDefinition(ctx, resolveObjectSchema(conn, database, schema), name)
	if errors.Is(err, dbcore.ErrNotSupported) {
		return "", fmt.Errorf("%s 没有函数/过程目录", conn.Type)
	}
	if err != nil {
		return "", err
	}
	if def == "" {
		return "", fmt.Errorf("函数/过程 %s 不存在或没有可读定义", name)
	}
	return def, nil
}

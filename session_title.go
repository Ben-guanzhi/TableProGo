package main

// 窗口标题跟随会话（Navicat/TablePlus 惯例，波 94 界面对标）：标题反映活动
// 标签的「连接 @ 库」，切换连接/换库/关标签时即时更新。前端在会话状态变化的
// effect 里调一次；隐私模式下传空名（隐私模式连树上的名字都遮，标题不能漏）。

import (
	"fmt"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// SetSessionTitle 把窗口标题更新为「conn @ db — TablePro Go」。db 为空时只显示
// 连接名；conn 也为空时回到应用名。返回实际设置的标题（前端可以校验）。
func (a *App) SetSessionTitle(conn, db string) string {
	var title string
	switch {
	case strings.TrimSpace(conn) != "" && strings.TrimSpace(db) != "":
		title = fmt.Sprintf("%s @ %s — TablePro Go", conn, db)
	case strings.TrimSpace(conn) != "":
		title = fmt.Sprintf("%s — TablePro Go", conn)
	default:
		title = "TablePro Go"
	}
	if w, ok := application.Get().Window.GetByID(1); ok {
		w.SetTitle(title)
	}
	return title
}

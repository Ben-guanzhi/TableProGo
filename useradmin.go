package main

// 用户管理（Navicat「用户」/ DBeaver Users 视图）：内核 dbcore.UserAdmin
// 能力直驱——列出可登录用户、生成方言正确的创建/删除/授权 DDL。
//
// **执行仍走既有闸门**（内核注释的分工）：三个 DDL 生成接口不执行，前端
// 拿到语句后经 ExecuteQuery 运行——Safe Mode 的确认、只读锁、审计全部
// 保留。SQLite 无用户体系，能力断言失败即诚实报「该引擎不支持」。

import (
	"encoding/json"
	"net/http"

	"github.com/Ben-guanzhi/dbcore"
)

// userListResponse 是用户列表的 API 形态。
type userListResponse struct {
	Users    []string `json:"users"`
	Hosts    []string `json:"hosts,omitempty"` // MySQL 的 user@host 二元组
	EngineOK bool     `json:"engineOk"`
	Error    string   `json:"error,omitempty"`
}

// handleUserList 列出当前连接的可登录用户。
func (a *App) handleUserList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var body struct {
		ConnID   string `json:"connId"`
		Database string `json:"database"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	conn, err := a.liveConnection(body.ConnID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	drv, err := a.sqlDriver(conn, body.Database)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ua, ok := dbcore.AsUserAdmin(drv)
	if !ok {
		writeJSON(w, userListResponse{EngineOK: false, Error: "当前引擎不支持用户管理（SQLite 无用户体系；插件引擎未实现该能力）"})
		return
	}
	users, err := ua.ListUsers(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	resp := userListResponse{EngineOK: true, Users: make([]string, 0, len(users)), Hosts: make([]string, 0, len(users))}
	for _, u := range users {
		resp.Users = append(resp.Users, u.Name)
		resp.Hosts = append(resp.Hosts, u.Host)
	}
	writeJSON(w, resp)
}

// userDDLRequest 是生成用户 DDL 的请求。
type userDDLRequest struct {
	ConnID    string `json:"connId"`
	Database  string `json:"database"`
	Action    string `json:"action"` // create / drop / grant
	User      string `json:"user"`
	Host      string `json:"host,omitempty"`
	Password  string `json:"password,omitempty"`
	Database2 string `json:"database2,omitempty"` // grant 的目标库
}

// handleUserDDL 生成方言正确的用户 DDL（不执行——前端确认后走 ExecuteQuery）。
func (a *App) handleUserDDL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var body userDDLRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	conn, err := a.liveConnection(body.ConnID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	drv, err := a.sqlDriver(conn, body.Database)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ua, ok := dbcore.AsUserAdmin(drv)
	if !ok {
		writeError(w, 400, "当前引擎不支持用户管理")
		return
	}
	var stmt string
	switch body.Action {
	case "create":
		stmt = ua.CreateUserDDL(body.User, body.Host, body.Password)
	case "drop":
		stmt = ua.DropUserDDL(body.User, body.Host)
	case "grant":
		stmt = ua.GrantDatabaseDDL(body.User, body.Host, body.Database2)
	default:
		writeError(w, 400, "未知动作: "+body.Action)
		return
	}
	writeJSON(w, map[string]string{"sql": stmt})
}

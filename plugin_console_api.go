package main

// W17: the console-action API.
//
// This surface is deliberately domain-blind. It lists the actions a plugin-backed
// connection declares and runs one by id, passing the caller's args straight through.
// The binary has no idea whether "vector-search" means a Milvus similarity query, a
// Kafka consume or a Nacos publish — that knowledge lives in the plugin. Which is the
// whole point: a new engine needs no change here.
//
// Two endpoints, both hand-written (not the post() helper) because a console action can
// take a while — a consume with a timeout, a search over a large collection — and the
// plugin call should be cancelled when the browser goes away, which needs r.Context().

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

// pluginConsoleRequest names the connection whose actions are wanted.
type pluginConsoleRequest struct {
	ConnID string `json:"connId"`
}

// pluginRunActionRequest runs one declared action.
type pluginRunActionRequest struct {
	ConnID string         `json:"connId"`
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
}

// pluginConsoleFor resolves the live ConnDriver and its conn id for a plugin-backed
// connection. It refuses a built-in connection explicitly: a native driver has no
// console, and silently returning an empty list would look like "this engine offers
// none" instead of "you asked the wrong thing".
func (a *App) pluginConsoleFor(connID string) (*plugindriver.ConnDriver, string, error) {
	conn, ok := a.connections[connID]
	if !ok {
		return nil, "", fmt.Errorf("未知连接 %s", connID)
	}
	if !pluginConnection(conn) {
		return nil, "", fmt.Errorf("%w: 连接 %q 不是插件连接", dbcore.ErrNotSupported, conn.Name)
	}
	// Starting through pluginDriver (rather than reading the handle directly) also
	// gives us the dead-process reopen: a console opened after the child crashed gets
	// a fresh process instead of a stale handle that errors forever.
	if _, err := a.pluginDriver(conn); err != nil {
		return nil, "", err
	}
	conn.pluginMu.Lock()
	drv, id := conn.pluginConn, conn.pluginConnID
	conn.pluginMu.Unlock()
	if drv == nil || id == "" {
		return nil, "", fmt.Errorf("插件连接 %s 尚未启动", conn.Name)
	}
	return drv, id, nil
}

// handlePluginActions lists the console actions a connection offers.
func (a *App) handlePluginActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body pluginConsoleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	drv, id, err := a.pluginConsoleFor(body.ConnID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	actions, err := drv.ListActions(r.Context(), id)
	if err != nil {
		// A plugin that predates the console protocol answers not-supported. That is
		// "this engine has no console", not a failure: an ordinary SQL plugin must not
		// produce an error toast just for being loaded.
		if errors.Is(err, dbcore.ErrNotSupported) {
			writeJSON(w, []plugindriver.ConnAction{})
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if actions == nil {
		actions = []plugindriver.ConnAction{}
	}
	writeJSON(w, actions)
}

// handlePluginRunAction runs one declared action and returns the result in the same
// shape as a query, so the existing grid renders it with no special case.
func (a *App) handlePluginRunAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body pluginRunActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Action == "" {
		writeError(w, http.StatusBadRequest, "action 不能为空")
		return
	}
	drv, id, err := a.pluginConsoleFor(body.ConnID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := drv.RunAction(r.Context(), id, body.Action, body.Args)
	if err != nil {
		// The plugin's own code and message are the useful part; friendlyDBError is
		// for engine errors, and a console action's failure is not one.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reuse the query→grid conversion so a console action's cells decode exactly like a
	// statement's: blobs as text, large ints as digit text, decimals as exact text.
	writeJSON(w, queryResultFrom(res, nil))
}
package main

// W-T2: the workbench HTTP face. The UI reads the workbench state (tabs, channels,
// Safe Mode policy), drives single channels, and broadcasts. Every entry is
// nil-safe: the workbench is an optional capability, and "not initialized" is a
// normal answer, not a panic.
//
// Safe Mode discipline (AD-3): /broadcast reads the interception key and either
// EXECUTES or reports {withheld: true}; /broadcast-confirmed executes regardless.
// The confirmation is a separate endpoint, never a request flag — the same rule
// the SQL editor's Safe Mode uses ("跳过确认不能是请求 flag"). Emission honesty:
// once executed, the input is idΓ — the response says so, the UI does not offer
// an undo.

import (
	"encoding/json"
	"fmt"
	"net/http"

	"tablepro-go/internal/workbenchhost"
)

// workbenchReady resolves the runner or reports the capability is unavailable.
func (a *App) workbenchReady(w http.ResponseWriter) *workbenchhost.Runner {
	if a.workbench == nil {
		writeError(w, http.StatusServiceUnavailable, "工作台未初始化")
		return nil
	}
	return a.workbench
}

// workbenchStateRequest names the state query (empty body is fine; the shape
// keeps the frontend's single post() helper uniform).
type workbenchStateRequest struct{}

// handleWorkbenchState serves the tabs/channels/policy snapshot the terminal
// panel renders. A dead plugin's bindings are already gone from σ by the time
// this runs, so the UI sees the post-reclaim surface without special cases.
func (a *App) handleWorkbenchState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	writeJSON(w, runner.State())
}

// workbenchChannelRequest drives ONE channel: open / data / resize / close.
// Open carries the connection target (host/port/user/password — the AD-4
// stdin-lend path is a recorded follow-up; see navop-ssh's package comment);
// data carries keystrokes and returns the shell's output.
type workbenchChannelRequest struct {
	Action    string `json:"action"`
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	User      string `json:"user,omitempty"`
	Password  string `json:"password,omitempty"`
	ChannelID string `json:"channelId,omitempty"`
	Input     string `json:"input,omitempty"`
	MaxBytes  int    `json:"maxBytes,omitempty"`
	Cols      uint16 `json:"cols,omitempty"`
	Rows      uint16 `json:"rows,omitempty"`
}

func (a *App) handleWorkbenchChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchChannelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "open":
		id, err := runner.OpenChannel(map[string]any{
			"host": body.Host, "port": body.Port, "user": body.User,
			"password": body.Password, "cols": body.Cols, "rows": body.Rows,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"channelId": id})
	case "data":
		out, ended, err := runner.WriteChannel(body.ChannelID, body.Input, body.MaxBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"channelId": body.ChannelID, "output": out, "ended": ended})
	case "resize":
		if err := runner.ResizeChannel(body.ChannelID, body.Cols, body.Rows); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "close":
		if err := runner.CloseChannel(body.ChannelID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	default:
		writeError(w, http.StatusBadRequest, "unknown channel action "+body.Action)
	}
}

// workbenchBroadcastRequest is one broadcast: the SAME input to N channels.
// This is the W-T2 danger case (one command, many machines), which is why it is
// the only workbench entry with a withhold/confirm pair.
type workbenchBroadcastRequest struct {
	ChannelIDs []string `json:"channelIds"`
	Input      string   `json:"input"`
	MaxBytes   int      `json:"maxBytes,omitempty"`
}

// handleWorkbenchBroadcast reads the safe.mode interception key: ON → withhold
// (the UI shows the confirmation bar); OFF → execute immediately. The withheld
// response carries NO results — nothing ran, which is what withholding means.
func (a *App) handleWorkbenchBroadcast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchBroadcastRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.ChannelIDs) == 0 {
		writeError(w, http.StatusBadRequest, "channelIds 不能为空")
		return
	}
	if runner.WithholdBroadcast() {
		writeJSON(w, map[string]any{
			"withheld":   true,
			"channelIds": body.ChannelIDs,
			"input":      body.Input,
			// The emission warning is the UI's job; the response only states the
			// fact the host cannot revert it.
			"warning": "广播会把同一输入发送到 N 个终端会话，发送后不可撤回",
		})
		return
	}
	writeJSON(w, runner.Broadcast(body.ChannelIDs, body.Input, body.MaxBytes))
}

// handleWorkbenchBroadcastConfirmed EXECUTES the broadcast. It exists so the
// confirmation is a separate route (the flag cannot bypass the prompt); being on
// this route IS the confirmation.
func (a *App) handleWorkbenchBroadcastConfirmed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchBroadcastRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.ChannelIDs) == 0 {
		writeError(w, http.StatusBadRequest, "channelIds 不能为空")
		return
	}
	writeJSON(w, runner.Broadcast(body.ChannelIDs, body.Input, body.MaxBytes))
}

// SetWorkbenchSafeMode flips the safe.mode INTERCEPTION key (Σinter): a derived
// realization, so this changes the policy WITHOUT reloading any fiber — the
// fibers' surfaces are untouched (test-pinned in ctxcore).
func (a *App) SetWorkbenchSafeMode(on bool) error {
	if a.workbench == nil {
		return fmt.Errorf("工作台未初始化")
	}
	a.workbench.SetSafeMode(on)
	return nil
}

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

// ---------- W-T3: recording + shortcuts ----------

// workbenchRecordRequest starts/stops one channel's cast. Start returns the
// .cast path (asciinema v3); stop writes the exit event and renames the temp
// file into place.
type workbenchRecordRequest struct {
	Action    string `json:"action"` // "start" | "stop"
	ChannelID string `json:"channelId"`
	Title     string `json:"title,omitempty"`
}

func (a *App) handleWorkbenchRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchRecordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "start":
		path, err := runner.StartRecording(body.ChannelID, body.Title)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"path": path})
	case "stop":
		path, err := runner.StopRecording(body.ChannelID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"path": path})
	default:
		writeError(w, http.StatusBadRequest, "unknown record action "+body.Action)
	}
}

// workbenchShortcutRequest is the shortcut CRUD + run surface. Run reuses the
// broadcast gate (the withhold decision lives in ONE place); its response shape
// is BroadcastResult's, so the UI's confirm flow is identical.
type workbenchShortcutRequest struct {
	Action     string   `json:"action"` // "list" | "save" | "delete" | "run"
	Name       string   `json:"name,omitempty"`
	Command    string   `json:"command,omitempty"`
	Body       string   `json:"body,omitempty"`
	ChannelIDs []string `json:"channelIds,omitempty"`
	Confirmed  bool     `json:"confirmed,omitempty"`
	MaxBytes   int      `json:"maxBytes,omitempty"`
}

func (a *App) handleWorkbenchShortcut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body workbenchShortcutRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "list":
		items, err := workbenchhost.ListShortcuts()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, items)
	case "save":
		if err := workbenchhost.SaveShortcut(workbenchhost.Shortcut{
			Name: body.Name, Command: body.Command, Description: body.Body,
		}); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "delete":
		if err := workbenchhost.DeleteShortcut(body.Name); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "run":
		runner := a.workbenchReady(w)
		if runner == nil {
			return
		}
		if len(body.ChannelIDs) == 0 {
			writeError(w, http.StatusBadRequest, "channelIds 不能为空")
			return
		}
		res, err := runner.RunShortcut(body.Name, body.ChannelIDs, body.Confirmed, body.MaxBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if res.Withheld {
			// Same shape as the broadcast withhold: the UI shows the confirm bar
			// and re-posts with confirmed=true. The emission warning travels with
			// it — a shortcut IS a broadcast under a name.
			res.Withheld = true
			writeJSON(w, map[string]any{
				"withheld":   true,
				"channelIds": body.ChannelIDs,
				"input":      "快捷指令 " + body.Name,
				"warning":    "快捷指令会把同一命令发送到 N 个终端会话，发送后不可撤回",
			})
			return
		}
		writeJSON(w, res)
	default:
		writeError(w, http.StatusBadRequest, "unknown shortcut action "+body.Action)
	}
}

// ---------- W-F1: files (SFTP over the plugin's files.session) ----------

// workbenchFilesRequest is the file surface: open a session (with its realm
// roots), then list/mkdir/rename/remove/upload/download against it. Upload is
// TWO calls (stage → commit) because the host tracks each step's inverse — the
// acquisition discipline; download renames locally after the plugin's stream.
type workbenchFilesRequest struct {
	Action    string   `json:"action"`
	Session   string   `json:"session,omitempty"`
	Host      string   `json:"host,omitempty"`
	Port      int      `json:"port,omitempty"`
	User      string   `json:"user,omitempty"`
	Password  string   `json:"password,omitempty"`
	Roots     []string `json:"roots,omitempty"`
	Path      string   `json:"path,omitempty"`
	To        string   `json:"to,omitempty"`
	LocalPath string   `json:"localPath,omitempty"`
}

func (a *App) handleWorkbenchFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchFilesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "open":
		id, err := runner.OpenFilesSession(body.Host, body.Port, body.User, body.Password, body.Roots)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"sessionId": id})
	case "list":
		entries, err := runner.ListFiles(body.Session, body.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"entries": entries})
	case "mkdir":
		if err := runner.MkdirFiles(body.Session, body.Path); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "rename":
		if err := runner.RenameFiles(body.Session, body.Path, body.To); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "remove":
		if err := runner.RemoveFiles(body.Session, body.Path); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "upload":
		// The local file was already staged by the UI into body.LocalPath (a
		// browser upload lands in a temp file the host owns); the runner does
		// the remote .tmp + commit with both inverses tracked.
		remote, size, err := runner.UploadFile(body.Session, body.LocalPath, body.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"path": remote, "size": size})
	case "download":
		size, err := runner.DownloadFile(body.Session, body.Path, body.LocalPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"path": body.LocalPath, "size": size})
	case "close":
		if err := runner.CloseFilesSession(body.Session); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	default:
		writeError(w, http.StatusBadRequest, "unknown files action "+body.Action)
	}
}

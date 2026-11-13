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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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
// The password field is the USER's input at this boundary; the handler lends it
// (AD-4) instead of forwarding it — the open params carry only the lend NAME.
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
		// AD-4: the password is lent (raw stdin body) and the params name the
		// lend. A password field here is the USER's input at the HTTP boundary;
		// it never becomes a plugin parameter.
		id, err := runner.OpenChannelWithSecret(map[string]any{
			"host": body.Host, "port": body.Port, "user": body.User,
			"cols": body.Cols, "rows": body.Rows,
		}, "ssh.password", []byte(body.Password))
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
		id, err := runner.OpenFilesSession(body.Host, body.Port, body.User, body.Roots,
			"ssh.password", []byte(body.Password))
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

// ---------- W-M1: monitor (host.stats sampling ring) ----------

// workbenchMonitorRequest drives the sampling surface: start/stop a host's
// sampler, read the rings, or submit a sample the PLUGIN took (the host stores
// what the plugin reports — it never invents numbers).
type workbenchMonitorRequest struct {
	Action string  `json:"action"` // "state" | "start" | "stop" | "submit"
	Host   string  `json:"host,omitempty"`
	Cap    int     `json:"cap,omitempty"`
	Value  float64 `json:"value,omitempty"`
	Time   string  `json:"time,omitempty"`
}

func (a *App) handleWorkbenchMonitor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchMonitorRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "state":
		writeJSON(w, runner.MonitorStates())
	case "start":
		tag, err := runner.StartMonitor(body.Host, body.Cap)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"tag": tag})
	case "stop":
		if err := runner.StopMonitor(body.Host); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "submit":
		at := time.Now()
		if body.Time != "" {
			parsed, err := time.Parse(time.RFC3339, body.Time)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad time: "+err.Error())
				return
			}
			at = parsed
		}
		if err := runner.SubmitSample(body.Host, body.Value, at); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	default:
		writeError(w, http.StatusBadRequest, "unknown monitor action "+body.Action)
	}
}

// ---------- W-C1: containers (navop-containers over the ssh tunnel) ----------

// containersReady resolves the CONSUMER runner (navop-containers) through the
// orchestrator. Two plugins run over one shared store: the provider (navop-ssh)
// serves the terminal/files panels, the consumer serves containers and rides the
// provider's tunnel.proxy. Without the orchestrator the capability is simply
// absent — a normal answer, not a panic.
func (a *App) containersReady(w http.ResponseWriter) *workbenchhost.Runner {
	if a.workbenchOrch == nil {
		writeError(w, http.StatusServiceUnavailable,
			"容器工作台未启用（需要同时加载 navop-ssh 与 navop-containers）")
		return nil
	}
	return a.workbenchOrch.Other()
}

// workbenchContainersRequest is the container surface: open a session (through
// the tunnel), list containers, run kubectl on the remote, or drive a container
// shell (exec reuses the terminal channel contract).
type workbenchContainersRequest struct {
	Action     string `json:"action"` // open|ps|kubectl|exec|exec-data|exec-close|close
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	DockerAddr string `json:"dockerAddr,omitempty"`
	Session    string `json:"session,omitempty"`
	Container  string `json:"container,omitempty"`
	ChannelID  string `json:"channelId,omitempty"`
	Data       string `json:"data,omitempty"`
	Args       string `json:"args,omitempty"`
}

func (a *App) handleWorkbenchContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	consumer := a.containersReady(w)
	if consumer == nil {
		return
	}
	// The provider's fiber must be the one that answers the proxy request; the
	// orchestrator owns that routing (navop-ssh records the chain coordinates).
	var body workbenchContainersRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "open":
		// OpenContainerSession resolves tunnel.proxy through the provider, then
		// asks the consumer to dial the daemon INSIDE that chain.
		id, err := consumer.OpenContainerSession(body.Host, body.Port, body.DockerAddr,
			"ssh.password", []byte(body.Password))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"sessionId": id})
	case "ps":
		rows, err := consumer.ContainerPS(body.Session)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"containers": rows})
	case "kubectl":
		out, err := consumer.ContainerKubectl(body.Session, body.Args)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"output": out})
	case "exec":
		id, err := consumer.ContainerExec(body.Session, body.Container)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"channelId": id})
	case "exec-data":
		out, ended, err := consumer.ContainerExecData(body.ChannelID, body.Data)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"output": out, "ended": ended})
	case "exec-close":
		if err := consumer.ContainerExecClose(body.ChannelID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "close":
		if err := consumer.ContainerCloseSession(body.Session); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	default:
		writeError(w, http.StatusBadRequest, "unknown containers action "+body.Action)
	}
}

// ---------- W-Q1: MQTT (publish = emission, subscribe = acquisition) ----------

// workbenchMQTTRequest drives the MQTT console. publish is GATED: it answers
// {withheld:true} under Safe Mode and the confirm is publish-confirmed, a separate
// action — never a flag on the gated path.
type workbenchMQTTRequest struct {
	Action   string `json:"action"` // open|subscribe|unsubscribe|publish|publish-confirmed|messages|close|subscriptions
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Session  string `json:"session,omitempty"`
	Topic    string `json:"topic,omitempty"`
	Payload  string `json:"payload,omitempty"`
	Since    int    `json:"since,omitempty"`
}

func (a *App) handleWorkbenchMQTT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchMQTTRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "open":
		// AD-4: the password is lent; an empty one opens anonymously.
		id, err := runner.OpenMQTTSession(body.Host, body.Port, body.User,
			"mqtt.password", []byte(body.Password))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"sessionId": id})
	case "subscribe":
		// The ACQUISITION: a reversible row at mqtt.subscriptions.
		if err := runner.SubscribeTopic(body.Session, body.Topic); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "subscriptions": runner.MQTTSubscriptions()})
	case "unsubscribe":
		if err := runner.UnsubscribeTopic(body.Session, body.Topic); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "subscriptions": runner.MQTTSubscriptions()})
	case "subscriptions":
		writeJSON(w, map[string]any{"subscriptions": runner.MQTTSubscriptions()})
	case "publish":
		res, err := runner.PublishTopic(body.Session, body.Topic, body.Payload)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, res)
	case "publish-confirmed":
		// The separate confirm path: the operator confirmed THIS emission.
		res, err := runner.PublishConfirmed(body.Session, body.Topic, body.Payload)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, res)
	case "messages":
		rows, next, total, err := runner.MQTTMessages(body.Session, body.Since)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"messages": rows, "next": next, "total": total})
	case "close":
		if err := runner.CloseMQTTSession(body.Session); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	default:
		writeError(w, http.StatusBadRequest, "unknown MQTT action "+body.Action)
	}
}

// ---------- W-D1: desktop (RDP = emission + file compensation, VNC = channel) ----------

// workbenchDesktopRequest drives the remote-desktop surface. The RDP start is an
// EMISSION: the response says so and offers no undo; the .rdp file is the tracked
// compensation. VNC rides the channel with base64 payloads.
type workbenchDesktopRequest struct {
	Action  string `json:"action"` // rdp|vnc|relay|close|state
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	User    string `json:"user,omitempty"`
	Session string `json:"session,omitempty"`
	Data    string `json:"data,omitempty"` // base64 for relay
}

func (a *App) handleWorkbenchDesktop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchDesktopRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "rdp":
		res, err := runner.StartRDP(body.Host, body.Port, body.User)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, res)
	case "vnc":
		res, err := runner.OpenVNCSession(body.Host, body.Port)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, res)
	case "relay":
		payload, err := base64.StdEncoding.DecodeString(body.Data)
		if err != nil {
			writeError(w, http.StatusBadRequest, "data 必须是 base64: "+err.Error())
			return
		}
		reply, ended, err := runner.VNCRelay(body.Session, payload)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"data":  base64.StdEncoding.EncodeToString(reply),
			"ended": ended,
			"bytes": len(reply),
		})
	case "close":
		if err := runner.CloseDesktopSession(body.Session); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	case "state":
		writeJSON(w, map[string]any{"sessions": runner.DesktopSessions()})
	default:
		writeError(w, http.StatusBadRequest, "unknown desktop action "+body.Action)
	}
}

// ---------- W-A1/A2: AI provider, host-privileged context, forced preview ----------

// workbenchAIRequest drives the assistant. The nl2sql action takes NO execute
// field on purpose: the response is a PREVIEW and running it is the editor's job
// through the normal confirm path. A request that carries `execute` is REFUSED
// rather than ignored, so a client cannot believe it ran something.
type workbenchAIRequest struct {
	Action   string                    `json:"action"` // open|chat|nl2sql|providers
	BaseURL  string                    `json:"baseUrl,omitempty"`
	Model    string                    `json:"model,omitempty"`
	APIKey   string                    `json:"apiKey,omitempty"`
	Session  string                    `json:"session,omitempty"`
	Messages []workbenchhost.AIMessage `json:"messages,omitempty"`
	Question string                    `json:"question,omitempty"`
	Dialect  string                    `json:"dialect,omitempty"`
	Context  workbenchhost.AIContext   `json:"context,omitempty"`
	// Execute is accepted only to be REFUSED with a message; see the handler.
	Execute bool `json:"execute,omitempty"`
}

func (a *App) handleWorkbenchAI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body workbenchAIRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "open":
		// AD-4: the API key is lent; a local gateway needs none.
		p, err := runner.OpenAIProvider(body.BaseURL, body.Model, "ai.apiKey", []byte(body.APIKey))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, p)
	case "providers":
		writeJSON(w, map[string]any{"providers": runner.AIProviders()})
	case "chat":
		text, err := runner.AIChat(body.Session, body.Messages, body.Context)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"text": text})
	case "nl2sql":
		if body.Execute {
			// The refusal is the feature: there is no execute path from this
			// endpoint, and saying so is better than silently downgrading.
			writeError(w, http.StatusBadRequest,
				"NL→SQL 只返回预览，不执行：请把生成的 SQL 放进编辑器，再走确认路径执行")
			return
		}
		res, err := runner.AINL2SQL(body.Session, body.Question, body.Dialect, body.Context)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, res)
	default:
		writeError(w, http.StatusBadRequest, "unknown AI action "+body.Action)
	}
}

// handleWorkbenchMCPTools lists the aggregate mcp.tools table and answers the
// permission gate for one call. The gate is what a caller must consult before
// invoking a tool: withheld means "re-post through the confirmed path".
func (a *App) handleWorkbenchMCPTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runner := a.workbenchReady(w)
	if runner == nil {
		return
	}
	var body struct {
		Action    string `json:"action"` // list|gate
		Name      string `json:"name,omitempty"`
		Confirmed bool   `json:"confirmed,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "list":
		writeJSON(w, map[string]any{"tools": runner.MCPTools()})
	case "gate":
		level, allowed, withheld, err := runner.ToolGate(body.Name, body.Confirmed)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"level": string(level), "allowed": allowed, "withheld": withheld,
		})
	default:
		writeError(w, http.StatusBadRequest, "unknown mcptools action "+body.Action)
	}
}

// ---------- W-N1: notes (host-local documents) ----------
// workbenchNotesRequest is the note CRUD surface. Notes are host-local: no plugin,
// no key surface beyond the nav node that announces the tab.
type workbenchNotesRequest struct {
	Action string `json:"action"` // list|save|delete
	ID     string `json:"id,omitempty"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
}

func (a *App) handleWorkbenchNotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body workbenchNotesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Action {
	case "list":
		notes, err := workbenchhost.ListNotes()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, notes)
	case "save":
		n, err := workbenchhost.SaveNote(workbenchhost.Note{
			ID: body.ID, Title: body.Title, Body: body.Body,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, n)
	case "delete":
		if err := workbenchhost.DeleteNote(body.ID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, statusOK)
	default:
		writeError(w, http.StatusBadRequest, "unknown notes action "+body.Action)
	}
}

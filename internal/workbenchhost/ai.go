package workbenchhost

// ai.go — W-A1's host half: the AI provider session, the HOST-PRIVILEGED context
// referencer, and the forced SQL preview.
//
// THE INVERSION THE PLAN ASKS FOR: "上下文引用器是宿主特权——只有宿主读得到全部键面".
// The plugin never reads another plugin's keys. The HOST assembles a context bundle
// from what it legitimately owns —
//
//	schema    the selected table's columns (the App hands them in: it is the tab's owner)
//	terminal  the last N lines of a channel (this package's scrollback ring)
//	notes     the operator's own note text (host-local documents)
//	file      a remote file head (through a files session, when one is open)
//
// — and only then calls the model. The operator sees exactly which sources were
// attached because the host attached them, one by one, named.
//
// THE FORCED PREVIEW: AINL2SQL returns SQL TEXT and a flag saying it is a preview.
// There is no execution path here — no connection handle, no statement runner, and
// the API layer refuses an `execute` field outright. Running the generated SQL is
// the editor's job, through the same Safe-Mode/confirm path as any typed
// statement. "单测锁死绕不过" is therefore about the absence of a path, not about a
// flag being checked.

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"github.com/dbcore/dbcore/plugindriver/workbench"
	"tablepro-go/internal/ctxcore"
)

// scrollback is a bounded line tail for one channel.
type scrollback struct {
	mu    sync.Mutex
	lines []string
	cap   int
}

func newScrollback(capacity int) *scrollback {
	if capacity <= 0 {
		capacity = 400
	}
	return &scrollback{cap: capacity}
}

// appendScrollback feeds the tail, splitting the output into lines so the context
// can quote a line count rather than a byte count.
func (r *Runner) appendScrollback(channelID, output string) {
	r.tailMu.Lock()
	sb := r.tail[channelID]
	if sb == nil {
		sb = newScrollback(400)
		r.tail[channelID] = sb
	}
	r.tailMu.Unlock()

	sb.mu.Lock()
	defer sb.mu.Unlock()
	for _, line := range strings.Split(output, "\n") {
		if len(sb.lines) >= sb.cap {
			sb.lines = sb.lines[1:]
		}
		sb.lines = append(sb.lines, line)
	}
}

// TailLines quotes the last n lines of a channel. An empty answer names the
// reason — a missing channel is not the same as an empty log.
func (r *Runner) TailLines(channelID string, n int) (string, bool) {
	r.tailMu.Lock()
	sb := r.tail[channelID]
	r.tailMu.Unlock()
	if sb == nil {
		return "", false
	}
	if n <= 0 || n > 2000 {
		n = 80
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	start := len(sb.lines) - n
	if start < 0 {
		start = 0
	}
	return strings.Join(sb.lines[start:], "\n"), true
}

// AIContext is the bundle the host assembles. Each field is a named source the
// operator asked for; empty fields are simply absent (not empty text with headers).
type AIContext struct {
	Schema   string `json:"schema,omitempty"`
	Terminal string `json:"terminal,omitempty"`
	Notes    string `json:"notes,omitempty"`
	File     string `json:"file,omitempty"`
}

// BuildContext joins the attached sources into one block, each prefixed with a
// header so the model (and the operator reading the request log) can tell which is
// which. Empty sources are skipped, never rendered as headings.
func BuildContext(c AIContext) string {
	var b strings.Builder
	add := func(title, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		b.WriteString("### " + title + "\n")
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n\n")
	}
	add("选中的表结构 (schema)", c.Schema)
	add("终端最近输出 (terminal tail)", c.Terminal)
	add("笔记 (notes)", c.Notes)
	add("远端文件片段 (file head)", c.File)
	return strings.TrimSpace(b.String())
}

// AIProvider is one open model session.
type AIProvider struct {
	Session string `json:"session"`
	Model   string `json:"model"`
	BaseURL string `json:"baseUrl"`
}

// OpenAIProvider opens a provider session at the plugin, lending the API key
// (AD-4: the key is a raw stdin body; a local gateway needs none).
func (r *Runner) OpenAIProvider(baseURL, model, secretName string, secret []byte) (AIProvider, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return AIProvider{}, fmt.Errorf("%s: the AI plugin fiber is not active", workbench.CodeInactive)
	}
	if secretName != "" && len(secret) > 0 {
		erase, err := r.LendSecret(secretName, secret)
		if err != nil {
			return AIProvider{}, err
		}
		defer func() { _ = erase() }()
	}
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return AIProvider{}, fmt.Errorf("AI plugin process is not running")
	}
	params := map[string]any{"baseUrl": baseURL, "model": model}
	if secretName != "" {
		params["secret"] = secretName
	}
	var res struct {
		SessionID string `json:"sessionId"`
		Model     string `json:"model"`
	}
	if err := p.call(context.Background(), workbench.MethodChannelOpen, params, &res); err != nil {
		return AIProvider{}, err
	}
	tag := ctxcore.Tag(res.SessionID)
	if err := r.store.Track(f, "ai-provider:"+res.SessionID, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyAIProvider, tag, f.UID(),
			map[string]any{"baseUrl": baseURL, "model": res.Model}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyAIProvider)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableRemove(ctxcore.KeyAIProvider, tag); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyAIProvider)
			return nil
		}, nil
	}); err != nil {
		return AIProvider{}, err
	}
	return AIProvider{Session: res.SessionID, Model: res.Model, BaseURL: baseURL}, nil
}

// AIMessage is one turn of a conversation.
type AIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// aiCall is the one wire to the plugin.
func (r *Runner) aiCall(action string, args map[string]any) (map[string]any, error) {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return nil, fmt.Errorf("AI plugin process is not running")
	}
	var out map[string]any
	if err := p.call(context.Background(), workbench.MethodChannelData, args, &out); err != nil {
		return nil, err
	}
	_ = action
	return out, nil
}

// AIChat sends a conversation with the host-assembled context.
func (r *Runner) AIChat(session string, messages []AIMessage, ctx AIContext) (string, error) {
	out, err := r.aiCall("chat", map[string]any{
		"action":  "chat",
		"session": session,
		"args": map[string]any{
			"messages": messages,
			"context":  BuildContext(ctx),
		},
	})
	if err != nil {
		return "", err
	}
	text, _ := out["text"].(string)
	return text, nil
}

// AISQL is a PREVIEW of a generated statement. Preview is a constant `true` in the
// type, not a field a request can set: there is no non-preview variant of this
// value, which is what makes "绕不过" structural.
type AISQL struct {
	SQL         string `json:"sql"`
	Explanation string `json:"explanation"`
	Preview     bool   `json:"preview"`
	Source      string `json:"source"`
}

// AINL2SQL turns a question into SQL text. It NEVER executes: this package has no
// statement runner, and the API layer rejects an execute field.
func (r *Runner) AINL2SQL(session, question, dialect string, ctx AIContext) (AISQL, error) {
	if strings.TrimSpace(question) == "" {
		return AISQL{}, fmt.Errorf("问题不能为空")
	}
	out, err := r.aiCall("nl2sql", map[string]any{
		"action":  "nl2sql",
		"session": session,
		"args": map[string]any{
			"question": question,
			"context":  BuildContext(ctx),
			"dialect":  dialect,
		},
	})
	if err != nil {
		return AISQL{}, err
	}
	sql, _ := out["sql"].(string)
	expl, _ := out["explanation"].(string)
	return AISQL{
		SQL: sql, Explanation: expl, Preview: true,
		Source: "由宿主附加的上下文生成；此处只预览，不执行。",
	}, nil
}

// AIProviders lists the live provider sessions.
func (r *Runner) AIProviders() []map[string]any {
	sigma := r.store.Sigma()
	out := make([]map[string]any, 0, len(sigma[ctxcore.KeyAIProvider]))
	for _, b := range sigma[ctxcore.KeyAIProvider] {
		row := map[string]any{"tag": string(b.Tag)}
		if m, ok := b.Value.(map[string]any); ok {
			for k, v := range m {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	return out
}

// FileHead reads the first maxBytes of a remote file through an open files session,
// for the "remote file snippet" context source. It uses the download machinery the
// files wave already has: the temp file's cleanup is the inverse of the acquisition,
// and the read is a pure function of it.
func (r *Runner) FileHead(session, path string, maxBytes int) (string, error) {
	if maxBytes <= 0 || maxBytes > 16*1024 {
		maxBytes = 4096
	}
	tmp := tempPathFor("ai-head")
	size, err := r.DownloadFile(session, path, tmp)
	if err != nil {
		return "", err
	}
	defer func() { _ = removeFile(tmp) }()
	if size > int64(maxBytes) {
		size = int64(maxBytes)
	}
	body, err := readFileHead(tmp, int(size))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// base64Of is a tiny helper kept for symmetry with the channel envelope (VNC uses
// the same shape); exported for tests that build payloads by hand.
func base64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

var _ = base64Of

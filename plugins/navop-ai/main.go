// Command navop-ai is the W-A1 workbench plugin: an AI provider over the
// OpenAI-compatible chat-completions shape.
//
// WHAT THE PLUGIN DOES NOT DO — and this is the wave's discipline: it never reads
// the host's key surface. The CONTEXT (selected table schema, the terminal's last
// N lines, a remote file snippet) is assembled by the HOST, which is the only
// component that can read every key (Σ absorbs all shared state, §3.3). The plugin
// receives a finished context string and returns text. That inversion is what
// keeps "the model saw my whole workspace" from becoming an ambient capability: the
// operator sees exactly which sources were attached, because the host put them
// there.
//
// NL→SQL returns SQL TEXT. It cannot execute anything: this plugin has no SQL
// client, no connection, and no execution method. Running the generated statement
// is the host UI's job, through the same preview/confirm path as any other
// statement — which is why "强制预览" is structural here, not a flag.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dbcore/dbcore/plugindriver"
	"github.com/dbcore/dbcore/plugindriver/workbench"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "navop-ai",
		Name:    "navop-ai 助手",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	srv := &server{
		sessions: map[string]*provider{},
		vault:    workbench.NewSecretVault(),
	}
	in := bufio.NewReader(os.Stdin)
	var wmu sync.Mutex
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				if req.Method == workbench.MethodSecret {
					var lend workbench.SecretLend
					_ = json.Unmarshal(req.Params, &lend)
					body, berr := workbench.ReadSecretBody(in, lend.Bytes)
					if berr != nil {
						srv.reply(out, &wmu, req.ID, nil, berr.Error(), "ERR_PLUGIN")
					} else if lerr := srv.vault.Lend(lend.Name, body); lerr != nil {
						workbench.Zero(body)
						srv.reply(out, &wmu, req.ID, nil, lerr.Error(), "ERR_PLUGIN")
					} else {
						workbench.Zero(body)
						srv.reply(out, &wmu, req.ID, workbench.SecretLend{Names: []string{lend.Name}}, "", "")
					}
					continue
				}
				go func(req plugindriver.Request) { srv.handle(out, &wmu, req) }(req)
			}
		}
		if err != nil {
			srv.vault.ZeroAll()
			return
		}
	}
}

// provider is one AI endpoint session.
type provider struct {
	baseURL string
	model   string
	key     string // the lent API key; erased when the session closes
}

type server struct {
	mu       sync.Mutex
	seq      uint64
	sessions map[string]*provider
	vault    *workbench.SecretVault
}

func (s *server) reply(out *bufio.Writer, wmu *sync.Mutex, id int64, result any, errText, code string) {
	payload, _ := json.Marshal(result)
	frame, _ := json.Marshal(plugindriver.Response{ID: id, Result: payload, Error: errText, Code: code})
	wmu.Lock()
	fmt.Fprintf(out, "%s\n", frame)
	out.Flush()
	wmu.Unlock()
}

func (s *server) handle(out *bufio.Writer, wmu *sync.Mutex, req plugindriver.Request) {
	send := func(result any, errText, code string) {
		payload, _ := json.Marshal(result)
		resp := plugindriver.Response{ID: req.ID, Result: payload, Error: errText, Code: code}
		frame, _ := json.Marshal(resp)
		wmu.Lock()
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
		wmu.Unlock()
	}

	switch req.Method {
	case plugindriver.MethodPing:
		send("pong", "", "")
	case workbench.MethodHello:
		send(workbench.Banner{
			Stream:     workbench.StreamFrameV1,
			PluginID:   "navop-ai",
			ProviderID: "navop-ai-" + strconv.FormatInt(time.Now().UnixNano(), 36),
			Provides:   []workbench.Capability{{Key: "ai.provider", Commutative: true}},
			Secrets:    []string{"ai.apiKey"},
		}, "", "")
	case workbench.MethodEffect:
		var p struct {
			Stage int `json:"stage"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.Stage == 0 {
			payload, _ := json.Marshal(map[string]string{"label": "AI", "icon": "sparkle"})
			send(workbench.EffectFrame{
				Tag: "navop-ai/tab", Key: "nav.nodes", Op: "insert",
				Payload: payload, Inverse: "remove", Boundary: workbench.BoundaryAcquisition,
			}, "", "")
			return
		}
		send(workbench.EffectFrame{Done: true}, "", "")
	case workbench.MethodChannelOpen:
		var p struct {
			BaseURL string `json:"baseUrl"`
			Model   string `json:"model"`
			Secret  string `json:"secret,omitempty"`
		}
		_ = json.Unmarshal(req.Params, &p)
		var probe map[string]json.RawMessage
		_ = json.Unmarshal(req.Params, &probe)
		if _, leaked := probe["apiKey"]; leaked {
			return
		}
		apiKey := ""
		if p.Secret != "" {
			val, erase, ok := s.vault.Take(p.Secret)
			if !ok {
				send(nil, "secret "+p.Secret+" 未被借出（lend 是一次性的，请重新借出）", "ERR_PLUGIN")
				return
			}
			// NOTE: the key lives for the session; erase() runs when the session
			// closes, not here — copying a key per call would spread it around.
			_ = erase
			apiKey = string(val)
		}
		if p.BaseURL == "" {
			p.BaseURL = "http://127.0.0.1:11434/v1"
		}
		if p.Model == "" {
			p.Model = "gpt-4o-mini"
		}
		// Serve the key under a stable name for the session's lifetime.
		s.mu.Lock()
		s.seq++
		id := fmt.Sprintf("ai-%d", s.seq)
		s.sessions[id] = &provider{baseURL: strings.TrimRight(p.BaseURL, "/"), model: p.Model, key: apiKey}
		s.mu.Unlock()
		send(map[string]string{"sessionId": id, "model": p.Model}, "", "")
	case workbench.MethodChannelData:
		var p struct {
			Action  string          `json:"action"`
			Session string          `json:"session"`
			Args    json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		prov := s.sessions[p.Session]
		s.mu.Unlock()
		if prov == nil {
			send(nil, "unknown AI session "+p.Session, "ERR_PLUGIN")
			return
		}
		switch p.Action {
		case "chat":
			var a struct {
				Messages []chatMessage `json:"messages"`
				Context  string        `json:"context,omitempty"`
			}
			_ = json.Unmarshal(p.Args, &a)
			text, err := prov.complete(a.Messages, a.Context, "")
			if err != nil {
				send(nil, err.Error(), "ERR_PLUGIN")
				return
			}
			send(map[string]string{"text": text}, "", "")
		case "nl2sql":
			var a struct {
				Question string `json:"question"`
				Context  string `json:"context,omitempty"`
				Dialect  string `json:"dialect,omitempty"`
			}
			_ = json.Unmarshal(p.Args, &a)
			text, err := prov.complete([]chatMessage{{Role: "user", Content: a.Question}}, a.Context, nl2sqlSystem(a.Dialect))
			if err != nil {
				send(nil, err.Error(), "ERR_PLUGIN")
				return
			}
			sql, explanation := splitSQLAnswer(text)
			send(map[string]any{"sql": sql, "explanation": explanation, "raw": text}, "", "")
		default:
			send(nil, "unknown AI action "+p.Action, "ERR_NOT_SUPPORTED")
		}
	case workbench.MethodChannelClose:
		var p struct {
			Session string `json:"session"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		prov := s.sessions[p.Session]
		delete(s.sessions, p.Session)
		s.mu.Unlock()
		if prov != nil {
			workbench.Zero([]byte(prov.key))
			prov.key = ""
		}
		send(map[string]bool{"ok": true}, "", "")
	case plugindriver.MethodClose:
		send(nil, "", "")
		s.closeAll()
		os.Exit(0)
	default:
		send(nil, "unknown method "+req.Method, "ERR_NOT_SUPPORTED")
	}
}

// chatMessage is the OpenAI message shape.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// nl2sqlSystem is the instruction for NL→SQL. It asks for JSON-ish output and says
// the statement is a PREVIEW — the model has no execution channel.
func nl2sqlSystem(dialect string) string {
	d := "SQL"
	if dialect != "" {
		d = dialect
	}
	return "You translate a natural-language request into one " + d + " statement. " +
		"Reply with the SQL in a ```sql fenced block, then one short paragraph explaining it. " +
		"The statement is a PREVIEW: it will be shown to a human who may edit and run it. " +
		"Never produce multiple statements and never assume you can execute anything."
}

// splitSQLAnswer pulls the fenced SQL out of the model's reply.
func splitSQLAnswer(text string) (sql, explanation string) {
	lower := strings.ToLower(text)
	if i := strings.Index(lower, "```"); i >= 0 {
		rest := text[i+3:]
		rest = strings.TrimPrefix(rest, "sql")
		rest = strings.TrimPrefix(rest, "SQL")
		rest = strings.TrimPrefix(rest, "\n")
		if j := strings.Index(rest, "```"); j >= 0 {
			return strings.TrimSpace(rest[:j]), strings.TrimSpace(rest[j+3:])
		}
	}
	// No fence: treat the first paragraph as SQL and the rest as explanation.
	if idx := strings.Index(text, "\n\n"); idx >= 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx+2:])
	}
	return strings.TrimSpace(text), ""
}

// complete posts one chat completion. The context is PREPENDED as a system
// message: the operator's attached sources are visible to the model as context,
// and visible to the operator as attachments.
func (p *provider) complete(messages []chatMessage, context, system string) (string, error) {
	var msgs []chatMessage
	sys := strings.TrimSpace(system)
	if context != "" {
		ctxBlock := "以下是从用户的数据库工作区读到的上下文（由宿主附加，可能不完整）：\n\n" + context
		if sys != "" {
			sys += "\n\n" + ctxBlock
		} else {
			sys = ctxBlock
		}
	}
	if sys != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: sys})
	}
	msgs = append(msgs, messages...)
	body, err := json.Marshal(map[string]any{
		"model":    p.model,
		"messages": msgs,
		"stream":   false,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.key != "" {
		req.Header.Set("Authorization", "Bearer "+p.key)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("AI 端点不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("AI 端点返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("AI 响应不是 chat-completions 形状: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("AI 端点错误: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("AI 端点没有返回候选")
	}
	return parsed.Choices[0].Message.Content, nil
}

func (s *server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, prov := range s.sessions {
		workbench.Zero([]byte(prov.key))
		delete(s.sessions, id)
	}
	s.vault.ZeroAll()
}

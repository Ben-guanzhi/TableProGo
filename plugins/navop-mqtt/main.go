// Command navop-mqtt is the W-Q1 workbench plugin: an MQTT console whose
// PUBLISH is an emission and whose SUBSCRIBE is an acquisition.
//
// THE SPLIT (§6.1 + AD-3), which is the whole point of the wave:
//
//	subscribe  → acquisition. The plugin sends SUBSCRIBE; the HOST tracks the row
//	             at mqtt.subscriptions (tag = topic), so it is REVERSIBLE:
//	             unsubscribe removes the row, and unloading the fiber reclaims it.
//	publish    → emission. There is no inverse, and the plugin never decides: the
//	             HOST withholds it behind Safe Mode (Σinter) exactly like a
//	             terminal broadcast, and the confirm is a SEPARATE endpoint.
//	             "publish" answers {withheld:true} when the gate holds.
//
// Credentials ride AD-4's lend (raw stdin body); a broker that needs no password
// is opened without one.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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
		Kind:    "navop-mqtt",
		Name:    "navop-mqtt 消息",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	srv := &server{
		sessions: map[string]*mqttSession{},
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

// mqttSession is one broker session: the live connection and its message ring.
type mqttSession struct {
	conn *mqttConn
	ring *messageRing
	// topics is the plugin's own record of what it subscribed (the HOST owns the
	// authoritative table; this only serves "which topic carried this message").
	mu     sync.Mutex
	topics []string
}

type server struct {
	mu       sync.Mutex
	seq      uint64
	sessions map[string]*mqttSession
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
			PluginID:   "navop-mqtt",
			ProviderID: "navop-mqtt-" + strconv.FormatInt(time.Now().UnixNano(), 36),
			Depends:    []workbench.Capability{{Key: "connections.store", Commutative: true}},
			Provides:   []workbench.Capability{{Key: "mqtt.session", Commutative: true}},
			Secrets:    []string{"mqtt.password"},
		}, "", "")
	case workbench.MethodEffect:
		// One stage: register the 消息 nav node (nav.nodes is the host-owned UI
		// table, so no Provides entry is needed — see navop-containers' note).
		var p struct {
			Stage int `json:"stage"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.Stage == 0 {
			payload, _ := json.Marshal(map[string]string{"label": "消息", "icon": "message"})
			send(workbench.EffectFrame{
				Tag: "navop-mqtt/tab", Key: "nav.nodes", Op: "insert",
				Payload: payload, Inverse: "remove", Boundary: workbench.BoundaryAcquisition,
			}, "", "")
			return
		}
		send(workbench.EffectFrame{Done: true}, "", "")
	case workbench.MethodChannelOpen:
		res, errText := s.open(req.Params)
		if errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(res, "", "")
	case workbench.MethodChannelData:
		var p struct {
			Action  string          `json:"action"`
			Session string          `json:"session"`
			Args    json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal(req.Params, &p)
		res, errText := s.action(p.Action, p.Session, p.Args)
		if errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(res, "", "")
	case workbench.MethodChannelClose:
		var p struct {
			Session string `json:"session"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.closeSession(p.Session)
		send(map[string]bool{"ok": true}, "", "")
	case plugindriver.MethodClose:
		send(nil, "", "")
		s.closeEverything()
		os.Exit(0)
	default:
		send(nil, "unknown method "+req.Method, "ERR_NOT_SUPPORTED")
	}
}

// open dials the broker. The password comes from the AD-4 vault (never a param).
type openParams struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Secret   string `json:"secret,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	Capacity int    `json:"capacity,omitempty"`
}

func (s *server) open(raw json.RawMessage) (any, string) {
	var p openParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, "bad open params: " + err.Error()
	}
	var probe map[string]json.RawMessage
	_ = json.Unmarshal(raw, &probe)
	if _, leaked := probe["password"]; leaked {
		return nil, "AD-4: 密码必须经秘密借出（stdin 原始体），不得随参数传输"
	}
	var password string
	if p.Secret != "" {
		val, erase, ok := s.vault.Take(p.Secret)
		if !ok {
			return nil, "secret " + p.Secret + " 未被借出（lend 是一次性的，请重新借出）"
		}
		defer erase()
		password = string(val)
	}
	clientID := p.ClientID
	if clientID == "" {
		clientID = fmt.Sprintf("tablepro-%d", time.Now().UnixNano())
	}
	conn, err := dialMQTT(p.Host, p.Port, clientID, p.User, password)
	if err != nil {
		return nil, err.Error()
	}
	cap := p.Capacity
	if cap <= 0 {
		cap = 500
	}
	sess := &mqttSession{conn: conn, ring: newMessageRing(cap)}
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("mqtt-%d", s.seq)
	s.sessions[id] = sess
	s.mu.Unlock()
	conn.StartPump(sess.ring)
	return map[string]string{"sessionId": id}, ""
}

// action dispatches the console's verbs. publish is called ONLY after the host's
// Safe Mode gate decided the emission may happen (the host has a confirmed path).
func (s *server) action(name, sessionID string, raw json.RawMessage) (any, string) {
	s.mu.Lock()
	sess := s.sessions[sessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, "unknown MQTT session " + sessionID
	}
	switch name {
	case "subscribe":
		var p struct {
			Topic string `json:"topic"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Topic == "" {
			return nil, "topic 不能为空"
		}
		if err := sess.conn.Subscribe(p.Topic); err != nil {
			return nil, err.Error()
		}
		sess.mu.Lock()
		sess.topics = append(sess.topics, p.Topic)
		sess.mu.Unlock()
		return map[string]bool{"ok": true}, ""
	case "unsubscribe":
		var p struct {
			Topic string `json:"topic"`
		}
		_ = json.Unmarshal(raw, &p)
		if err := sess.conn.Unsubscribe(p.Topic); err != nil {
			return nil, err.Error()
		}
		sess.mu.Lock()
		out := sess.topics[:0]
		for _, t := range sess.topics {
			if t != p.Topic {
				out = append(out, t)
			}
		}
		sess.topics = out
		sess.mu.Unlock()
		return map[string]bool{"ok": true}, ""
	case "publish":
		var p struct {
			Topic   string `json:"topic"`
			Payload string `json:"payload"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Topic == "" {
			return nil, "topic 不能为空"
		}
		if err := sess.conn.Publish(p.Topic, p.Payload); err != nil {
			return nil, err.Error()
		}
		// The emission happened; there is no undo to offer.
		return map[string]any{"published": true, "topic": p.Topic, "bytes": len(p.Payload)}, ""
	case "messages":
		var p struct {
			Since int `json:"since"`
		}
		_ = json.Unmarshal(raw, &p)
		msgs, next := sess.ring.Snapshot(p.Since)
		return map[string]any{"messages": msgs, "next": next, "total": sess.ring.Total()}, ""
	case "ping":
		if err := sess.conn.Ping(); err != nil {
			return nil, err.Error()
		}
		return map[string]bool{"ok": true}, ""
	case "close":
		s.closeSession(sessionID)
		return map[string]bool{"ok": true}, ""
	default:
		return nil, "unknown MQTT action " + name
	}
}

func (s *server) closeSession(id string) {
	s.mu.Lock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if sess != nil {
		sess.conn.Close()
	}
}

func (s *server) closeEverything() {
	s.mu.Lock()
	sessions := make([]*mqttSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.sessions = map[string]*mqttSession{}
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.conn.Close()
	}
}

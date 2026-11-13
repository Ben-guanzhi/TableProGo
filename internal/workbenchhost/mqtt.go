package workbenchhost

// mqtt.go — W-Q1's host half: an MQTT console whose PUBLISH is an emission and
// whose SUBSCRIBE is an acquisition.
//
// THE SPLIT (§6.1 + AD-3), which is what the wave is actually about:
//
//	Subscribe   → ACQUISITION. The subscription lands as a row at
//	              mqtt.subscriptions (tag = session/topic) via ctxcore.Track, so
//	              the fiber's accumulator reclaims it on unload (Cor 69) and an
//	              explicit unsubscribe removes it early. A subscription is
//	              REVERSIBLE, so it is an effect with an inverse.
//	Publish     → EMISSION. idΓ: there is no inverse, and the host must not
//	              pretend otherwise. It is WITHHELD behind Safe Mode (Σinter) —
//	              the same gate as a terminal broadcast — and the confirm is a
//	              SEPARATE call (PublishConfirmed), never a flag on the gated
//	              path. A withheld publish sends NOTHING to the broker.
//
// The plugin does not decide any of this: it writes bytes when told. The gate is
// host policy, which is why flipping Safe Mode changes no fiber (Σinter is a
// derived realization, not an effect).

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/dbcore/dbcore/plugindriver/workbench"
	"tablepro-go/internal/ctxcore"
)

// MQTTResult is the shape the console renders: what the broker said, or the
// withhold notice with everything the confirm needs to re-post.
type MQTTResult struct {
	// Withheld means Safe Mode held the publish back; nothing was sent.
	Withheld bool `json:"withheld,omitempty"`
	// Topic/Payload echo the withheld emission so the confirm bar re-posts
	// EXACTLY the same bytes (an emission must not be re-editable in transit).
	Topic   string `json:"topic,omitempty"`
	Payload string `json:"payload,omitempty"`
	// Published reports that the emission happened — and there is no undo.
	Published bool   `json:"published,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	Warning   string `json:"warning,omitempty"`
	Session   string `json:"session,omitempty"`
}

// mqttClient is the host's view of one open session.
type mqttClient struct {
	sessionID string
	// topics is what the host believes is subscribed. The authoritative record is
	// mqtt.subscriptions; this mirrors it for the confirm path.
	mu     sync.Mutex
	topics []string
}

// OpenMQTTSession dials a broker through the plugin, lending the password (AD-4).
func (r *Runner) OpenMQTTSession(host string, port int, user string, secretName string, secret []byte) (string, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return "", fmt.Errorf("%s: the MQTT plugin fiber is not active", workbench.CodeInactive)
	}
	// AD-4: lend only when there IS a password. An anonymous broker is opened
	// without a lend — lending an empty secret would be refused by the vault and
	// is not what the operator asked for.
	if secretName != "" && len(secret) > 0 {
		erase, err := r.LendSecret(secretName, secret)
		if err != nil {
			return "", err
		}
		defer func() { _ = erase() }()
	}
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return "", fmt.Errorf("MQTT plugin process is not running")
	}
	params := map[string]any{"host": host, "port": port, "user": user}
	if secretName != "" {
		params["secret"] = secretName
	}
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := p.call(context.Background(), workbench.MethodChannelOpen, params, &res); err != nil {
		return "", err
	}
	// The session handle itself is a runtime effect: binding it means unload
	// closes it (and with it every subscription's broker state).
	if err := r.store.Track(f, "open-mqtt:"+res.SessionID, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyMQTTSession, ctxcore.Tag(res.SessionID), f.UID(),
			map[string]any{"host": host, "port": port}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyMQTTSession)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableRemove(ctxcore.KeyMQTTSession, ctxcore.Tag(res.SessionID)); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyMQTTSession)
			return nil
		}, nil
	}); err != nil {
		return "", err
	}
	return res.SessionID, nil
}

// mqttCall is the one wire to the plugin.
func (r *Runner) mqttCall(session, action string, args any) (map[string]any, error) {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return nil, fmt.Errorf("MQTT plugin process is not running")
	}
	var out map[string]any
	if err := p.call(context.Background(), workbench.MethodChannelData,
		map[string]any{"action": action, "session": session, "args": args}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Subscribe is the ACQUISITION: it subscribes at the broker and records the row
// at mqtt.subscriptions, tracked on the fiber so unloading reclaims it.
func (r *Runner) SubscribeTopic(session, topic string) error {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return fmt.Errorf("%s: the MQTT plugin fiber is not active", workbench.CodeInactive)
	}
	if topic == "" {
		return fmt.Errorf("topic 不能为空")
	}
	if _, err := r.mqttCall(session, "subscribe", map[string]any{"topic": topic}); err != nil {
		return err
	}
	tag := ctxcore.Tag(session + "/" + topic)
	if err := r.store.Track(f, "subscribe:"+string(tag), func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyMQTTSubscriptions, tag, f.UID(),
			map[string]any{"topic": topic}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyMQTTSubscriptions)
		return func(st2 *ctxcore.Store) error {
			// The inverse is the broker-side unsubscribe AND the row's removal: a
			// subscription is only truly withdrawn when neither side holds it.
			_, _ = r.mqttCall(session, "unsubscribe", map[string]any{"topic": topic})
			if err := st2.TableRemove(ctxcore.KeyMQTTSubscriptions, tag); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyMQTTSubscriptions)
			return nil
		}, nil
	}); err != nil {
		// The broker subscribed but the host could not record it: undo the half
		// that exists rather than leave an untracked subscription behind.
		_, _ = r.mqttCall(session, "unsubscribe", map[string]any{"topic": topic})
		return err
	}
	return nil
}

// UnsubscribeTopic is the operator's early teardown of one subscription (the same
// inverse the accumulator would run).
func (r *Runner) UnsubscribeTopic(session, topic string) error {
	if _, err := r.mqttCall(session, "unsubscribe", map[string]any{"topic": topic}); err != nil {
		return err
	}
	return r.store.TableRemove(ctxcore.KeyMQTTSubscriptions, ctxcore.Tag(session+"/"+topic))
}

// PublishTopic is THE EMISSION, gated: with Safe Mode on it is WITHHELD and sends
// nothing; the caller must re-post through PublishConfirmed. There is deliberately
// no "force" parameter — a flag on the gated path is exactly the bypass the
// design forbids.
func (r *Runner) PublishTopic(session, topic, payload string) (MQTTResult, error) {
	if r.WithholdBroadcast() {
		return MQTTResult{
			Withheld: true, Topic: topic, Payload: payload, Session: session,
			Warning: "Safe Mode 打开：发布被扣留，未向代理发送任何字节。确认后不可撤回。",
		}, nil
	}
	return r.publishNow(session, topic, payload)
}

// PublishConfirmed is the separate confirm path: it executes regardless of Safe
// Mode because the operator confirmed THIS emission explicitly.
func (r *Runner) PublishConfirmed(session, topic, payload string) (MQTTResult, error) {
	return r.publishNow(session, topic, payload)
}

func (r *Runner) publishNow(session, topic, payload string) (MQTTResult, error) {
	if strings.TrimSpace(topic) == "" {
		return MQTTResult{}, fmt.Errorf("topic 不能为空")
	}
	if _, err := r.mqttCall(session, "publish", map[string]any{"topic": topic, "payload": payload}); err != nil {
		return MQTTResult{}, err
	}
	return MQTTResult{Published: true, Topic: topic, Payload: payload, Bytes: len(payload), Session: session}, nil
}

// MQTTMessages polls the received ring. since is the ring cursor the console got
// last time (bounded buffer: the panel may miss messages while closed, and says so
// via total).
func (r *Runner) MQTTMessages(session string, since int) ([]map[string]any, int, uint64, error) {
	out, err := r.mqttCall(session, "messages", map[string]any{"since": since})
	if err != nil {
		return nil, 0, 0, err
	}
	rows := make([]map[string]any, 0)
	if raw, ok := out["messages"].([]any); ok {
		for _, r := range raw {
			if m, ok := r.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
	}
	next, _ := out["next"].(float64)
	var total uint64
	if t, ok := out["total"].(float64); ok {
		total = uint64(t)
	}
	return rows, int(next), total, nil
}

// CloseMQTTSession drops the session (the accumulator's inverse would do the same).
func (r *Runner) CloseMQTTSession(session string) error {
	if _, err := r.mqttCall(session, "close", nil); err != nil {
		// A dead process is already closed; fall through to the row removal.
		_ = err
	}
	return r.store.TableRemove(ctxcore.KeyMQTTSession, ctxcore.Tag(session))
}

// MQTTSubscriptions lists the live subscription rows (the console shows what the
// HOST believes, which is the authoritative record).
func (r *Runner) MQTTSubscriptions() []map[string]any {
	sigma := r.store.Sigma()
	out := make([]map[string]any, 0, len(sigma[ctxcore.KeyMQTTSubscriptions]))
	for _, b := range sigma[ctxcore.KeyMQTTSubscriptions] {
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

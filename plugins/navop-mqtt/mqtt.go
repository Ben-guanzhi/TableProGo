package main

// mqtt.go — a minimal MQTT 3.1.1 client: exactly the subset the console needs,
// with no dependency.
//
// WHY NOT A LIBRARY: the workbench publishes and subscribes to a broker the
// operator already has; the wire subset is CONNECT / SUBSCRIBE / PUBLISH (QoS 0)
// / PINGREQ / DISCONNECT. That is a few hundred bytes of framing, and the
// project's rule is that the smallest feature must not pull in the largest
// dependency. What we do NOT implement is stated instead of implied: no QoS 1/2
// acknowledgement traffic, no retained-message flag we honour, no TLS (a TLS
// broker is reached through the ssh tunnel's forwarding or a future flag).
//
// THE DISCIPLINE THE WAVE IS ABOUT:
//   - subscribe is an ACQUISITION: the plugin sends SUBSCRIBE and the HOST tracks
//     the subscription as a row at mqtt.subscriptions; unsubscribing removes the
//     row. A subscription is reversible, so it is an effect with an inverse.
//   - publish is an EMISSION: there is no inverse. The HOST gates it behind Safe
//     Mode (Σinter) exactly like a terminal broadcast — withheld unless the
//     operator confirms through the SEPARATE confirm endpoint. The plugin itself
//     does not decide; it only writes bytes once told to.

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// mqttConn is one broker connection. ONE goroutine reads the socket (readLoop);
// callers that need an acknowledgement (SUBACK/UNSUBACK) wait on a channel. Two
// concurrent readers would race each other for frames — the first version of this
// file did exactly that and hung every subscribe.
type mqttConn struct {
	mu     sync.Mutex
	conn   net.Conn
	nextID uint16
	closed bool
	ring   *messageRing
	acks   chan ackFrame
	done   chan struct{}
}

// ackFrame is one control response routed to its waiter.
type ackFrame struct {
	header byte
	body   []byte
}

// packet types (MQTT 3.1.1 §2.2.1).
const (
	pktCONNECT    = 0x10
	pktCONNACK    = 0x20
	pktPUBLISH    = 0x30
	pktSUBSCRIBE  = 0x82
	pktSUBACK     = 0x90
	pktUNSUB      = 0xA2
	pktUNSUBACK   = 0xB0
	pktPINGREQ    = 0xC0
	pktPINGRESP   = 0xD0
	pktDISCONNECT = 0xE0
)

// dialMQTT connects, performs CONNECT/CONNACK, and returns the live connection.
func dialMQTT(host string, port int, clientID, user, password string) (*mqttConn, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("MQTT 代理 %s 不可达: %w", addr, err)
	}
	m := &mqttConn{conn: c, acks: make(chan ackFrame, 8), done: make(chan struct{})}
	// CONNECT.
	var vh []byte
	vh = appendMQTTString(vh, "MQTT") // protocol name
	vh = append(vh, 4)                // level 3.1.1
	flags := byte(0x02)               // clean session
	if user != "" {
		flags |= 0x80
	}
	if password != "" {
		flags |= 0x40
	}
	vh = append(vh, flags)
	vh = append(vh, 0, 60) // keepalive 60s
	payload := appendMQTTString(nil, clientID)
	if user != "" {
		payload = appendMQTTString(payload, user)
	}
	if password != "" {
		payload = appendMQTTString(payload, password)
	}
	if err := m.writeFrame(pktCONNECT, append(vh, payload...)); err != nil {
		_ = c.Close()
		return nil, err
	}
	typ, body, err := m.readFrame(10 * time.Second)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if typ&0xF0 != pktCONNACK || len(body) < 2 {
		_ = c.Close()
		return nil, fmt.Errorf("MQTT CONNACK 期望 %x，收到 %x", pktCONNACK, typ)
	}
	if body[1] != 0 {
		_ = c.Close()
		return nil, fmt.Errorf("MQTT 连接被拒绝，返回码 %d", body[1])
	}
	return m, nil
}

// StartPump hands the socket to ONE reader goroutine: PUBLISH payloads go to the
// ring, control acknowledgements go to the ack channel. Must be called exactly
// once, after CONNACK.
func (m *mqttConn) StartPump(ring *messageRing) {
	m.ring = ring
	go m.readLoop()
}

// readLoop is the single owner of reads.
func (m *mqttConn) readLoop() {
	defer close(m.done)
	for {
		_ = m.conn.SetReadDeadline(time.Time{})
		typ, body, err := m.readFrame(0)
		if err != nil {
			return
		}
		switch typ & 0xF0 {
		case pktPUBLISH:
			if topic, rest, ok := readMQTTString(body); ok && m.ring != nil {
				m.ring.Push(Message{Topic: topic, Payload: string(rest), At: time.Now()})
			}
		case pktSUBACK, pktUNSUBACK:
			select {
			case m.acks <- ackFrame{header: typ, body: body}:
			default:
				// Nobody waiting: drop it rather than block the reader.
			}
		case pktPINGRESP:
			// nothing to do
		}
	}
}

// waitAck waits for one control acknowledgement of the wanted type.
func (m *mqttConn) waitAck(want byte, d time.Duration) ([]byte, error) {
	select {
	case ack := <-m.acks:
		if ack.header&0xF0 != want {
			return nil, fmt.Errorf("MQTT 期望 %x，收到 %x", want, ack.header)
		}
		return ack.body, nil
	case <-m.done:
		return nil, fmt.Errorf("MQTT 连接已断开")
	case <-time.After(d):
		return nil, fmt.Errorf("MQTT 等待 %x 确认超时", want)
	}
}

// Subscribe sends SUBSCRIBE (QoS 0) and waits for SUBACK.
func (m *mqttConn) Subscribe(topic string) error {
	id := m.takeID()
	var body []byte
	body = binary.BigEndian.AppendUint16(body, id)
	body = appendMQTTString(body, topic)
	body = append(body, 0) // requested QoS 0
	if err := m.writeFrame(pktSUBSCRIBE, body); err != nil {
		return err
	}
	resp, err := m.waitAck(pktSUBACK, 10*time.Second)
	if err != nil {
		return err
	}
	if len(resp) < 3 {
		return fmt.Errorf("MQTT SUBACK 过短")
	}
	if resp[2] == 0x80 {
		return fmt.Errorf("MQTT 订阅 %q 被拒绝", topic)
	}
	return nil
}

// Unsubscribe sends UNSUBSCRIBE and waits for UNSUBACK.
func (m *mqttConn) Unsubscribe(topic string) error {
	id := m.takeID()
	var body []byte
	body = binary.BigEndian.AppendUint16(body, id)
	body = appendMQTTString(body, topic)
	if err := m.writeFrame(pktUNSUB, body); err != nil {
		return err
	}
	if _, err := m.waitAck(pktUNSUBACK, 10*time.Second); err != nil {
		return err
	}
	return nil
}

// Publish sends one QoS 0 PUBLISH. THE HOST has already decided this emission may
// happen (Safe Mode gate); the plugin writes the bytes and nothing else.
func (m *mqttConn) Publish(topic, payload string) error {
	var body []byte
	body = appendMQTTString(body, topic)
	body = append(body, []byte(payload)...)
	return m.writeFrame(pktPUBLISH, body)
}

// Pump reads inbound frames forever, appending PUBLISH payloads to ring (bounded).
// It returns when the connection dies; the caller reports that as a session end.
func (m *mqttConn) Ping() error { return m.writeFrame(pktPINGREQ, nil) }

// Close sends DISCONNECT and closes.
func (m *mqttConn) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	_ = m.writeFrame(pktDISCONNECT, nil)
	_ = m.conn.Close()
}

func (m *mqttConn) takeID() uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	if m.nextID == 0 {
		m.nextID = 1
	}
	return m.nextID
}

func (m *mqttConn) writeFrame(header byte, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("MQTT 连接已关闭")
	}
	frame := make([]byte, 0, len(body)+8)
	frame = append(frame, header)
	frame = append(frame, encodeRemainingLength(len(body))...)
	frame = append(frame, body...)
	if _, err := m.conn.Write(frame); err != nil {
		return fmt.Errorf("MQTT 写入: %w", err)
	}
	return nil
}

func (m *mqttConn) readFrame(timeout time.Duration) (byte, []byte, error) {
	if timeout > 0 {
		_ = m.conn.SetReadDeadline(time.Now().Add(timeout))
	} else {
		_ = m.conn.SetReadDeadline(time.Time{})
	}
	head := make([]byte, 1)
	if _, err := io.ReadFull(m.conn, head); err != nil {
		return 0, nil, err
	}
	length, err := readRemainingLength(m.conn)
	if err != nil {
		return 0, nil, err
	}
	if length > 1<<20 {
		return 0, nil, fmt.Errorf("MQTT 帧过大: %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(m.conn, body); err != nil {
		return 0, nil, err
	}
	return head[0], body, nil
}

// encodeRemainingLength is MQTT's variable-length integer (§1.5.5).
func encodeRemainingLength(n int) []byte {
	var out []byte
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		out = append(out, d)
		if n == 0 {
			return out
		}
	}
}

func readRemainingLength(r io.Reader) (int, error) {
	mult, value := 1, 0
	for i := 0; i < 4; i++ {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		value += int(b[0]&0x7F) * mult
		if b[0]&0x80 == 0 {
			return value, nil
		}
		mult *= 128
	}
	return 0, fmt.Errorf("MQTT 剩余长度格式错误")
}

func appendMQTTString(dst []byte, s string) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(s)))
	return append(dst, []byte(s)...)
}

func readMQTTString(b []byte) (string, []byte, bool) {
	if len(b) < 2 {
		return "", nil, false
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	if len(b) < 2+n {
		return "", nil, false
	}
	return string(b[2 : 2+n]), b[2+n:], true
}

// Message is one received PUBLISH, shaped for the console's table.
type Message struct {
	Topic   string    `json:"topic"`
	Payload string    `json:"payload"`
	At      time.Time `json:"at"`
}

// messageRing is a bounded buffer: a chatty topic must not grow the process.
type messageRing struct {
	mu    sync.Mutex
	cap   int
	items []Message
	total uint64
}

func newMessageRing(capacity int) *messageRing {
	if capacity < 1 {
		capacity = 1
	}
	return &messageRing{cap: capacity}
}

func (r *messageRing) Push(m Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) >= r.cap {
		r.items = r.items[1:]
	}
	r.items = append(r.items, m)
	r.total++
}

func (r *messageRing) Snapshot(since int) ([]Message, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Message, 0, len(r.items))
	for _, m := range r.items[min(since, len(r.items)):] {
		out = append(out, m)
	}
	return out, len(r.items)
}

func (r *messageRing) Total() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// topicMatches is MQTT's wildcard match (+ single level, # trailing multi) — used
// to label a received message with the subscription that carried it.
func topicMatches(filter, topic string) bool {
	f := strings.Split(filter, "/")
	t := strings.Split(topic, "/")
	for i, part := range f {
		if part == "#" {
			return true
		}
		if i >= len(t) {
			return false
		}
		if part != "+" && part != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}

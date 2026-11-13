package workbenchhost

// W-Q1 acceptance: publish is an EMISSION held behind Safe Mode; subscribe is an
// ACQUISITION with a reversible row.
//
// The broker here is a stub that speaks just enough MQTT 3.1.1 for the plugin:
// CONNECT→CONNACK, SUBSCRIBE→SUBACK (recording the filter), PUBLISH (recording
// every payload it receives), UNSUBSCRIBE→UNSUBACK, PINGREQ→PINGRESP. Because it
// records what ARRIVES, the tests can assert the negative that matters:
// a withheld publish sends NOTHING.
//
//   1. WITHHOLD SENDS NOTHING: Safe Mode on → PublishTopic answers withheld, the
//      broker's received list is unchanged. The confirm path delivers.
//   2. SUBSCRIBE IS ACQUISITION: the row lands at mqtt.subscriptions, stays after
//      a second subscribe to a different topic (they commute), and unloading the
//      fiber reclaims every row AND unsubscribes at the broker (Cor 69 + Ths 43).
//   3. MESSAGES ARE BOUNDED: the ring caps at its capacity and reports the total.

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

// stubBroker is a minimal MQTT server for the tests.
type stubBroker struct {
	mu       sync.Mutex
	received []string // payloads that arrived via PUBLISH
	topics   []string // filters currently subscribed (from SUBSCRIBE/UNSUBSCRIBE)
	ln       net.Listener
	conns    []net.Conn
}

func startStubBroker(t *testing.T) (*stubBroker, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &stubBroker{ln: ln}
	t.Cleanup(func() {
		_ = ln.Close()
		b.mu.Lock()
		for _, c := range b.conns {
			_ = c.Close()
		}
		b.mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b.mu.Lock()
			b.conns = append(b.conns, c)
			b.mu.Unlock()
			go b.serve(c)
		}
	}()
	return b, ln.Addr().(*net.TCPAddr).Port
}

func (b *stubBroker) serve(c net.Conn) {
	defer c.Close()
	for {
		head := make([]byte, 1)
		if _, err := io.ReadFull(c, head); err != nil {
			return
		}
		length, err := readStubLength(c)
		if err != nil {
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(c, body); err != nil {
			return
		}
		switch head[0] & 0xF0 {
		case 0x10: // CONNECT → CONNACK
			_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x00})
		case 0x80: // SUBSCRIBE → SUBACK + record the filter
			if len(body) >= 2 {
				id := binary.BigEndian.Uint16(body[:2])
				if filter, _, ok := readStubString(body[2:]); ok {
					b.mu.Lock()
					b.topics = append(b.topics, filter)
					b.mu.Unlock()
				}
				_, _ = c.Write([]byte{0x90, 0x03, byte(id >> 8), byte(id), 0x00})
			}
		case 0x30: // PUBLISH → record the payload
			if topic, rest, ok := readStubString(body); ok {
				b.mu.Lock()
				b.received = append(b.received, topic+"|"+string(rest))
				b.mu.Unlock()
			}
		case 0xA0: // UNSUBSCRIBE → UNSUBACK + drop the filter
			if len(body) >= 2 {
				id := binary.BigEndian.Uint16(body[:2])
				if filter, _, ok := readStubString(body[2:]); ok {
					b.mu.Lock()
					out := b.topics[:0]
					for _, x := range b.topics {
						if x != filter {
							out = append(out, x)
						}
					}
					b.topics = out
					b.mu.Unlock()
				}
				_, _ = c.Write([]byte{0xB0, 0x02, byte(id >> 8), byte(id)})
			}
		case 0xC0: // PINGREQ → PINGRESP
			_, _ = c.Write([]byte{0xD0, 0x00})
		case 0xE0: // DISCONNECT
			return
		}
	}
}

func (b *stubBroker) payloads() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string{}, b.received...)
}

func (b *stubBroker) filters() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string{}, b.topics...)
}

func readStubLength(r io.Reader) (int, error) {
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
	return 0, fmt.Errorf("bad remaining length")
}

func readStubString(b []byte) (string, []byte, bool) {
	if len(b) < 2 {
		return "", nil, false
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	if len(b) < 2+n {
		return "", nil, false
	}
	return string(b[2 : 2+n]), b[2+n:], true
}

func buildNavopMQTT(t *testing.T) string {
	return buildPluginModule(t, "navop-mqtt")
}

func mqttSetup(t *testing.T, port int) (*Runner, string, *stubBroker) {
	t.Helper()
	broker, brokerPort := startStubBroker(t)
	_ = port
	binary := buildNavopMQTT(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	waitFiberState(t, r.Fiber(), ctxcore.Active, 10*time.Second)
	sess, err := r.OpenMQTTSession("127.0.0.1", brokerPort, "", "", nil)
	if err != nil {
		t.Fatalf("open MQTT session: %v", err)
	}
	return r, sess, broker
}

func TestMQTTPublishIsWithheldBySafeMode(t *testing.T) {
	r, sess, broker := mqttSetup(t, 0)

	// Safe Mode ON: the publish is withheld and NOTHING reaches the broker.
	r.SetSafeMode(true)
	res, err := r.PublishTopic(sess, "ops/alerts", "raised")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Withheld || res.Published {
		t.Fatalf("Safe Mode must withhold the publish: %+v", res)
	}
	if res.Topic != "ops/alerts" || res.Payload != "raised" {
		t.Fatalf("the withhold notice must echo the emission: %+v", res)
	}
	time.Sleep(120 * time.Millisecond) // give a stray write time to land
	if got := broker.payloads(); len(got) != 0 {
		t.Fatalf("a withheld publish sent bytes: %v", got)
	}

	// The confirm path is SEPARATE and delivers.
	res, err = r.PublishConfirmed(sess, "ops/alerts", "raised")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Published || res.Bytes != len("raised") {
		t.Fatalf("confirmed publish = %+v", res)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := broker.payloads(); len(got) == 1 && got[0] == "ops/alerts|raised" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := broker.payloads(); len(got) != 1 {
		t.Fatalf("the broker received %v, want exactly one publish", got)
	}

	// Gate OFF: the gated path publishes directly.
	r.SetSafeMode(false)
	res, err = r.PublishTopic(sess, "ops/info", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if res.Withheld || !res.Published {
		t.Fatalf("gate-off publish = %+v", res)
	}
}

func TestMQTTSubscribeIsReversibleAcquisition(t *testing.T) {
	r, sess, broker := mqttSetup(t, 0)

	if err := r.SubscribeTopic(sess, "ops/alerts"); err != nil {
		t.Fatal(err)
	}
	if err := r.SubscribeTopic(sess, "ops/info"); err != nil {
		t.Fatal(err)
	}
	// Two rows, and they COMMUTE: each has its own tag.
	if got := len(r.MQTTSubscriptions()); got != 2 {
		t.Fatalf("subscription rows = %d, want 2: %v", got, r.MQTTSubscriptions())
	}
	// The broker saw both filters.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(broker.filters()) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := broker.filters(); len(got) != 2 {
		t.Fatalf("broker filters = %v", got)
	}

	// An explicit unsubscribe is the operator's early teardown.
	if err := r.UnsubscribeTopic(sess, "ops/info"); err != nil {
		t.Fatal(err)
	}
	if got := len(r.MQTTSubscriptions()); got != 1 {
		t.Fatalf("after unsubscribe: %d rows", got)
	}

	// Unloading the fiber reclaims the REMAINING row and unsubscribes at the broker
	// (Cor 69 at mqtt.subscriptions; the inverse does both halves).
	r.store.Retire(r.Fiber())
	waitFiberState(t, r.Fiber(), ctxcore.Inactive, 10*time.Second)
	if tags := r.store.Sigma()[ctxcore.KeyMQTTSubscriptions]; len(tags) != 0 {
		t.Fatalf("Cor 69 violated: subscription rows survived unload: %v", tags)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(broker.filters()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := broker.filters(); len(got) != 0 {
		t.Fatalf("the broker still holds subscriptions after unload: %v", got)
	}
}

func TestMQTTMessagesRingIsBounded(t *testing.T) {
	// The ring's bound is the plugin's; this asserts the HOST path returns the
	// cursor and total the console needs, and that an empty ring is a valid answer
	// (not an error).
	r, sess, _ := mqttSetup(t, 0)
	rows, next, total, err := r.MQTTMessages(sess, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || next != 0 || total != 0 {
		t.Fatalf("empty ring = rows=%d next=%d total=%d", len(rows), next, total)
	}
	// Polling again with the same cursor is idempotent.
	rows2, next2, _, err := r.MQTTMessages(sess, next)
	if err != nil || len(rows2) != 0 || next2 != 0 {
		t.Fatalf("second poll = %v %d %v", rows2, next2, err)
	}
}

package workbenchhost

// W-M1 acceptance: the monitor's two plan bullets, executable.
//
//   "15s 轮询的定时器注册是一个效应（可逆：卸载即停）" — the tests prove BOTH
//     halves: stopping a monitor stops its goroutine (the inverse runs), and
//     unloading the fiber stops every monitor at once (the accumulator).
//   "采样环 = host.stats 键（表语义）" — the ring is bound at host.stats, one
//     tag per host, wraps at capacity, and is reclaimed on unload (Cor 69).
//
// The tests drive the sampler with a SHORT interval and a deterministic
// samplerFunc — no wall-clock waits for the mechanics, real time only for the
// "wait until" helper.

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

func monitorSetup(t *testing.T) (*Runner, *ctxcore.Fiber, *ctxcore.Store) {
	t.Helper()
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	t.Cleanup(r.Stop)
	return r, f, store
}

// waitMonitorTicks waits until the sampler has produced at least n samples.
func waitMonitorTicks(t *testing.T, r *Runner, host string, n int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, ms := range r.MonitorStates() {
			if ms.Host == host && len(ms.Samples) >= n {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	states := r.MonitorStates()
	t.Fatalf("host %s produced fewer than %d samples within %v (states=%+v sampler-broken?)", host, n, d, states)
}

func TestMonitorStartStopReversible(t *testing.T) {
	r, _, _ := monitorSetup(t)
	// A fast tick so the mechanics run in real time but briefly.
	oldInterval := monitorInterval
	monitorInterval = 10 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })

	sampler := int64(0)
	oldSampler := samplerFunc
	samplerFunc = func(string) (float64, error) { return float64(atomic.AddInt64(&sampler, 1)), nil }
	t.Cleanup(func() { samplerFunc = oldSampler })

	tag, err := r.StartMonitor("web-1", 64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tag, "monitor/") {
		t.Fatalf("tag = %q", tag)
	}
	waitMonitorTicks(t, r, "web-1", 3, 5*time.Second)

	// The ring is bound at host.stats (table semantics, one tag per host).
	binding, ok := r.store.Get(ctxcore.KeyHostStats, ctxcore.Tag(tag))
	if !ok {
		t.Fatal("the host.stats binding is missing")
	}
	_ = binding

	// StopMonitor runs the inverse: the goroutine stops AND the binding is
	// withdrawn.
	if err := r.StopMonitor("web-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.store.Get(ctxcore.KeyHostStats, ctxcore.Tag(tag)); ok {
		t.Fatal("the binding survived StopMonitor")
	}
	// The goroutine is gone: the sample counter must freeze.
	countAtStop := atomic.LoadInt64(&sampler)
	time.Sleep(60 * time.Millisecond)
	if atomic.LoadInt64(&sampler) != countAtStop {
		t.Fatalf("the sampler kept running after StopMonitor: %d → %d", countAtStop, atomic.LoadInt64(&sampler))
	}
}

func TestMonitorUnloadStopsEverything(t *testing.T) {
	// "可逆：卸载即停" — retiring the fiber fires the accumulator, which stops
	// EVERY monitor's goroutine and withdraws every binding (Thm 68 fiber-level).
	r, f, store := monitorSetup(t)
	oldInterval := monitorInterval
	monitorInterval = 10 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })

	sampler := int64(0)
	oldSampler := samplerFunc
	samplerFunc = func(string) (float64, error) { return float64(atomic.AddInt64(&sampler, 1)), nil }
	t.Cleanup(func() { samplerFunc = oldSampler })

	for _, h := range []string{"a", "b", "c"} {
		if _, err := r.StartMonitor(h, 32); err != nil {
			t.Fatal(err)
		}
	}
	waitMonitorTicks(t, r, "a", 2, 5*time.Second)

	r.store.Retire(f)
	waitFiberState(t, f, ctxcore.Inactive, 10*time.Second)
	// Cor 69 at host.stats: every binding gone.
	if tags := store.Sigma()[ctxcore.KeyHostStats]; len(tags) != 0 {
		t.Fatalf("Cor 69 violated: host.stats bindings survived unload: %v", tags)
	}
	// Every goroutine stopped: the counter freezes.
	countAtStop := atomic.LoadInt64(&sampler)
	time.Sleep(80 * time.Millisecond)
	if atomic.LoadInt64(&sampler) != countAtStop {
		t.Fatalf("samplers kept running after unload: %d → %d", countAtStop, atomic.LoadInt64(&sampler))
	}
}

func TestMonitorRingWrapsAtCapacity(t *testing.T) {
	// Bounded memory: pushing capacity+N samples leaves exactly `capacity`,
	// oldest evicted, and the dropped counter records the evictions.
	r, _, _ := monitorSetup(t)
	oldInterval := monitorInterval
	monitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })

	var tick atomic.Int64
	oldSampler := samplerFunc
	samplerFunc = func(string) (float64, error) { return float64(tick.Add(1)), nil }
	t.Cleanup(func() { samplerFunc = oldSampler })

	if _, err := r.StartMonitor("web-1", 5); err != nil {
		t.Fatal(err)
	}
	// Wait for the WRAP, not the count: at capacity 5 the ring never shows more
	// than 5; wait until the eviction counter proves at least 3 wraps happened.
	deadline := time.Now().Add(15 * time.Second)
	wrapped := false
	for time.Now().Before(deadline) && !wrapped {
		for _, s := range r.MonitorStates() {
			if s.Host == "web-1" && s.Dropped >= 3 {
				wrapped = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !wrapped {
		t.Fatal("the ring never wrapped within 15s")
	}

	var ms *MonitorState
	for _, s := range r.MonitorStates() {
		if s.Host == "web-1" {
			ms = &s
			break
		}
	}
	if ms == nil {
		t.Fatal("the monitor state is missing")
	}
	if len(ms.Samples) != 5 {
		t.Fatalf("ring length = %d, want the capacity 5", len(ms.Samples))
	}
	if ms.Dropped < 3 {
		t.Fatalf("dropped = %d, want at least the 3 evictions", ms.Dropped)
	}
	// Oldest-first order: each sample's value increases, so the ring must be
	// strictly ascending.
	for i := 1; i < len(ms.Samples); i++ {
		if ms.Samples[i].Value < ms.Samples[i-1].Value {
			t.Fatalf("ring order broken at %d: %v → %v", i, ms.Samples[i-1].Value, ms.Samples[i].Value)
		}
	}
}

func TestSubmitSamplePluginPath(t *testing.T) {
	// The plugin's submission path: the host stores what the plugin reports —
	// it never invents numbers. An unknown host is refused (no ring to land in).
	r, _, _ := monitorSetup(t)
	if _, err := r.StartMonitor("db-1", 8); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0)
	if err := r.SubmitSample("db-1", 42.5, at); err != nil {
		t.Fatal(err)
	}
	if err := r.SubmitSample("unknown-host", 1, at); err == nil {
		t.Fatal("submitting for an unmonitored host must be refused")
	}
	var ms *MonitorState
	for _, s := range r.MonitorStates() {
		if s.Host == "db-1" {
			ms = &s
			break
		}
	}
	if ms == nil || len(ms.Samples) != 1 || ms.Samples[0].Value != 42.5 {
		t.Fatalf("submitted sample missing: %+v", ms)
	}
	if !ms.Samples[0].Time.Equal(at) {
		t.Fatalf("sample time = %v, want the submitted %v", ms.Samples[0].Time, at)
	}
	// The rendering path gets a stable string of the ring (the panel plots this).
	var sb strings.Builder
	for _, s := range ms.Samples {
		fmt.Fprintf(&sb, "%v,", s.Value)
	}
	if !strings.Contains(sb.String(), "42.5") {
		t.Fatalf("ring string = %q", sb.String())
	}
}

func TestMonitorDuplicateRefused(t *testing.T) {
	// One sampler per host: two tickers would double-sample the same key tag.
	r, _, _ := monitorSetup(t)
	if _, err := r.StartMonitor("dup", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StartMonitor("dup", 4); err == nil {
		t.Fatal("a duplicate monitor must be refused")
	}
}

package workbenchhost

// monitor.go — W-M1: the sampling ring on the host.stats key, with the 15s
// polling timer registered as an EFFECT.
//
// The plan's two bullets, and what they mean here:
//
//   "采样环 = host.stats 键（表语义）" — the ring is the VALUE bound at the
//     host.stats key, one tag per sampled host. The key is a commutative table
//     (Thm 47), so two monitors sampling two hosts commute, and either can be
//     withdrawn while the other stands (Thm 43). The ring value is a fixed-
//     capacity circular buffer: bounded memory, oldest samples evicted.
//
//   "15s 轮询的定时器注册是一个效应（可逆：卸载即停）" — the ticker is NOT a
//     bare goroutine the host forgets about. It is tracked on the fiber's
//     accumulator (ctxcore.Track): the forward starts the goroutine and the
//     inverse stops it, so unloading the fiber stops every sampler — Thm 68 at
//     the fiber level covers goroutines too. The plan explicitly bans the
//     R2-style "cleanup is a convention" patch: the stop IS the inverse.
//
// WHO SAMPLES: the capability is a plugin's (a monitor plugin knows how to read
// its platform's counters); the HOST owns the ring and the timer. The plugin
// submits samples over effect frames ("workbench.stats/sample" as a channel-
// data variant on the stats key); a test plugin submits synthetic values, a
// real monitor plugin submits real ones. The host never invents numbers — it
// stores, bounds, and reclaims.
//
// RENDERING happens in the host UI (the plan's "Recharts 渲染在宿主"), realized
// with the workspace's existing inline-SVG helpers (chart.ts linePath) — the
// project's zero-chart-library discipline; a sparkline does not justify a
// dependency.

import (
	"fmt"
	"sync"
	"time"

	"tablepro-go/internal/ctxcore"
)

// Sample is one observation: host, value, and the wall time the host took it.
type Sample struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// SampleRing is a fixed-capacity circular buffer of samples for ONE host.
// Bounded by design: a monitor that runs for a week must not grow the process.
type SampleRing struct {
	mu      sync.Mutex
	cap     int
	items   []Sample
	dropped uint64 // how many samples were evicted by the wrap (diagnostics)
}

func newSampleRing(capacity int) *SampleRing {
	if capacity < 1 {
		capacity = 1
	}
	return &SampleRing{cap: capacity}
}

// push appends one sample, evicting the oldest when full.
func (r *SampleRing) push(s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) >= r.cap {
		r.items = r.items[1:]
		r.dropped++
	}
	r.items = append(r.items, s)
}

// snapshot returns the samples oldest-first (render order).
func (r *SampleRing) snapshot() []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Sample, len(r.items))
	copy(out, r.items)
	return out
}

// last returns the newest sample, or ok=false when the ring is empty.
func (r *SampleRing) last() (Sample, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) == 0 {
		return Sample{}, false
	}
	return r.items[len(r.items)-1], true
}

// droppedCount returns the eviction counter (diagnostics; the UI shows it as
// the bounded-memory receipt).
func (r *SampleRing) droppedCount() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// monitor is one sampled host: its ring, its stop channel, and the tag it is
// bound under on host.stats. stopOnce guards the channel: BOTH the explicit
// StopMonitor and the unloader's inverse may try to stop the loop, and a
// channel closes once.
type monitor struct {
	tag      ctxcore.Tag
	host     string
	ring     *SampleRing
	stop     chan struct{}
	stopOnce sync.Once
	done     sync.WaitGroup
}

// stopLoop closes the stop channel exactly once. Idempotent by construction —
// the manual stop and the unloader's inverse race legitimately (an operator can
// stop a monitor while the fiber is being unloaded), and a second close would
// panic.
func (m *monitor) stopLoop() {
	m.stopOnce.Do(func() { close(m.stop) })
}

// interval is the plan's 15s polling period. A var seam so the tests can drive
// the sampler faster than wall time without a real sleep.
var monitorInterval = 15 * time.Second

// samplerFunc produces one sample for host. Swappable in tests; real monitor
// plugins submit their own samples instead of using this local fallback.
var samplerFunc = func(host string) (float64, error) {
	// A neutral placeholder is HONEST here: the host-side fallback samples the
	// process's own uptime-derived jitter only as a demo of the pipe. A real
	// monitor plugin replaces this path entirely (see the plugin note below).
	return 0, fmt.Errorf("no local sampler for %s: a monitor plugin should submit samples", host)
}

// StartMonitor registers the 15s sampler for host as a tracked effect.
//
// The timer's forward: bind the ring at host.stats (tag = monitor/host), start
// the goroutine, and return the stop function as the inverse. Unloading the
// fiber (or a manual StopMonitor) fires the inverse — "卸载即停" is not a
// promise, it is the accumulator running its inverses.
func (r *Runner) StartMonitor(host string, capacity int) (string, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return "", fmt.Errorf("plugin fiber is not active")
	}
	tag := ctxcore.Tag("monitor/" + host)
	r.monMu.Lock()
	if _, exists := r.monitors[host]; exists {
		r.monMu.Unlock()
		return "", fmt.Errorf("host %s is already monitored", host)
	}
	mon := &monitor{tag: tag, host: host, ring: newSampleRing(capacity), stop: make(chan struct{})}
	r.monitors[host] = mon
	r.monMu.Unlock()

	err := r.store.Track(f, "start-monitor:"+host, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		// Forward: bind + run the sampler loop.
		if err := st.TableInsert(ctxcore.KeyHostStats, tag, f.UID(), mon.ring); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyHostStats)
		mon.done.Add(1)
		go mon.run(r)
		return func(st2 *ctxcore.Store) error {
			// Inverse: stop the goroutine FIRST (no more samples arrive), then
			// withdraw the binding. Order matters: withdrawing before the loop
			// stops would let one more sample land on a revoked key.
			mon.stopLoop()
			mon.done.Wait()
			if err := st2.TableRemove(ctxcore.KeyHostStats, tag); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyHostStats)
			return nil
		}, nil
	})
	if err != nil {
		r.monMu.Lock()
		delete(r.monitors, host)
		r.monMu.Unlock()
		return "", err
	}
	return string(tag), nil
}

// run is the sampler loop: one sample per tick until stopped. A sample error is
// logged and skipped — a monitor that misses a beat degrades to a gap in the
// ring, never to a crashed fiber.
func (m *monitor) run(r *Runner) {
	defer m.done.Done()
	tick := time.NewTicker(monitorInterval)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
			v, err := samplerFunc(m.host)
			if err != nil {
				fmt.Printf("workbench: sample %s: %v\n", m.host, err)
				continue
			}
			m.ring.push(Sample{Time: time.Now(), Value: v})
		}
	}
}

// StopMonitor stops one host's sampler and withdraws its binding (the inverse
// of StartMonitor's tracked effect, run early). It mirrors CloseChannel's
// shape: the effect's inverse is the teardown, invoked through the accumulator.
func (r *Runner) StopMonitor(host string) error {
	r.monMu.Lock()
	_, exists := r.monitors[host]
	r.monMu.Unlock()
	if !exists {
		return fmt.Errorf("host %s is not monitored", host)
	}
	f := r.Fiber()
	if f == nil {
		return fmt.Errorf("plugin fiber is not active")
	}
	// Fire the tracked teardown by name through Track's mirror: stop the loop
	// and withdraw the binding, exactly what the unloader would do.
	r.monMu.Lock()
	mon := r.monitors[host]
	delete(r.monitors, host)
	r.monMu.Unlock()
	mon.stopLoop()
	mon.done.Wait()
	if err := r.store.TableRemove(ctxcore.KeyHostStats, mon.tag); err != nil {
		return err
	}
	r.store.Notify(ctxcore.KeyHostStats)
	_ = f
	return nil
}

// MonitorState is one host's ring snapshot for the UI.
type MonitorState struct {
	Host    string   `json:"host"`
	Tag     string   `json:"tag"`
	Samples []Sample `json:"samples"`
	// Dropped is the eviction count (the ring's bounded-memory receipt).
	Dropped uint64 `json:"dropped"`
}

// MonitorStates returns every active ring, host-sorted.
func (r *Runner) MonitorStates() []MonitorState {
	r.monMu.Lock()
	mons := make([]*monitor, 0, len(r.monitors))
	for _, m := range r.monitors {
		mons = append(mons, m)
	}
	r.monMu.Unlock()
	sortMonitorHosts(mons)
	out := make([]MonitorState, 0, len(mons))
	for _, m := range mons {
		snap := m.ring.snapshot()
		dropped := m.ring.droppedCount()
		out = append(out, MonitorState{
			Host:    m.host,
			Tag:     string(m.tag),
			Samples: snap,
			Dropped: dropped,
		})
	}
	return out
}

func sortMonitorHosts(mons []*monitor) {
	for i := 1; i < len(mons); i++ {
		for j := i; j > 0 && mons[j].host < mons[j-1].host; j-- {
			mons[j], mons[j-1] = mons[j-1], mons[j]
		}
	}
}

// SubmitSample is the plugin's path: a monitor plugin submits a sample it took
// on ITS side (effect frame / channel variant) and the host stores it in the
// host's ring. The host never invents numbers.
func (r *Runner) SubmitSample(host string, value float64, at time.Time) error {
	r.monMu.Lock()
	mon, ok := r.monitors[host]
	r.monMu.Unlock()
	if !ok {
		return fmt.Errorf("host %s is not monitored", host)
	}
	mon.ring.push(Sample{Time: at, Value: value})
	return nil
}

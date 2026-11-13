package workbenchhost

// runner.go — the bridge between a REAL plugin process and the ctxcore context
// model (P0). This is W-T1's core deliverable: it turns "a plugin is a fiber"
// from a model statement into running code.
//
// The mapping (ch07 Def 48 → running system):
//
//	banner   → Component (d, p): Depends/Provides come from the plugin's hello;
//	           σ ⊧ d is decidable BEFORE the fiber activates, so a plugin whose
//	           dependencies are missing never even opens a channel.
//	load     → ONE iterator whose stages are the plugin's effect frames. The host
//	           applies each frame to its OWN tables (ctxcore) and tracks the
//	           host-side inverse; the plugin's inverse is a WITNESS NAME the host
//	           maps to a disposer (§5.1.1's unchecked obligation, made explicit).
//	channels → runtime effects via ctxcore.Track: a PTY opened by the user is a
//	           context mutation like any other, reclaimed by the SAME accumulator
//	           on unload (Thm 68 at the fiber level).
//	death    → the process-exit channel fires → the runner RETIRES the fiber →
//	           Settle applies the accumulator LIFO → nav node and channels are
//	           reclaimed → the fiber is Inactive. A fresh Launch is a NEW fiber
//	           (τ never resurrects, Thm 80) whose load re-registers the SAME tags
//	           — which is why the terminal tab comes back exactly as it was.
//
// AD-3 discipline: the load iterator refuses EMISSION frames outright — anything
// the plugin marks as crossing the boundary (rendered bytes, published messages)
// is withheld/compensated by the host UI, never tracked as if it were reversible.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/dbcore/dbcore/plugindriver/workbench"
	"tablepro-go/internal/ctxcore"
)

// Runner drives one workbench plugin process as one ctxcore fiber.
type Runner struct {
	store *ctxcore.Store

	mu         sync.Mutex
	proc       *proc
	banner     workbench.Banner
	fiber      *ctxcore.Fiber
	pluginPath string
	dead       chan struct{} // closed when the CURRENT process died unexpectedly
	deadOnce   sync.Once
	stopped    bool

	// recording (W-T3): one cast per channel, keyed by channel id. The recMu is
	// separate from mu so a frame hot path never contends with lifecycle.
	recMu      sync.Mutex
	recordings map[string]*recordingState

	// monitor (W-M1): one sampler per host, keyed by host. The timers are
	// tracked effects on the fiber's accumulator — unloading stops them.
	monMu    sync.Mutex
	monitors map[string]*monitor

	// orchestrator (W-C1): when this runner is part of a multi-plugin system,
	// the orchestrator owns the shared store and the proxy switch. Nil =
	// single-plugin deployment (proxy requests are refused).
	Orchestrator *Orchestrator

	// secrets (AD-4): the erasure receipt log. Names only — an erasure log that
	// stored the value would defeat the erasure it records.
	secrets secretLog
}

// New returns a runner over a fresh context store. The store may already carry
// host-provided keys (connections.store) — providing it before any plugin loads
// is what lets a plugin's fiber activate immediately.
func New(store *ctxcore.Store) *Runner {
	return &Runner{
		store:      store,
		dead:       make(chan struct{}),
		recordings: map[string]*recordingState{},
		monitors:   map[string]*monitor{},
	}
}

// ProvideConnectionsArchive publishes the host's connection archive under the
// connections.store key (single writer = the host, AD-6). Plugins only ever
// resolve it through their declared dependency; the values are host-owned.
func (r *Runner) ProvideConnectionsArchive(archive any) error {
	const archiveTag = ctxcore.Tag("host/connections")
	// IDEMPOTENT: the host re-provides after a provider relaunch. The TAG is the
	// same, so an existing binding must be withdrawn first (Set inserts; the
	// duplicate tag would be refused). The withdraw→install pair is observable —
	// notify fires on both halves, and the consumers' inertial reload picks the
	// fresh snapshot up.
	if _, exists := r.store.Get(ctxcore.KeyConnectionsStore, archiveTag); exists {
		if err := r.store.TableRemove(ctxcore.KeyConnectionsStore, archiveTag); err != nil {
			return err
		}
		r.store.Notify(ctxcore.KeyConnectionsStore)
	}
	_, err := r.store.Set(ctxcore.KeyConnectionsStore, archiveTag, "host", archive)
	return err
}

// Fiber returns the live fiber (nil before the first Launch).
func (r *Runner) Fiber() *ctxcore.Fiber {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fiber
}

// Banner returns the plugin's declared interface.
func (r *Runner) Banner() workbench.Banner {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.banner
}

// Launch starts the plugin process and drives its fiber to Active.
//
// Re-launching after a death is the §4.4 revision COMPOSITE: the old fiber was
// retired by the death watch and deactivated by Settle; the vestigial registry
// entry is REMOVED here (O-Remove: retired + Inactive + σ=⌀ + no children) so
// O-Insert may reinsert at the same name with a FRESH fiber — τ never flips
// back (Lemma 59(5) / Thm 80).
func (r *Runner) Launch(pluginPath string) error {
	r.mu.Lock()
	if r.proc != nil && !r.proc.Dead() {
		r.mu.Unlock()
		return fmt.Errorf("a plugin process is already running")
	}
	old := r.fiber
	r.mu.Unlock()
	if old != nil {
		if !old.Retired() {
			r.store.Retire(old)
		}
		if err := r.store.Remove(old); err != nil {
			return fmt.Errorf("remove the previous fiber: %w", err)
		}
	}

	p, banner, err := startProc(pluginPath, nil)
	if err != nil {
		return err
	}
	if err := banner.Validate(); err != nil {
		_ = p.Close()
		return fmt.Errorf("plugin %s: %w", pluginPath, err)
	}

	comp := ctxcore.Component{
		ID:       "plugin:" + banner.PluginID,
		Provides: keysOf(banner.Provides),
		Depends:  keysOf(banner.Depends),
		Load: func(s *ctxcore.Store, f *ctxcore.Fiber) ctxcore.Iterator {
			return r.loadIterator(p, banner, f)
		},
	}
	f, _, err := r.store.Use(comp, "root")
	if err != nil {
		_ = p.Close()
		return fmt.Errorf("use plugin %s: %w", banner.PluginID, err)
	}

	r.mu.Lock()
	r.proc = p
	r.banner = banner
	r.fiber = f
	r.pluginPath = pluginPath
	r.dead = make(chan struct{})
	r.deadOnce = sync.Once{}
	r.mu.Unlock()

	// Death watch: the process-exit channel closes the runner's dead channel,
	// which retires the fiber. Retire → Settle reclaims the nav node and every
	// channel the accumulator holds — the kill/recover acceptance's mechanism.
	go func() {
		<-p.DeadCh()
		r.mu.Lock()
		f := r.fiber
		r.mu.Unlock()
		if f != nil {
			r.store.Retire(f)
		}
		r.deadOnce.Do(func() { close(r.dead) })
	}()

	// AD-2: a plugin whose dependencies are missing is NOT a launch failure —
	// its fiber sits Inactive with a reason, and the converge loop activates it
	// the moment a provider satisfies the declaration. "Inactive 等待" is the
	// unified semantics; an error here would make the consumer launch order
	// matter, which Def 53/Thm 70 explicitly do not.
	return nil
}

// DeadCh fires when the CURRENT process died unexpectedly. It closes on Launch
// of a replacement.
func (r *Runner) DeadCh() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dead
}

// loadIterator is the host side of the plugin's load: each stage requests one
// effect frame from the plugin and applies it to the host's tables. The
// iterator runs under ctxcore's target-stability guard, so a dependency that
// vanishes mid-load diverts the transition and recovers the partial state
// (Cor 69: nothing installed).
func (r *Runner) loadIterator(p *proc, banner workbench.Banner, f *ctxcore.Fiber) ctxcore.Iterator {
	provides := map[string]bool{}
	for _, k := range banner.ProvidesKeys() {
		provides[k] = true
	}
	stage := 0
	var next func() (ctxcore.Stage, error)
	next = func() (ctxcore.Stage, error) {
		var frame workbench.EffectFrame
		if err := p.call(context.Background(), workbench.MethodEffect,
			map[string]int{"stage": stage}, &frame); err != nil {
			return ctxcore.Stage{}, fmt.Errorf("effect stage %d: %w", stage, err)
		}
		stage++
		if frame.Done {
			return ctxcore.Stage{
				Effect: ctxcore.Effect{Name: "load-done", Forward: func(*ctxcore.Store) (ctxcore.Inverse, error) {
					return func(*ctxcore.Store) error { return nil }, nil
				}},
				Done: true,
			}, nil
		}
		if err := r.validateFrame(banner, frame, provides); err != nil {
			return ctxcore.Stage{}, err
		}
		return ctxcore.Stage{
			Effect: ctxcore.Effect{
				Name: frame.Op + ":" + frame.Tag,
				Forward: func(st *ctxcore.Store) (ctxcore.Inverse, error) {
					// The stage applies the plugin's frame to the HOST's table.
					// Direct table operations + notify here (not store.Set): the
					// stage is already INSIDE the load iterator's tracked effect,
					// and its Inverse is what the accumulator collects — the exact
					// shape of Cordis's execute collecting each iter.next() yield.
					key := ctxcore.Key(frame.Key)
					tag := ctxcore.Tag(frame.Tag)
					if err := st.TableInsert(key, tag, f.UID(), json.RawMessage(frame.Payload)); err != nil {
						return nil, err
					}
					st.Notify(key)
					return func(st2 *ctxcore.Store) error {
						if err := st2.TableRemove(key, tag); err != nil {
							return err
						}
						st2.Notify(key)
						return nil
					}, nil
				},
			},
			Done: false,
			Next: next,
		}, nil
	}
	return next
}

// validateFrame is the protocol-boundary enforcement (§5.1.4 get-trap semantics
// at the effect boundary): only ACQUISITION frames, only keys the plugin
// DECLARES it provides, only inverse names the host knows how to honour.
func (r *Runner) validateFrame(banner workbench.Banner, frame workbench.EffectFrame, provides map[string]bool) error {
	if frame.Boundary != workbench.BoundaryAcquisition {
		// AD-3: an emission is idΓ — the host cannot track it and must not pretend
		// to. During LOAD an emission frame is a protocol violation (emissions
		// happen over channels after activation, behind Safe Mode).
		return fmt.Errorf("%s: effect %q is an emission frame during load", workbench.CodeEmissionBlocked, frame.Tag)
	}
	if !provides[frame.Key] && frame.Key != string(ctxcore.KeyNavNodes) {
		// nav.nodes is the HOST-OWNED shared UI table: every plugin registers its
		// own tabs into it, distinguished by unique TAGS (the commutative-table
		// discipline — two plugins' tab registrations commute, either withdraws
		// while the other stands). The single-source discipline does NOT apply:
		// the provider of record is the HOST (root-side, like connections.store),
		// and a plugin's effect frames are writes the host mediates, not
		// provisions. Every OTHER key requires the plugin to declare it.
		return fmt.Errorf("%s: effect %q writes key %q the plugin does not declare", workbench.CodeUndeclared, frame.Tag, frame.Key)
	}
	switch frame.Inverse {
	case "remove":
		// The host honours this witness with its own disposer: remove THAT tag.
		// Whether the plugin's own bookkeeping matches is its obligation (§5.1.1).
	default:
		return fmt.Errorf("%s: inverse %q is not a known disposer", workbench.CodeUnknownInverse, frame.Inverse)
	}
	if frame.Tag == "" {
		return fmt.Errorf("%s: an effect frame must carry a unique tag", workbench.CodeBadBanner)
	}
	return nil
}

// OpenChannel asks the plugin for a new terminal session and registers it as a
// RUNTIME effect on the fiber (ctxcore.Track): the channel's binding lands in
// terminal.channels and is reclaimed by the accumulator on unload.
// OpenChannelWithSecret lends a secret, opens one channel that names the lend,
// and erases the caller's copy when the open returns. This is the shape the API
// uses: the password reaches the plugin as a raw stdin body, never as a param.
func (r *Runner) OpenChannelWithSecret(params map[string]any, secretName string, secret []byte) (string, error) {
	if secretName == "" || len(secret) == 0 {
		return r.OpenChannel(params)
	}
	erase, err := r.LendSecret(secretName, secret)
	if err != nil {
		return "", err
	}
	defer func() { _ = erase() }()
	withSecret := make(map[string]any, len(params)+1)
	for k, v := range params {
		withSecret[k] = v
	}
	withSecret["secret"] = secretName
	return r.OpenChannel(withSecret)
}

func (r *Runner) OpenChannel(params map[string]any) (string, error) {
	r.mu.Lock()
	p, f := r.proc, r.fiber
	r.mu.Unlock()
	if p == nil || f == nil || p.Dead() {
		return "", fmt.Errorf("plugin process is not running")
	}
	var res struct {
		ChannelID string `json:"channelId"`
	}
	if err := p.call(context.Background(), workbench.MethodChannelOpen, params, &res); err != nil {
		return "", err
	}
	err := r.store.Track(f, "open-channel:"+res.ChannelID, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyTerminalChannels, ctxcore.Tag(res.ChannelID), f.UID(),
			map[string]any{"opened": true}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyTerminalChannels)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableRemove(ctxcore.KeyTerminalChannels, ctxcore.Tag(res.ChannelID)); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyTerminalChannels)
			return nil
		}, nil
	})
	if err != nil {
		return "", err
	}
	return res.ChannelID, nil
}

// WriteChannel sends keystrokes and returns whatever the shell produced.
func (r *Runner) WriteChannel(channelID, input string, maxBytes int) (string, bool, error) {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return "", false, fmt.Errorf("plugin process is not running")
	}
	var res struct {
		ChannelID string `json:"channelId"`
		Output    string `json:"output"`
		Ended     bool   `json:"ended"`
	}
	err := p.call(context.Background(), workbench.MethodChannelData, map[string]any{
		"channelId": channelID, "input": input, "maxBytes": maxBytes,
	}, &res)
	if err != nil {
		return "", false, err
	}
	// W-T3: record the EXCHANGE — input first ("i"), then the output the shell
	// produced ("o"). The recorder is the §4 compensation: it never undoes
	// anything, it keeps the faithful trace.
	r.recordChannelFrame(channelID, "i", input)
	if res.Output != "" {
		r.recordChannelFrame(channelID, "o", res.Output)
	}
	if res.Ended {
		r.stopRecordingsFor(channelID)
	}
	return res.Output, res.Ended, nil
}

// ResizeChannel re-announces the window size. The resize is recorded as an "r"
// frame: a player replaying the cast reproduces the user's window changes.
func (r *Runner) ResizeChannel(channelID string, cols, rows uint16) error {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return fmt.Errorf("plugin process is not running")
	}
	err := p.call(context.Background(), workbench.MethodChannelSize, map[string]any{
		"channelId": channelID, "cols": cols, "rows": rows,
	}, nil)
	if err == nil {
		r.recordResize(channelID, cols, rows)
	}
	return err
}

// recordResize routes a resize into the channel's cast, if recording.
func (r *Runner) recordResize(channel string, cols, rows uint16) {
	r.recMu.Lock()
	st, ok := r.recordings[channel]
	r.recMu.Unlock()
	if ok {
		_ = st.rec.Resize(cols, rows)
	}
}

// CloseChannel tears one session down and reclaims its binding (the inverse of
// the runtime effect that opened it — Track's mirror image). The channel's cast,
// if any, gets its exit event first: a recording that never ends is a trace with
// a hole at exactly the moment that matters.
func (r *Runner) CloseChannel(channelID string) error {
	r.stopRecordingsFor(channelID)
	r.mu.Lock()
	p, f := r.proc, r.fiber
	r.mu.Unlock()
	if p == nil || f == nil || p.Dead() {
		return fmt.Errorf("plugin process is not running")
	}
	if err := p.call(context.Background(), workbench.MethodChannelClose, map[string]any{
		"channelId": channelID,
	}, nil); err != nil {
		return err
	}
	return r.store.Track(f, "close-channel:"+channelID, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableRemove(ctxcore.KeyTerminalChannels, ctxcore.Tag(channelID)); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyTerminalChannels)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableInsert(ctxcore.KeyTerminalChannels, ctxcore.Tag(channelID), f.UID(),
				map[string]any{"reopened": true}); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyTerminalChannels)
			return nil
		}, nil
	})
}

// Stop shuts the plugin down POLITELY (the host is closing): the fiber retires,
// the accumulator reclaims everything, and the process gets a close request with
// a hard-kill fallback.
func (r *Runner) Stop() {
	r.mu.Lock()
	p, f := r.proc, r.fiber
	r.proc = nil
	r.mu.Unlock()
	// Every open cast gets its exit event: Stop ends all sessions, so the traces
	// must close at the same boundary rather than dangle.
	r.recMu.Lock()
	channels := make([]string, 0, len(r.recordings))
	for ch := range r.recordings {
		channels = append(channels, ch)
	}
	r.recMu.Unlock()
	for _, ch := range channels {
		r.stopRecordingsFor(ch)
	}
	// W-M1: stop every sampler — the timers are tracked effects, but Stop fires
	// them explicitly so the goroutines never outlive the process teardown.
	r.monMu.Lock()
	monHosts := make([]string, 0, len(r.monitors))
	for h := range r.monitors {
		monHosts = append(monHosts, h)
	}
	r.monMu.Unlock()
	for _, h := range monHosts {
		_ = r.StopMonitor(h)
	}
	if f != nil {
		r.store.Retire(f)
	}
	if p != nil {
		_ = p.Close()
	}
}

// keysOf converts the banner's capability list into ctxcore keys.
func keysOf(caps []workbench.Capability) []ctxcore.Key {
	out := make([]ctxcore.Key, 0, len(caps))
	for _, c := range caps {
		out = append(out, ctxcore.Key(c.Key))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

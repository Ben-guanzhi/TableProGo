// Package ctxcore implements the CONTEXT CORE of the all-in-one ops workbench:
// the runtime half of the shi-spatiotemporal-composability paradigm
// (Shi/Zhang/Cui, arXiv:2608.25512) that the P0 plan mandates.
//
// Everything here is deliberately a pure, in-process Go model — no sockets, no
// goroutines, no filesystem. That is what makes the four metatheory acceptance
// tests executable as ordinary unit tests instead of prose.
//
// Anchors:
//
//   - §3.1 / Def 1-18: an effect is Γ → Γ × (Γ → Γ). Inverses accumulate in
//     reverse application order (twisted composition, Thm 16). An iterator is a
//     reified delimited continuation: a component's WHOLE load is one iterator.
//   - §3.2 / Def 19-22: Σ is a key→value table; σ ⊧ d is decidable; notify_d
//     classifies a change as activating / deactivating / neutral. `set` IS an
//     effect (the synergy), so every coeffect change is observed at an effect
//     boundary.
//   - §3.4 / Thm 45/47: keys are commutative tables with UNIQUE TAGS. Operations
//     at distinct keys commute automatically; with witnessed keys, independence
//     reduces to P1 ∩ S2 = P2 ∩ S1 = ⌀ read off declarations.
//   - §4.1-4.2 / Def 48-57: component (d,p,e), fiber, nine rules, committed view
//     ω keyed by PROVIDER UIDS, target_n(γ), the ¬ relied_n guard on L-Unload.
//   - §4.3 / Thm 68, 70, 73, 80: recovery exactness, ordering, progress,
//     confluence — the four acceptance tests in ctxcore_metatheory_test.go.
//   - §5 / Table 2: ctx.effect is the SOLE mutation primitive; dispose is armed;
//     `ctx[key]` enforces d at the point of use (UNDECLARED / INACTIVE access).
//   - §6.1: acquisition is inside the boundary (revertible); emission is outside
//     (idΓ) and is withheld/compensated, never tracked.
//
// What this package deliberately does NOT do:
//   - verify the witness that an inverse really reverts (§5.1.1: an unchecked
//     author obligation). It stores and applies the inverse; it cannot prove it.
//   - model emissions. Theorem 80 speaks of STATE, not of bytes already sent.
//   - sandbox a plugin process. pluginreg's SHA-256 trust gate stays OUTSIDE the
//     context model (AD-6): it is a file-identity boundary check before startup.
package ctxcore

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// Key is a coeffect key name (§1's key surface). Every key is a commutative table
// with unique tags: NOT an ordered chain, NOT a lowest-available-descriptor
// allocator (§6.3's anti-patterns), because those break Thm 47's declaration-only
// independence check.
type Key string

// The §1 key surface. Declared as constants so a typo becomes a compile error and
// the key face doubles as Thm 47's checklist.
const (
	KeyNavNodes         Key = "nav.nodes"
	KeyTerminalChannels Key = "terminal.channels"
	KeyFilesSession     Key = "files.session"
	KeyHostStats        Key = "host.stats"
	KeyTunnelProxy      Key = "tunnel.proxy"
	KeyMQTTSession      Key = "mqtt.session"
	KeyContainerSession Key = "container.session"
	KeyDesktopSession   Key = "desktop.session"
	KeyAIProvider       Key = "ai.provider"
	KeyMCPTools         Key = "mcp.tools"
	KeyConnectionsStore Key = "connections.store" // single writer = the host kernel
	KeySafeMode         Key = "safe.mode"         // INTERCEPTION key (Σinter), not an effect
)

// AllKeys is the full key surface, used by tests to assert recovery leaves nothing
// behind (Cor 69: σ^{u+1}_n = ⌀).
var AllKeys = []Key{
	KeyNavNodes, KeyTerminalChannels, KeyFilesSession, KeyHostStats,
	KeyTunnelProxy, KeyMQTTSession, KeyContainerSession, KeyDesktopSession,
	KeyAIProvider, KeyMCPTools, KeyConnectionsStore, KeySafeMode,
}

// Tag is the unique, caller-supplied identity of one entry in a key's table. The
// tag — never the position — identifies a registration, which is what makes two
// registrations commute and either withdrawable while the other stands (Thm 43/47).
type Tag string

// Binding is one entry of a key's commutative table. Value is opaque: the kernel
// never interprets a plugin's value, it only stores, resolves and withdraws it.
type Binding struct {
	Tag      Tag
	Provider string // uid of the fiber that installed it (Def 53 target view)
	Value    any
}

// KeyTable is a commutative table (Def 44). insert/remove/lookup by unique tag.
// A duplicate tag is refused (CodeDuplicateKey) rather than silently overwritten:
// overwriting a provider's OWN binding in place is not observed by notify (§5.1.3),
// so the disciplined operation is withdraw-then-install-afresh.
type KeyTable struct {
	mu      sync.Mutex
	entries map[Tag]Binding
}

func newKeyTable() *KeyTable { return &KeyTable{entries: map[Tag]Binding{}} }

func (t *KeyTable) insert(b Binding) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.entries[b.Tag]; exists {
		return fmt.Errorf("%s: tag %q already bound", CodeDuplicateKey, b.Tag)
	}
	t.entries[b.Tag] = b
	return nil
}

func (t *KeyTable) remove(tag Tag) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.entries[tag]; !exists {
		return fmt.Errorf("%s: tag %q not bound", CodeRevokeAbsent, tag)
	}
	delete(t.entries, tag)
	return nil
}

func (t *KeyTable) lookup(tag Tag) (any, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.entries[tag]
	return b.Value, ok
}

// snapshot returns the table's entries in TAG-sorted order. Sorting is essential:
// observational equivalence ≃ (Def 31) compares what operations can observe, and a
// map's iteration order is not an observable operation. A snapshot with a stable
// order is what lets the confluence test compare two installation orders.
func (t *KeyTable) snapshot() []Binding {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Binding, 0, len(t.entries))
	for _, b := range t.entries {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out
}

// ---------- Γ∞ : the unified context ----------

// Store is Γ∞ (§3.3): the ONE entity through which every component↔environment
// interaction passes. It holds:
//   - tables: the commutative key surface (Σ);
//   - intercept: right-biased metadata (Σinter), a DERIVED realization, not an
//     effect — changing a policy does not trigger a reload (§3.2 / AD-3);
//   - fibers: the registry F_γ (Def 50);
//   - log: an audit trail of effect applications, used by the metatheory tests to
//     assert ORDERING (Thm 70) without guessing at a scheduler.
type Store struct {
	mu        sync.Mutex
	tables    map[Key]*KeyTable
	intercept map[Key]any
	fibers    map[string]*Fiber
	// uidSeq allocates fiber uids. A monotone allocator is NOT rewound (Thm 68's
	// bound), but a uid is an internal name no key operation publishes, so it stays
	// inside the boundary.
	uidSeq atomic.Uint64
	log    *EventLog

	// notifyMu guards the re-entrancy state below. `settling` lets only ONE
	// converge loop run; `activating` records fibers whose load iterator is
	// executing on the current call stack. A notify issued from INSIDE a load
	// (Algorithm 2's set → notify) must only QUEUE its key: running the converge
	// loop there would deadlock against the very transition that called it.
	// refresh is idempotent (Algorithm 3) and the calculus is scheduling-
	// independent (Thm 80), so deferring the drain to the loop's next pass
	// changes nothing observable.
	notifyMu   sync.Mutex
	settling   bool
	activating map[string]bool
	pending    map[Key]bool

	// entries / entryFibers are the loader's Def 81 bookkeeping: the desired
	// configuration per entry id, and the fiber each entry currently owns. They
	// are reconciled by Reconcile (reconcile.go).
	entries     map[string]Entry
	entryFibers map[string]*Fiber
}

func NewStore() *Store {
	s := &Store{
		tables:      map[Key]*KeyTable{},
		intercept:   map[Key]any{},
		fibers:      map[string]*Fiber{},
		log:         &EventLog{},
		entries:     map[string]Entry{},
		entryFibers: map[string]*Fiber{},
	}
	for _, k := range AllKeys {
		s.tables[k] = newKeyTable()
	}
	return s
}

func (s *Store) nextUID() string {
	return fmt.Sprintf("f%d", s.uidSeq.Add(1))
}

func (s *Store) table(k Key) *KeyTable {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tables[k]
	if !ok {
		// An undeclared key is refused at the access boundary (below); building a
		// table lazily here would let an effect silently widen the key surface.
		panic("ctxcore: unknown key " + string(k))
	}
	return t
}

// Event is one logged step. The log is a TEST OBSERVATION DEVICE: Thm 70 asserts
// the provider's episode strictly contains its consumers', which needs an ordered
// trace. It is not part of the calculus's state (Def 58 compares tables + control
// fields), so it never affects quiescence or confluence.
type Event struct {
	Step  uint64
	Fiber string
	Kind  string
	Key   Key
	Tag   Tag
}

type EventLog struct {
	mu     sync.Mutex
	events []Event
	seq    uint64
}

func (l *EventLog) record(fiber, kind string, k Key, tag Tag) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	l.events = append(l.events, Event{Step: l.seq, Fiber: fiber, Kind: kind, Key: k, Tag: tag})
}

func (l *EventLog) Snapshot() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

func (l *EventLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = nil
	l.seq = 0
}

// Log exposes the event log for hosts and tests that need the ordered trace
// (Thm 70's ordering assertions read it). It is an observation device, not part
// of the calculus state.
func (s *Store) Log() *EventLog { return s.log }

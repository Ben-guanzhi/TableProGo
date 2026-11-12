package ctxcore

// Fibers, the reactive notify classifier, the access proxy, and the nine-rule
// lifecycle. Anchors: ch04 (Def 19-22), ch07 (Def 48-57, Figure 1), ch09
// (Algorithms 3/5/6), ch08 (Thm 70/73).

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// FiberState is ΘΓ (Def 49): Inactive | Reloading | Active | Unloading.
// The two SETTLED states are Inactive and Active; the other two are in-flight.
// Separating decision (L-Leave/L-Divert) from act (L-Unload) is what gives a
// consumer an interval to tear down while still reading its provider's key.
type FiberState int

const (
	Inactive FiberState = iota
	Reloading
	Active
	Unloading
)

func (s FiberState) String() string {
	switch s {
	case Inactive:
		return "Inactive"
	case Reloading:
		return "Reloading"
	case Active:
		return "Active"
	case Unloading:
		return "Unloading"
	}
	return "?"
}

// Installed is installed_n(γ) ≔ θ_n ≠ Inactive (Def 49).
func (f *Fiber) Installed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state != Inactive
}

// Component is (d, p, e) of Def 48: declared dependencies, provided keys, and the
// witnessed effect iterator. e is a function of the store + this fiber so a load
// can register nav nodes, open channels, etc., all through ctx.effect.
type Component struct {
	ID       string
	Provides []Key
	Depends  []Key
	// Load is ℑ_{d∪p}Γ. It MUST be confined (Def 55): it may only write its own
	// table and declared keys in providers' tables. The proxy below enforces that.
	Load func(*Store, *Fiber) Iterator
}

// Fiber is Def 49's instantiation ⟨d,p,e,π,σ,τ,θ⟩, plus the runtime fields of
// Table 2: uid, committed view ω, target, dispose (accumulator g), inertia.
type Fiber struct {
	mu sync.Mutex

	uid       string
	parent    string // "root" or a parent uid
	comp      Component
	state     FiberState
	retired   bool // τ (Def 49): MONOTONE (Lemma 59(5)); never flipped back
	dispose   *Dispose
	committed map[Key]string // ω: declared key → provider uid (Def 53)
	target    map[Key]string // target_n(γ), ⊥ represented by nil
	Reason    string         // why Inactive (dependency missing / cycle / retired)
}

func (f *Fiber) UID() string { return f.uid }
func (f *Fiber) ID() string  { return f.comp.ID }

// View is a snapshot of the committed view, for tests and diagnostics.
func (f *Fiber) View() map[Key]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[Key]string{}
	for k, v := range f.committed {
		out[k] = v
	}
	return out
}

func (f *Fiber) State() FiberState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *Fiber) Retired() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retired
}

// ---------- σ_γ : the derived coeffect context ----------

// Sigma is σ_γ (eq. 46): the union over ACTIVE fibers ONLY. This single-source
// discipline is what makes a provider CEASE to provide before it withdraws its
// bindings — the one-step head start that lets the ¬ relied guard release without
// deadlock (ch07 worked example, Thm 73).
func (s *Store) Sigma() map[Key][]Binding {
	// Collect pointers under s.mu, release it, THEN read fiber state. Never nest
	// s.mu and f.mu: the lock order must be total or a target-view computation
	// (which holds f.mu) could deadlock against a table walk (which holds s.mu).
	s.mu.Lock()
	all := make([]*Fiber, 0, len(s.fibers))
	for _, f := range s.fibers {
		all = append(all, f)
	}
	s.mu.Unlock()
	active := map[string]bool{}
	for _, f := range all {
		if f.State() == Active {
			active[f.uid] = true
		}
	}
	out := map[Key][]Binding{}
	for _, k := range AllKeys {
		bindings := s.table(k).snapshot()
		var visible []Binding
		for _, b := range bindings {
			if active[b.Provider] {
				visible = append(visible, b)
			}
		}
		out[k] = visible
	}
	return out
}

// Satisfies is σ ⊧ d (eq. 22): ∀k ∈ d, k ∈ dom(σ). Decidable because the domain
// is finite. dom(σ_γ) is the set of keys an ACTIVE fiber provides — a provider that
// has activated but installed no entries yet still provides its key (totality on
// provision, Def 76, is a separate condition).
func (s *Store) Satisfies(depends []Key) bool {
	for _, k := range depends {
		if s.providerOf(k) == "" {
			return false
		}
	}
	return true
}

// providerOf returns the uid of the ACTIVE fiber providing k, or "" (single-source
// discipline: at most one, enforced by O-Insert).
// providerOf returns the uid of the ACTIVE fiber providing k, or "" (single-source
// discipline: at most one, enforced by O-Insert).
//
// Host-provided keys are the exception that makes the model complete: the host
// kernel binds connections.store (and any other host-owned key) itself, with the
// provider name "host". In the paper's terms the host is the ROOT-side provider —
// its bindings are always available because the host is always running.
func (s *Store) providerOf(k Key) string {
	s.mu.Lock()
	all := make([]*Fiber, 0, len(s.fibers))
	for _, f := range s.fibers {
		all = append(all, f)
	}
	s.mu.Unlock()
	for _, f := range all {
		if f.State() != Active {
			continue
		}
		for _, p := range f.comp.Provides {
			if p == k {
				return f.uid
			}
		}
	}
	// Fall back to host-owned bindings: any binding at k whose provider is the
	// host satisfies the dependency (the host never deactivates).
	for _, b := range s.table(k).snapshot() {
		if b.Provider == "host" {
			return "host"
		}
	}
	return ""
}

// TargetView is target_n(γ) (Def 53): ⊥ (nil) when retired or unsatisfied,
// otherwise each declared key mapped to its PROVIDER UID — never to a value.
// Recording a provider rather than a value is what makes the comparison usable:
// a different fiber providing an equal value would otherwise compare equal.
func (s *Store) TargetView(f *Fiber) map[Key]string {
	f.mu.Lock()
	retired := f.retired
	depends := append([]Key(nil), f.comp.Depends...)
	f.mu.Unlock()
	if retired || !s.Satisfies(depends) {
		return nil
	}
	out := map[Key]string{}
	for _, k := range depends {
		out[k] = s.providerOf(k)
	}
	return out
}

func sameView(a, b map[Key]string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// NotifyClass is Def 22's three-way classification.
type NotifyClass int

const (
	Neutral NotifyClass = iota
	Activating
	Deactivating
)

// Notify is notify_d(σ, σ′) (Def 22). It compares SATISFACTION before and after a
// change for one component's specification. An activating transition runs the
// component's tracked effects; a deactivating one applies its accumulator; a
// neutral change is harmless (refresh's idempotence, Algorithm 3).
func Notify(before, after bool) NotifyClass {
	switch {
	case !before && after:
		return Activating
	case before && !after:
		return Deactivating
	default:
		return Neutral
	}
}

// notify is the host's internal dispatch (Algorithm 3): a change at key k queues
// it and runs ONE converge loop. It is called at every effect boundary — the
// algebraic basis of reactivity (ch04: every coeffect change is observed).
//
// STRUCTURE (the lesson the first stress run taught): lifecycle steps must NOT
// recurse. beginLeave → notify → settle → refresh → beginLeave → drain → … is an
// interleaving where an inner drain can observe a half-refreshed world (a consumer
// still Active with a stale committed view) and wrongly declare a deadlock. The
// faithful shape is ONE converge loop that alternates
//
//	refresh every fiber declaring a queued key  →  apply L-Unload to the
//	Unloading fibers the ¬ relied guard frees
//
// until quiescent. refresh is idempotent (Algorithm 3) and the calculus is
// scheduling-independent (Thm 80), so the quiescent state does not depend on when
// a step runs — only that the loop keeps going until Thm 73's progress says it
// stops.
func (s *Store) notify(k Key) {
	s.notifyMu.Lock()
	if s.pending == nil {
		s.pending = map[Key]bool{}
	}
	s.pending[k] = true
	// A notify from INSIDE a load iterator (Algorithm 2's set → notify while a
	// fiber is Reloading on this call stack) must only queue: running the converge
	// loop here would deadlock against the transition that called it. The loop
	// picks queued keys up on its next pass (inertial, §4.4).
	blocked := s.settling || len(s.activating) > 0
	s.notifyMu.Unlock()
	if blocked {
		return
	}
	s.Settle()
}

// Settle drives EVERY lifecycle step to quiescence: refresh queued keys' fibers,
// then apply L-Unload under the ¬ relied guard, repeating until nothing is
// pending, nothing is Unloading, and every Active fiber's committed view matches
// its target. `settling` makes it re-entrant-safe: an effect that notifies during
// a converge loop only queues its key; the loop picks it up on the next pass.
func (s *Store) Settle() {
	s.notifyMu.Lock()
	if s.settling {
		s.notifyMu.Unlock()
		return // the running loop will pick up anything we queue
	}
	s.settling = true
	s.notifyMu.Unlock()
	defer func() {
		s.notifyMu.Lock()
		s.settling = false
		s.notifyMu.Unlock()
	}()

	for pass := 0; pass < maxSettlePasses; pass++ {
		// 1) refresh every fiber declaring a queued key.
		s.notifyMu.Lock()
		keys := make([]Key, 0, len(s.pending))
		for key := range s.pending {
			keys = append(keys, key)
		}
		s.pending = map[Key]bool{}
		s.notifyMu.Unlock()
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, key := range keys {
			s.refreshDeclaring(key)
		}

		// 2) apply L-Unload to every Unloading fiber the guard frees (one pass).
		unloaded := s.drainOnce()

		// 3) quiescence: nothing pending, nothing Unloading, every Active view
		// matches its target → the converge loop is DONE (Thm 73: quiescence).
		if s.Quiescent() {
			s.notifyMu.Lock()
			empty := len(s.pending) == 0
			s.notifyMu.Unlock()
			if empty {
				return
			}
		}

		// 4) No progress and nothing to do: a fiber is stuck in Reloading (its
		// guard keeps diverting?) or a view oscillates. A fiber whose load is
		// executing on THIS call stack is exempt — it finishes when the stack
		// unwinds into the loop's next pass.
		if !unloaded && len(keys) == 0 && s.activatingCount() == 0 {
			panic("ctxcore: converge loop stalled: " + s.registryDump())
		}
		// Quiescent but keys kept arriving from a concurrent writer; loop again.
	}
	panic("ctxcore: settle did not quiesce within the pass bound (Thm 73 progress violated)")
}

// activatingCount reports how many fibers have their load iterator on the current
// call stack. The converge loop must not judge them stalled: they complete when
// the stack unwinds into the loop's next pass.
func (s *Store) activatingCount() int {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	return len(s.activating)
}

// drainOnce applies L-Unload (the ONLY rule that applies an accumulator) to every
// Unloading fiber the ¬ relied_n guard frees, in one pass. Returns whether any
// unload happened. The guard orders deactivations ALONG COEFFECTS: a provider
// withdraws only after every dependent that resolved it has deactivated.
func (s *Store) drainOnce() bool {
	s.mu.Lock()
	var candidates []*Fiber
	for _, f := range s.fibers {
		if f.State() == Unloading {
			candidates = append(candidates, f)
		}
	}
	s.mu.Unlock()
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].uid < candidates[j].uid })
	progressed := false
	for _, f := range candidates {
		if s.Relied(f.uid) {
			continue // guard: a dependent has not deactivated yet
		}
		s.unload(f)
		progressed = true
	}
	return progressed
}

// registryDump renders the full registry for a stall diagnostic: every fiber's
// id, uid, state, retirement flag, relied status and committed view.
func (s *Store) registryDump() string {
	s.mu.Lock()
	names := make([]string, 0, len(s.fibers))
	for id := range s.fibers {
		names = append(names, id)
	}
	all := make([]*Fiber, 0, len(names))
	for _, id := range names {
		all = append(all, s.fibers[id])
	}
	s.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].uid < all[j].uid })
	var detail []string
	for _, c := range all {
		c.mu.Lock()
		state := c.state
		var committed string
		if c.committed == nil {
			committed = "⊥"
		} else {
			committed = fmt.Sprint(c.committed)
		}
		c.mu.Unlock()
		detail = append(detail, fmt.Sprintf("%s[%s] state=%s retired=%v relied=%v committed=%s target=%v",
			c.comp.ID, c.uid, state, c.Retired(), s.Relied(c.uid), committed, s.TargetView(c)))
	}
	return strings.Join(detail, "; ")
}

// quiescentLocked is Quiescent for a caller already holding s.mu (it reads fiber
// fields directly instead of going through TargetView, which acquires s.mu).
func (s *Store) quiescentLocked() bool {
	for _, f := range s.fibers {
		f.mu.Lock()
		state := f.state
		committed := f.committed
		retired := f.retired
		depends := append([]Key(nil), f.comp.Depends...)
		f.mu.Unlock()
		var target map[Key]string
		if !retired && s.satisfiesLocked(depends) {
			target = map[Key]string{}
			for _, k := range depends {
				target[k] = s.providerOfLocked(k)
			}
		}
		switch state {
		case Inactive:
			if target != nil {
				return false
			}
		case Active:
			if !sameView(target, committed) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// satisfiesLocked / providerOfLocked: σ ⊧ d and provider resolution for a caller
// already holding s.mu.
func (s *Store) satisfiesLocked(depends []Key) bool {
	for _, k := range depends {
		if s.providerOfLocked(k) == "" {
			return false
		}
	}
	return true
}

func (s *Store) providerOfLocked(k Key) string {
	for _, f := range s.fibers {
		f.mu.Lock()
		state := f.state
		provides := f.comp.Provides
		f.mu.Unlock()
		if state != Active {
			continue
		}
		for _, p := range provides {
			if p == k {
				return f.uid
			}
		}
	}
	return ""
}

const maxSettlePasses = 10000

// refreshDeclaring refreshes every fiber whose specification declares k, in a
// deterministic order (the calculus has no scheduler; a stable order only makes
// the event log readable).
func (s *Store) refreshDeclaring(k Key) {
	s.mu.Lock()
	var affected []*Fiber
	for _, f := range s.fibers {
		f.mu.Lock()
		declared := false
		for _, d := range f.comp.Depends {
			if d == k {
				declared = true
				break
			}
		}
		f.mu.Unlock()
		if declared {
			affected = append(affected, f)
		}
	}
	s.mu.Unlock()
	sort.Slice(affected, func(i, j int) bool { return affected[i].uid < affected[j].uid })
	for _, f := range affected {
		s.refresh(f)
	}
}

// Access resolves ctx[key] through the fiber's COMMITTED VIEW (Algorithm 6 /
// §5.1.4), enforcing the coeffect specification d AT THE POINT OF USE:
//
//   - key in committed view → return the bound value;
//   - key declared but not committed (fiber Inactive) → INACTIVE_ACCESS;
//   - key not declared at all → UNDECLARED_ACCESS.
//
// This is the protocol-boundary equivalent of Cordis's Proxy get-trap: a plugin
// requesting an undeclared key is REFUSED, never silently served. Bare Get never
// fails and does not enforce d; the plugin-facing path always goes through here.
func (s *Store) Access(f *Fiber, k Key, tag Tag) (any, error) {
	f.mu.Lock()
	declared := false
	for _, d := range f.comp.Depends {
		if d == k {
			declared = true
			break
		}
	}
	committed := f.state != Inactive && f.committed != nil
	var provider string
	if committed {
		provider = f.committed[k]
	}
	f.mu.Unlock()

	if !declared {
		return nil, fmt.Errorf("%s: fiber %s reads key %s it did not declare", CodeUndeclared, f.ID(), k)
	}
	if !committed {
		return nil, fmt.Errorf("%s: fiber %s is not active (key %s)", CodeInactive, f.ID(), k)
	}
	_ = provider // the committed view names the provider uid; resolution below
	v, ok := s.table(k).lookup(tag)
	if !ok {
		return nil, fmt.Errorf("%s: tag %q not bound at key %s", CodeRevokeAbsent, tag, k)
	}
	s.log.record(f.uid, "access", k, tag)
	return v, nil
}

// Get is the bare lookup (Algorithm 2): returns the bound value or nothing and
// NEVER fails. It does not enforce d. Only host-side code uses it; the plugin
// boundary uses Access.
func (s *Store) Get(k Key, tag Tag) (any, bool) {
	return s.table(k).lookup(tag)
}

// ---------- orchestration inputs (O-*) ----------

// Use is O-Insert (Def 50) + Def 52's instantiation: insert an INACTIVE fiber and
// return a Dispose whose inverse RETIRES it. Retire, not Remove — O-Remove carries
// premises that can fail (children still active), while O-Retire has only
// n ∈ dom(F_γ). The vestigial entry left behind is invisible to ≃_K (Lemma 62).
//
// Provision disjointness (∀m. p ∩ p_m = ⌀) is checked here: two fibers providing
// the same key in one realm would violate the single-source discipline.
func (s *Store) Use(comp Component, parent string) (*Fiber, *Dispose, error) {
	if comp.ID == "" {
		return nil, nil, fmt.Errorf("%s: component id is required", CodeBadBanner)
	}
	s.mu.Lock()
	if _, exists := s.fibers[comp.ID]; exists {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("%s: component %s already inserted", CodeDuplicateKey, comp.ID)
	}
	for _, f := range s.fibers {
		if f.Retired() {
			continue // a vestigial entry provides nothing
		}
		for _, a := range comp.Provides {
			for _, b := range f.comp.Provides {
				if a == b {
					s.mu.Unlock()
					return nil, nil, fmt.Errorf("%s: key %s already provided by %s", CodeDuplicateKey, a, f.ID())
				}
			}
		}
	}
	uid := s.nextUID()
	if parent == "" {
		parent = "root"
	}
	f := &Fiber{
		uid: uid, parent: parent, comp: comp, state: Inactive,
		Reason: "dependencies not yet satisfied",
	}
	s.fibers[comp.ID] = f
	s.log.record(uid, "insert", "", "")
	s.mu.Unlock()

	// The insertion itself is a tracked effect of the parent: unloading the parent
	// cascades to children (Def 52 / §5.1.3).
	d, err := s.Effect("use:"+comp.ID, func(st *Store) (Inverse, error) {
		return func(st2 *Store) error {
			st2.Retire(f)
			return nil
		}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	s.refresh(f)
	// Drive the new fiber (and any dependents it satisfies) to quiescence.
	s.Settle()
	return f, d, nil
}

// Retire is O-Retire: writes τ_n = ⊤. τ is MONOTONE — never flipped back (Lemma
// 59(5)). Re-enabling MUST use a fresh fiber (Def 53 reads no parent pointer and
// Thm 80 rests on a retired fiber staying retired).
//
// Idempotent against REMOVED fibers: a Use disposal's inverse retires its fiber,
// but a revision composite may already have O-Remove'd that entry before the
// dispose fires. Writing τ on a fiber that is no longer in the registry would
// resurrect state nobody observes; skip it instead.
func (s *Store) Retire(f *Fiber) {
	s.mu.Lock()
	live := s.fibers[f.comp.ID]
	s.mu.Unlock()
	if live != f {
		return // removed, or replaced by a fresh fiber under the same entry id
	}
	f.mu.Lock()
	f.retired = true
	f.mu.Unlock()
	s.log.record(f.uid, "retire", "", "")
	s.refresh(f)
	// The consumer's refresh above only QUEUES its providers' keys; the converge
	// loop below drives every lifecycle step to quiescence in ONE place.
	s.Settle()
}

// Remove is O-Remove: legal only when retired, Inactive, σ_n = ⌀, and no child.
// It collects what it needs under s.mu, releases it, then inspects tables —
// s.table(k) acquires s.mu, so holding it across that call would deadlock.
func (s *Store) Remove(f *Fiber) error {
	if !f.Retired() || f.State() != Inactive {
		return fmt.Errorf("%s: fiber must be retired and inactive", CodeBadBanner)
	}
	s.mu.Lock()
	hasChild := false
	for _, child := range s.fibers {
		if child.parent == f.uid {
			hasChild = true
			break
		}
	}
	provides := append([]Key(nil), f.comp.Provides...)
	s.mu.Unlock()
	if hasChild {
		return fmt.Errorf("%s: remove children first", CodeBadBanner)
	}
	for _, k := range provides {
		// σ_n = ⌀: a retiring provider's bindings are withdrawn by its accumulator
		// before it becomes Inactive, so its table must be empty here.
		for _, b := range s.table(k).snapshot() {
			if b.Provider == f.uid {
				return fmt.Errorf("%s: fiber still owns %s/%s", CodeBadBanner, k, b.Tag)
			}
		}
	}
	s.mu.Lock()
	delete(s.fibers, f.comp.ID)
	s.mu.Unlock()
	s.log.record(f.uid, "remove", "", "")
	return nil
}

// ---------- lifecycle rules (L-*) ----------

// Relied is relied_n(γ) (Def 54): some installed fiber m has a committed view
// naming n. The guard on L-Unload. It orders deactivations ALONG COEFFECTS, not
// along the fiber tree, and it is PER BINDING.
func (s *Store) Relied(n string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.fibers {
		if m.uid == n || !m.Installed() {
			continue
		}
		m.mu.Lock()
		for _, provider := range m.committed {
			if provider == n {
				m.mu.Unlock()
				return true
			}
		}
		m.mu.Unlock()
	}
	return false
}

// refresh is Algorithm 5's `refresh`: recompute target_n(γ) and, when it differs
// from the committed view, drive the lifecycle. It is IDEMPOTENT, which is what
// makes a neutral notify harmless.
//
// The implementation is SYNCHRONOUS (no scheduler), which is a legitimate runtime
// choice: the calculus is "reactive only" and quantifies over all interleavings, so
// a deterministic drain is one of them. §4.4's asynchrony/inertia extension is
// modelled by the Unloading state below, not by goroutines.
func (s *Store) refresh(f *Fiber) {
	target := s.TargetView(f)
	f.mu.Lock()
	state := f.state
	committed := f.committed
	targetMatches := sameView(target, committed)
	f.mu.Unlock()

	switch state {
	case Inactive, Unloading:
		if target != nil && !targetMatches {
			s.activate(f, target)
		}
	case Active, Reloading:
		if !targetMatches {
			s.beginLeave(f)
		}
	}
}

// activate is L-Begin → L-Iter* → L-Finish (§4.2.2). It commits the view BEFORE
// running any effect (Algorithm 5 line 14), then executes the component's whole
// load as ONE iterator under a target-stability guard. If the target changes
// mid-load the accumulator is recovered and the fiber routes into Unloading —
// the landing alternative of L-Divert (§4.4 inertia).
func (s *Store) activate(f *Fiber, view map[Key]string) {
	f.mu.Lock()
	if f.state != Inactive && f.state != Unloading {
		f.mu.Unlock()
		return
	}
	f.state = Reloading
	f.committed = view
	f.Reason = ""
	f.mu.Unlock()
	// Mark the fiber as mid-activation for the whole iterator: notifies issued by
	// its own effects (Algorithm 2's set → notify) must only queue — running the
	// converge loop from inside the load would deadlock against this very call.
	s.notifyMu.Lock()
	if s.activating == nil {
		s.activating = map[string]bool{}
	}
	s.activating[f.uid] = true
	s.notifyMu.Unlock()
	s.log.record(f.uid, "begin", "", "")
	defer func() {
		s.notifyMu.Lock()
		delete(s.activating, f.uid)
		s.notifyMu.Unlock()
	}()

	comp := f.comp
	guard := func() bool { return sameView(s.TargetView(f), view) }
	d, err := s.EffectIterator("load:"+comp.ID, func() (Stage, error) {
		if comp.Load == nil {
			return Stage{Effect: Effect{Name: "noop", Forward: func(*Store) (Inverse, error) {
				return func(*Store) error { return nil }, nil
			}}, Done: true}, nil
		}
		return comp.Load(s, f)()
	}, guard)

	if err != nil {
		// §4.4 failure: arrive INACTIVE having installed nothing (Cor 69), with the
		// error as an OUTCOME on the fiber. A failed fiber is NOT retried against an
		// unchanged environment; retry is a revision (fresh fiber).
		f.mu.Lock()
		f.state = Inactive
		f.committed = nil
		f.Reason = "load failed: " + err.Error()
		f.mu.Unlock()
		s.log.record(f.uid, "failed", "", "")
		return
	}

	// Compute the target BEFORE taking f.mu: TargetView acquires f.mu itself, so
	// holding f.mu across it would self-deadlock.
	stable := sameView(s.TargetView(f), view)
	f.mu.Lock()
	if stable {
		f.state = Active
		f.dispose = d
		f.mu.Unlock()
		s.log.record(f.uid, "finish", "", "")
		// A newly ACTIVE fiber may satisfy its own dependents. Queue the keys it
		// provides; the converge loop drives their refresh.
		for _, k := range comp.Provides {
			s.notify(k)
		}
		return
	}
	// Divert: target moved during the load. Hold the inverse, go Unloading; the
	// converge loop applies the accumulator once the guard releases.
	f.state = Unloading
	f.dispose = d
	f.mu.Unlock()
	s.log.record(f.uid, "divert", "", "")
}

// beginLeave is L-Leave: mark UNLOADING BEFORE scheduling inverses (Algorithm 5
// line 10). σ_γ unions over ACTIVE fibers alone, so this immediately removes the
// provider's bindings from the derived context, giving consumers their one-step
// head start — while the committed view still lets them read during teardown.
func (s *Store) beginLeave(f *Fiber) {
	f.mu.Lock()
	if f.state != Active && f.state != Reloading {
		f.mu.Unlock()
		return
	}
	f.state = Unloading
	f.mu.Unlock()
	s.log.record(f.uid, "leave", "", "")
	// Notify dependents FIRST so they begin their own L-Leave and clear their
	// committed views, which is what releases the ¬ relied guard.
	for _, k := range f.comp.Provides {
		s.notify(k)
	}

}

// unload is L-Unload: apply the accumulator, discard the committed view, leave
// Inactive. Then, if the target is non-⊥ again (a replacement provider appeared),
// chain straight back into activation (§4.4's inertial reload/unload chaining).
func (s *Store) unload(f *Fiber) {
	f.mu.Lock()
	d := f.dispose
	f.dispose = nil
	f.committed = nil
	f.state = Inactive
	f.Reason = "dependencies not satisfied"
	f.mu.Unlock()
	if d != nil {
		if err := d.Fire(); err != nil {
			// Recovery failed: report as an outcome rather than silently continuing.
			s.log.record(f.uid, "recovery-error", "", "")
		}
	}
	s.log.record(f.uid, "unload", "", "")
	// Inertial chaining (§4.4): if the target is non-⊥ again (a replacement
	// provider appeared), the converge loop re-activates this fiber. Queue the
	// keys it declares rather than recursing — Settle owns the loop.
	if len(f.comp.Depends) > 0 {
		s.notifyMu.Lock()
		if s.pending == nil {
			s.pending = map[Key]bool{}
		}
		for _, k := range f.comp.Depends {
			s.pending[k] = true
		}
		s.notifyMu.Unlock()
	}
}

// Quiescent is quiet(γ) (Def 53): every fiber's state agrees with its target.
// It must NOT hold s.mu while calling TargetView (which acquires s.mu itself via
// providerOf), so it collects pointers first and releases s.mu.
func (s *Store) Quiescent() bool {
	s.mu.Lock()
	all := make([]*Fiber, 0, len(s.fibers))
	for _, f := range s.fibers {
		all = append(all, f)
	}
	s.mu.Unlock()
	for _, f := range all {
		target := s.TargetView(f)
		f.mu.Lock()
		state := f.state
		committed := f.committed
		f.mu.Unlock()
		switch state {
		case Inactive:
			if target != nil {
				return false
			}
		case Active:
			// An active fiber's committed view must equal its target.
			if !sameView(target, committed) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ValidateComponents checks the DECLARATION-level premises Thm 73 assumes:
// acyclic precedence ≺ (Def 72: n ≺ m ≔ p_n ∩ d_m ≠ ⌀) and finite names.
// A cycle leaves components permanently Inactive (§6.5); refusing at load time is
// clearer. Returns a description of the cycle when found.
func ValidateComponents(comps []Component) error {
	provides := map[Key]string{}
	for _, c := range comps {
		for _, p := range c.Provides {
			if prev, ok := provides[p]; ok {
				return fmt.Errorf("%s: key %s provided by both %s and %s", CodeDuplicateKey, p, prev, c.ID)
			}
			provides[p] = c.ID
		}
	}
	// Build the precedence graph and detect a cycle with DFS.
	edges := map[string][]string{}
	for _, c := range comps {
		for _, d := range c.Depends {
			if provider, ok := provides[d]; ok {
				edges[provider] = append(edges[provider], c.ID)
			}
		}
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(string) bool
	visit = func(n string) bool {
		color[n] = gray
		for _, m := range edges[n] {
			if color[m] == gray {
				return true
			}
			if color[m] == white && visit(m) {
				return true
			}
		}
		color[n] = black
		return false
	}
	for _, c := range comps {
		if color[c.ID] == white && visit(c.ID) {
			return fmt.Errorf("%s: precedence cycle among components (Thm 73 assumes ≺ acyclic)", CodeCycle)
		}
	}
	return nil
}

// Code constants shared with the workbench protocol, re-declared here so the host
// package does not import the protocol package for string constants alone.
const (
	CodeBadBanner      = "WB_BAD_BANNER"
	CodeDuplicateKey   = "WB_DUPLICATE_KEY"
	CodeRevokeAbsent   = "WB_REVOKE_ABSENT"
	CodeUndeclared     = "WB_UNDECLARED_ACCESS"
	CodeInactive       = "WB_INACTIVE_ACCESS"
	CodeCycle          = "WB_CYCLE"
	CodeUnknownInverse = "WB_UNKNOWN_INVERSE"
)

package ctxcore

// P0 acceptance gate: the four metatheory obligations of the plan, as executable
// tests. "P0 的验收即本范式 vs 上一版的全部增量价值；不过不启动后续波."
//
//   Thm 68 (Recovery exactness) / Cor 69: a fiber's accumulator withdraws EXACTLY
//     its contribution despite arbitrary interleaving; σ^{u+1}_n = ⌀.
//   Thm 80 (Confluence): the quiescent state is the statically assembled one —
//     installation order permutations leave no trace.
//   Thm 70 (Ordering): a provider's episode strictly contains its consumers';
//     L-Begin only where γ ⊧ d; ¬ relied guard defers withdrawal.
//   Thm 73 (Progress): no deadlock; every maximal lifecycle sequence quiesces.
//
// Plus the O-Insert / access-control / notify-classifier / Def 81 obligations that
// P0 item 2 and 3 name.

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// loadComponent builds a component whose load registers N tagged entries into its
// OWN provided key through ctx.effect, one per iterator stage. Each stage's inverse
// removes THAT tag — a commutative table with unique tags (Thm 47), so two
// components' registrations commute and either can be withdrawn while the other
// stands (Thm 43).
//
// The iterator shape matters: a component's WHOLE load is ONE iterator (Def 17),
// and each stage boundary is a divert granularity (AD-1 / ch07-08).
func loadComponent(id string, provides, depends []Key, n int, tags ...Tag) Component {
	return Component{
		ID: id, Provides: provides, Depends: depends,
		Load: func(s *Store, f *Fiber) Iterator {
			step := 0
			var next func() (Stage, error)
			next = func() (Stage, error) {
				if step >= n {
					return Stage{Done: true, Effect: Effect{Name: "done", Forward: func(*Store) (Inverse, error) {
						return func(*Store) error { return nil }, nil
					}}}, nil
				}
				i := step
				step++
				tag := Tag(fmt.Sprintf("%s/tag-%d", id, i))
				if i < len(tags) {
					tag = tags[i]
				}
				key := KeyNavNodes
				if len(provides) > 0 {
					key = provides[0]
				}
				return Stage{
					Effect: Effect{Name: fmt.Sprintf("register-%s-%d", id, i), Forward: func(st *Store) (Inverse, error) {
						// This whole stage is already ONE ctx.effect application. Insert
						// directly rather than calling s.Set (which is itself an
						// effect): nesting would double-track the same mutation.
						if err := st.table(key).insert(Binding{Tag: tag, Provider: f.uid, Value: fmt.Sprintf("value-%d", i)}); err != nil {
							return nil, err
						}
						st.notify(key)
						return func(st2 *Store) error {
							if err := st2.table(key).remove(tag); err != nil {
								return err
							}
							st2.notify(key)
							return nil
						}, nil
					}},
					Done: false,
					Next: next,
				}, nil
			}
			return next
		},
	}
}

// tableTags returns the sorted tags bound at a key. Sorting gives a canonical form
// for observational equivalence ≃ (Def 31): a map's iteration order is not an
// operation any test can observe.
func tableTags(s *Store, k Key) []string {
	var out []string
	for _, b := range s.table(k).snapshot() {
		out = append(out, string(b.Tag))
	}
	sort.Strings(out)
	return out
}

// ---------- Thm 68 / Cor 69 : Recovery exactness ----------

func TestThm68RecoveryExactness(t *testing.T) {
	// "echo 插件装载后随机执行 N 个效应再卸载，断言键面回到装载前 (σ^{u+1} = ⌀)"
	s := NewStore()
	before := tableTags(s, KeyNavNodes)

	// A foreign component installs entries of its own and STAYS active while the
	// subject loads and unloads. Thm 68's whole point: one fiber's accumulator
	// withdraws exactly its own contribution despite foreign effects in between.
	foreign := loadComponent("foreign", nil, nil, 3)
	if _, _, err := s.Use(foreign, "root"); err != nil {
		t.Fatal(err)
	}
	if !s.Quiescent() {
		t.Fatal("the store did not quiesce after the foreign load")
	}
	foreignTags := tableTags(s, KeyNavNodes)
	if len(foreignTags) != 3 {
		t.Fatalf("foreign effect count = %d, want 3", len(foreignTags))
	}

	subject := loadComponent("subject", nil, nil, 5)
	f, d, err := s.Use(subject, "root")
	if err != nil {
		t.Fatal(err)
	}
	if f.State() != Active {
		t.Fatalf("subject state = %s, want Active", f.State())
	}
	if got := len(tableTags(s, KeyNavNodes)); got != 8 {
		t.Fatalf("after subject load = %d tags, want 8", got)
	}

	// Retire → deactivate → L-Unload applies the accumulator LIFO.
	s.Retire(f)
	if err := d.Fire(); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if !s.Quiescent() {
		t.Fatal("the store did not quiesce after the subject unloaded")
	}
	after := tableTags(s, KeyNavNodes)
	if strings.Join(after, ",") != strings.Join(foreignTags, ",") {
		t.Fatalf("Cor 69 violated: the departing fiber left a trace.\nbefore=%v\nafter =%v\nwant  =%v",
			before, after, foreignTags)
	}
	// The subject's own contribution is exactly gone.
	for _, tag := range after {
		if strings.HasPrefix(tag, "subject/") {
			t.Fatalf("subject tag %q survived unload", tag)
		}
	}
}

func TestThm68RecoveryIsLIFO(t *testing.T) {
	// Thm 16: inverses run in REVERSE application order. A component that records
	// the order its inverses fire must see last-in-first-out.
	var order []string
	s := NewStore()
	comp := Component{
		ID: "lifo",
		Load: func(s *Store, f *Fiber) Iterator {
			step := 0
			var next func() (Stage, error)
			next = func() (Stage, error) {
				if step >= 3 {
					return Stage{Done: true, Effect: Effect{Name: "done", Forward: func(*Store) (Inverse, error) {
						return func(*Store) error { return nil }, nil
					}}}, nil
				}
				i := step
				step++
				return Stage{
					Effect: Effect{Name: fmt.Sprintf("e%d", i), Forward: func(st *Store) (Inverse, error) {
						return func(st2 *Store) error {
							order = append(order, fmt.Sprintf("e%d", i))
							return nil
						}, nil
					}},
					Done: false, Next: next,
				}, nil
			}
			return next
		},
	}
	f, d, err := s.Use(comp, "root")
	if err != nil {
		t.Fatal(err)
	}
	if f.State() != Active {
		t.Fatal("not active")
	}
	s.Retire(f)
	if err := d.Fire(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "e2,e1,e0" {
		t.Fatalf("inverse order = %v, want [e2 e1 e0] (Thm 16 LIFO)", order)
	}
}

func TestDisposeIsArmed(t *testing.T) {
	// §5.1.1: firing dispose twice would apply an inverse at a state no
	// application of the effect produced. `armed` must make the second a no-op.
	fired := 0
	s := NewStore()
	d, err := s.Effect("once", func(*Store) (Inverse, error) {
		return func(*Store) error { fired++; return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Fire(); err != nil {
		t.Fatal(err)
	}
	if err := d.Fire(); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("inverse fired %d times, want 1", fired)
	}
}

// ---------- Thm 80 : Confluence ----------

func TestThm80Confluence(t *testing.T) {
	// "安装顺序置换 fuzz（3 插件 × 全排列 × 交错卸载），终态键面观测等价"
	// Three INDEPENDENT leaf components: their provisions are disjoint from each
	// other's declarations, so Thm 47 discharges independence and Thm 80 says the
	// quiescent state is a function of the final configuration alone.
	comps := []Component{
		loadComponent("alpha", nil, nil, 2),
		loadComponent("beta", nil, nil, 3),
		loadComponent("gamma", nil, nil, 1),
	}
	var permutations [][]int
	var permute func([]int, int)
	permute = func(a []int, k int) {
		if k == len(a) {
			cp := append([]int(nil), a...)
			permutations = append(permutations, cp)
			return
		}
		for i := k; i < len(a); i++ {
			a[k], a[i] = a[i], a[k]
			permute(a, k+1)
			a[k], a[i] = a[i], a[k]
		}
	}
	permute([]int{0, 1, 2}, 0)
	if len(permutations) != 6 {
		t.Fatalf("expected 6 permutations, got %d", len(permutations))
	}

	canonical := ""
	for pi, p := range permutations {
		s := NewStore()
		var fibers []*Fiber
		var disposes []*Dispose
		for _, idx := range p {
			f, d, err := s.Use(comps[idx], "root")
			if err != nil {
				t.Fatalf("perm %d: %v", pi, err)
			}
			fibers = append(fibers, f)
			disposes = append(disposes, d)
		}
		if !s.Quiescent() {
			t.Fatalf("perm %d did not quiesce", pi)
		}
		// Interleaved卸载：retire in yet another order, then fire all disposes.
		order := []int{2, 0, 1}
		if pi%2 == 0 {
			order = []int{0, 2, 1}
		}
		for _, i := range order {
			s.Retire(fibers[i])
		}
		if !s.Quiescent() {
			t.Fatalf("perm %d did not quiesce after retire", pi)
		}
		state := strings.Join(tableTags(s, KeyNavNodes), ",")
		if pi == 0 {
			canonical = state
		} else if state != canonical {
			t.Fatalf("Thm 80 violated: perm %d ended at %q, perm 0 at %q", pi, state, canonical)
		}
	}
	// All three retired ⇒ the final configuration is EMPTY, so confluence says the
	// quiescent key surface must be empty regardless of the route taken.
	if canonical != "" {
		t.Fatalf("canonical quiescent state = %q, want empty (all fibers retired)", canonical)
	}
}

func TestThm80AddRemoveReplaceUndo(t *testing.T) {
	// The ch08 worked example: insert P₁ + C, retire P₁, insert P₂ (same key),
	// retire P₂, reinsert P₁. The quiescent state equals loading the FINAL
	// configuration from scratch — "its dynamic history leaves no trace".
	// Provider uids, not values, drive the target view (Def 53 / §5.1.3).
	makeProvider := func(id string) Component {
		return loadComponent(id, []Key{KeyTunnelProxy}, nil, 1, Tag(id+"/proxy"))
	}
	consumer := loadComponent("consumer", nil, []Key{KeyTunnelProxy}, 1)

	live := NewStore()
	p1, d1, err := live.Use(makeProvider("p1"), "root")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := live.Use(consumer, "root"); err != nil {
		t.Fatal(err)
	}
	if !live.Quiescent() || !live.Satisfies([]Key{KeyTunnelProxy}) {
		t.Fatal("the provider was not available to the consumer")
	}
	live.Retire(p1)
	_ = d1
	if !live.Quiescent() {
		t.Fatal("did not quiesce after retiring p1")
	}
	// Re-enablement must be a FRESH FIBER (Thm 80 / Lemma 59(5)): the retired p1
	// stays retired, and a new uid provides the key.
	p1b, _, err := live.Use(makeProvider("p1-again"), "root")
	if err != nil {
		t.Fatal(err)
	}
	_ = p1b
	if !live.Quiescent() {
		t.Fatal("did not quiesce after the replacement")
	}
	if got := live.providerOf(KeyTunnelProxy); got == "" {
		t.Fatal("the replacement provider did not take over")
	}
	// The final configuration: one fresh provider + the consumer. Loading that
	// from scratch must give the same key surface.
	scratch := NewStore()
	if _, _, err := scratch.Use(makeProvider("p1-again"), "root"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scratch.Use(consumer, "root"); err != nil {
		t.Fatal(err)
	}
	if !scratch.Quiescent() {
		t.Fatal("scratch did not quiesce")
	}
	if strings.Join(tableTags(live, KeyTunnelProxy), ",") != strings.Join(tableTags(scratch, KeyTunnelProxy), ",") {
		t.Fatalf("Thm 80 violated:\nlive   =%v\nscratch=%v",
			tableTags(live, KeyTunnelProxy), tableTags(scratch, KeyTunnelProxy))
	}
}

// ---------- Thm 70 : Ordering ----------

func TestThm70Ordering(t *testing.T) {
	// "拔 navop-ssh 时断言消费者先失活、隧道最后关闭 (¬ relied 守卫日志)"
	// A provider (tunnel.proxy) and a consumer declaring it. On retirement the
	// consumer must deactivate BEFORE the provider's accumulator runs.
	s := NewStore()
	provider := loadComponent("ssh", []Key{KeyTunnelProxy}, nil, 1, Tag("ssh/tunnel"))
	consumer := loadComponent("containers", []Key{KeyContainerSession}, []Key{KeyTunnelProxy}, 1, Tag("containers/tab"))

	pf, pd, err := s.Use(provider, "root")
	if err != nil {
		t.Fatal(err)
	}
	cf, _, err := s.Use(consumer, "root")
	if err != nil {
		t.Fatal(err)
	}
	if pf.State() != Active || cf.State() != Active {
		t.Fatalf("provider=%s consumer=%s, want both Active", pf.State(), cf.State())
	}
	// The committed view names the PROVIDER UID, not a value.
	if got := cf.View()[KeyTunnelProxy]; got != pf.UID() {
		t.Fatalf("committed view = %q, want provider uid %q", got, pf.UID())
	}
	if !s.Relied(pf.UID()) {
		t.Fatal("the provider must be relied upon while its consumer is installed")
	}

	s.log.Reset()
	s.Retire(pf)
	_ = pd
	if !s.Quiescent() {
		t.Fatal("did not quiesce after retiring the provider")
	}
	events := s.log.Snapshot()
	consumerLeave, providerUnload := -1, -1
	for i, e := range events {
		if e.Fiber == cf.UID() && e.Kind == "leave" {
			consumerLeave = i
		}
		if e.Fiber == pf.UID() && e.Kind == "unload" {
			providerUnload = i
		}
	}
	if consumerLeave < 0 || providerUnload < 0 {
		t.Fatalf("missing lifecycle events: %+v", events)
	}
	if consumerLeave > providerUnload {
		t.Fatalf("Thm 70 violated: consumer left at %d but provider unloaded at %d (¬ relied guard failed)",
			consumerLeave, providerUnload)
	}
	// Both fibers end Inactive and the consumer's committed view is discarded.
	if cf.State() != Inactive || pf.State() != Inactive {
		t.Fatalf("final states: provider=%s consumer=%s", pf.State(), cf.State())
	}
	if len(cf.View()) != 0 {
		t.Fatal("the consumer kept a committed view after deactivation")
	}
}

func TestThm70BeginOnlyWhereSatisfied(t *testing.T) {
	// L-Begin(m) ⇒ γ_t ⊧ d_m. A fiber whose dependency never appears stays
	// Inactive with an explanatory Reason — AD-2's "Inactive 等待" rather than a
	// bare error.
	s := NewStore()
	consumer := loadComponent("waiting", nil, []Key{KeyTunnelProxy}, 1)
	f, _, err := s.Use(consumer, "root")
	if err != nil {
		t.Fatal(err)
	}
	if f.State() != Inactive {
		t.Fatalf("state = %s, want Inactive", f.State())
	}
	if f.Reason == "" {
		t.Fatal("an Inactive fiber must say WHY (the UI shows '此能力需要安装 navop-ssh')")
	}
	// Now the dependency appears → activating → the consumer runs.
	provider := loadComponent("ssh", []Key{KeyTunnelProxy}, nil, 1, Tag("ssh/tunnel"))
	if _, _, err := s.Use(provider, "root"); err != nil {
		t.Fatal(err)
	}
	if !s.Quiescent() {
		t.Fatal("did not quiesce")
	}
	if f.State() != Active {
		t.Fatalf("consumer did not activate: state=%s reason=%q", f.State(), f.Reason)
	}
}

// ---------- Thm 73 : Progress ----------

func TestThm73Progress(t *testing.T) {
	// No deadlock + every maximal lifecycle sequence quiesces. Randomized stress:
	// random inserts / retires / replacement providers, with a bounded number of
	// names (Thm 73 assumes N finite) and short iterators (len(e) ≤ K).
	for seed := int64(0); seed < 60; seed++ {
		rng := rand.New(rand.NewSource(seed))
		s := NewStore()
		var fibers []*Fiber
		var disposes []*Dispose
		// A provider plus several consumers of it, with random retire/reinsert.
		provider := loadComponent("ssh", []Key{KeyTunnelProxy}, nil, 1, Tag("ssh/tunnel"))
		pf, pd, err := s.Use(provider, "root")
		if err != nil {
			t.Fatal(err)
		}
		fibers = append(fibers, pf)
		disposes = append(disposes, pd)
		for i := 0; i < 4; i++ {
			c := loadComponent(fmt.Sprintf("c%d", i), nil, []Key{KeyTunnelProxy}, rng.Intn(3)+1)
			f, d, err := s.Use(c, "root")
			if err != nil {
				t.Fatal(err)
			}
			fibers = append(fibers, f)
			disposes = append(disposes, d)
		}
		if !s.Quiescent() {
			t.Fatalf("seed %d: initial load did not quiesce", seed)
		}
		// Randomly retire a subset, then reinsert a fresh provider so the target
		// view changes underneath the survivors (inertial chaining).
		for i := 1; i < len(fibers); i++ {
			if rng.Intn(2) == 0 {
				s.Retire(fibers[i])
			}
		}
		if rng.Intn(2) == 0 {
			s.Retire(pf)
			replacement := loadComponent("ssh2", []Key{KeyTunnelProxy}, nil, 1, Tag("ssh2/tunnel"))
			if _, _, err := s.Use(replacement, "root"); err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
		}
		if !s.Quiescent() {
			t.Fatalf("seed %d: Thm 73 progress violated — the store did not quiesce", seed)
		}
		// No orphaned bindings from retired fibers: Cor 69 at every unload.
		for _, b := range s.table(KeyTunnelProxy).snapshot() {
			found := false
			for _, f := range s.fibers {
				if f.uid == b.Provider && f.State() == Active {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("seed %d: orphan binding %s/%s from a non-active provider", seed, KeyTunnelProxy, b.Tag)
			}
		}
	}
}

func TestValidateComponentsRefusesCycles(t *testing.T) {
	// §6.5: dependency cycles leave components permanently Inactive. Thm 73
	// assumes ≺ acyclic; refuse at the door instead.
	a := Component{ID: "a", Provides: []Key{KeyTunnelProxy}, Depends: []Key{KeyContainerSession}}
	b := Component{ID: "b", Provides: []Key{KeyContainerSession}, Depends: []Key{KeyTunnelProxy}}
	if err := ValidateComponents([]Component{a, b}); err == nil {
		t.Fatal("a precedence cycle must be refused")
	}
	if err := ValidateComponents([]Component{
		{ID: "a", Provides: []Key{KeyTunnelProxy}},
		{ID: "b", Depends: []Key{KeyTunnelProxy}},
	}); err != nil {
		t.Fatalf("an acyclic pair must pass: %v", err)
	}
}

// ---------- P0 item 2: notify classifier + access enforcement ----------

func TestNotifyClassifier(t *testing.T) {
	// Def 22: activating iff ¬before ∧ after; deactivating iff before ∧ ¬after;
	// neutral otherwise.
	for _, c := range []struct {
		before, after bool
		want          NotifyClass
	}{
		{false, false, Neutral},
		{false, true, Activating},
		{true, false, Deactivating},
		{true, true, Neutral},
	} {
		if got := Notify(c.before, c.after); got != c.want {
			t.Fatalf("notify(%v,%v) = %v, want %v", c.before, c.after, got, c.want)
		}
	}
}

func TestAccessEnforcesDeclaration(t *testing.T) {
	// §5.1.4 Algorithm 6: UNDECLARED_ACCESS for a key not in d, INACTIVE_ACCESS for
	// a declared-but-not-committed key. Bare Get never fails.
	s := NewStore()
	provider := loadComponent("ssh", []Key{KeyTunnelProxy}, nil, 1, Tag("ssh/tunnel"))
	declared := loadComponent("consumer", nil, []Key{KeyTunnelProxy}, 1)
	if _, _, err := s.Use(provider, "root"); err != nil {
		t.Fatal(err)
	}
	f, _, err := s.Use(declared, "root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access(f, KeyTunnelProxy, Tag("ssh/tunnel")); err != nil {
		t.Fatalf("a declared key must be readable: %v", err)
	}
	if _, err := s.Access(f, KeyMQTTSession, Tag("x")); err == nil || !strings.Contains(err.Error(), CodeUndeclared) {
		t.Fatalf("an undeclared key must be refused with %s, got %v", CodeUndeclared, err)
	}
	// Bare Get never fails, even for an absent tag.
	if _, ok := s.Get(KeyMQTTSession, Tag("absent")); ok {
		t.Fatal("bare Get must report absence, not fail")
	}
	// An Inactive fiber declaring the key gets INACTIVE_ACCESS.
	waiting := loadComponent("waiting", nil, []Key{KeyTunnelProxy}, 1)
	wf, _, err := s.Use(waiting, "root")
	if err != nil {
		t.Fatal(err)
	}
	// Force it Inactive by retiring the provider (which deactivates dependents).
	// Simpler: a fiber with an unsatisfied dependency is already Inactive.
	_ = wf
	inactive := loadComponent("never", nil, []Key{KeyDesktopSession}, 1)
	inf, _, err := s.Use(inactive, "root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access(inf, KeyDesktopSession, Tag("x")); err == nil || !strings.Contains(err.Error(), CodeInactive) {
		t.Fatalf("an inactive fiber must be refused with %s, got %v", CodeInactive, err)
	}
}

func TestInterceptIsNotAnEffect(t *testing.T) {
	// Σinter (Def 26/27): a derived realization. Changing a policy must NOT trigger
	// a reload — AD-3's "改策略不触发重载". We assert the key surface is untouched
	// and no lifecycle event is logged.
	s := NewStore()
	c := loadComponent("leaf", nil, nil, 1)
	f, _, err := s.Use(c, "root")
	if err != nil {
		t.Fatal(err)
	}
	before := f.State()
	s.log.Reset()
	s.Intercept(KeySafeMode, "strict")
	if s.InterceptOf(KeySafeMode) != "strict" {
		t.Fatal("interception metadata was not stored")
	}
	if f.State() != before {
		t.Fatal("changing an interception key reloaded the fiber")
	}
	for _, e := range s.log.Snapshot() {
		if e.Kind == "begin" || e.Kind == "leave" || e.Kind == "unload" {
			t.Fatalf("interception produced a lifecycle event: %+v", e)
		}
	}
}

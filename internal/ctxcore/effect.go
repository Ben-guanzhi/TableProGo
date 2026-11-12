package ctxcore

// The single mutation primitive (AD-1 / §5.1.1): every context change flows through
// ctx.effect. This file realizes Def 17's effect iterator, Thm 16's LIFO recovery,
// and Algorithm 1's armed dispose.

import (
	"errors"
	"fmt"
	"sync"
)

// Effect is one stage of a witnessed effect function 𝔈Γ (Def 8): a forward map
// applied to the context, returning an inverse the runtime holds. The witness —
// that g(f(γ)) = γ — is the AUTHOR'S OBLIGATION (§5.1.1); the runtime stores and
// applies the inverse but cannot prove it.
//
// Forward receives the store and returns the inverse. Inverse is a closure over the
// exact state the application produced, which is what lets each revert recover the
// preceding state exactly (Thm 16) — a uniform inverse fixed before seeing the
// state would be the trackΓ anti-pattern of ch03.
type Effect struct {
	Name    string
	Forward func(*Store) (Inverse, error)
}

// Inverse restores the state its own application produced. It takes the store (not
// a saved copy): §3.1's g is Γ → Γ, and Thm 68's whole point is that g still
// reverts correctly when FOREIGN effects have moved Γ in between.
type Inverse func(*Store) error

// Stage is one iteration of ℑΓ (Def 17): an effect plus a continuation. Done=true
// terminates the iterator (Nothing); Done=false supplies the next stage (Just i').
// An iteration boundary is exactly where interleaving becomes possible (§3.4/§4).
type Stage struct {
	Effect Effect
	Done   bool
	Next   func() (Stage, error)
}

// Iterator is ℑΓ, the reified delimited continuation. A component's WHOLE LOAD is
// one iterator: banner → capability registration → ready, each boundary a divert
// granularity (AD-1 / ch07-08).
type Iterator func() (Stage, error)

// Guard is consulted before each iteration (Algorithm 1's `guard`). The two guards
// the paper names:
//   - armed / self-disposal (§5.1.1): a dispose fires at most once;
//   - target stability (§5.1.3): if the committed view changes mid-load, the
//     transition diverts into unload rather than completing against a stale view.
//
// Returning false stops the iterator WITHOUT running further stages; the inverses
// already accumulated are still returned so the caller can recover them.
type Guard func() bool

// Accumulator is 𝜑, the composite of inverses of effects performed so far
// (Def 2). Inverses are stored in APPLICATION order and run in REVERSE (LIFO) —
// the twisted composition of Def 1 (𝑓1∘𝑓2, 𝑔2∘𝑔1), Thm 16.
type Accumulator struct {
	mu       sync.Mutex
	inverses []Inverse
	names    []string
	armed    bool
}

func newAccumulator() *Accumulator { return &Accumulator{armed: true} }

// track appends the inverse yielded by one stage: trackΓ(𝑓,𝑔)(𝛾,𝜑) = (𝑓(𝛾), 𝜑∘𝑔).
// Storing in application order and executing from the end IS 𝜑∘𝑔.
func (a *Accumulator) track(name string, inv Inverse) {
	if inv == nil {
		inv = func(*Store) error { return nil }
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inverses = append(a.inverses, inv)
	a.names = append(a.names, name)
}

// Recover applies the accumulator LIFO and resets it to idΓ: recoverΓ(𝛾,𝜑) =
// (𝜑(𝛾), idΓ). The soundness invariant (Thm 7) is 𝜑(𝛾) = 𝛾0.
//
// armed is the §5.1.1 self-disposal guard: firing twice would apply an inverse at
// a state no application of the effect produced. It flips false BEFORE running the
// inverses, so a re-entrant call is a no-op even if an inverse fails midway.
func (a *Accumulator) Recover(s *Store) error {
	a.mu.Lock()
	if !a.armed {
		a.mu.Unlock()
		return nil
	}
	a.armed = false
	inverses := append([]Inverse(nil), a.inverses...)
	names := append([]string(nil), a.names...)
	a.inverses = nil
	a.names = nil
	a.mu.Unlock()

	var firstErr error
	for i := len(inverses) - 1; i >= 0; i-- {
		s.log.record("", "recover:"+names[i], "", "")
		if err := inverses[i](s); err != nil && firstErr == nil {
			// Keep going: one bad inverse must not strand the rest. Thm 68's
			// recovery is exact only when every inverse reverts its own effect;
			// a failing inverse is reported so the test can catch it.
			firstErr = fmt.Errorf("inverse %q: %w", names[i], err)
		}
	}
	return firstErr
}

// Dispose is the armed, one-shot handle Algorithm 1 prepends to ctx.dispose.
type Dispose struct {
	once sync.Once
	acc  *Accumulator
	s    *Store
}

func (d *Dispose) Fire() error {
	var err error
	d.once.Do(func() {
		d.s.log.record("", "dispose", "", "")
		err = d.acc.Recover(d.s)
	})
	return err
}

// execute drives one iterator under a guard, folding inverses LIFO (Algorithm 1 /
// Def 18's effectiterΓ). It is the SOLE place iterates run: ctx.effect wraps it,
// and ctx.set is itself a ctx.effect call (ch04's synergy).
//
// If a stage's Forward fails, execute stops and returns the accumulated inverses
// plus the error, so the caller can recover the partial load — the failure extension
// of §4.4: a raise routes into UNLOADING holding the inverse built up to the
// failing iteration and arrives INACTIVE having installed nothing (Cor 69).
func execute(s *Store, iter Iterator, guard Guard) (*Accumulator, error) {
	acc := newAccumulator()
	cur := iter
	for step := 0; ; step++ {
		if guard != nil && !guard() {
			return acc, nil // diverted: caller recovers the partial accumulator
		}
		stage, err := cur()
		if err != nil {
			return acc, err
		}
		if stage.Effect.Name == "" {
			return acc, errors.New("ctxcore: effect stage has no name")
		}
		s.log.record("", "apply:"+stage.Effect.Name, "", "")
		inv, err := stage.Effect.Forward(s)
		if err != nil {
			return acc, fmt.Errorf("effect %q: %w", stage.Effect.Name, err)
		}
		acc.track(stage.Effect.Name, inv)
		if stage.Done {
			return acc, nil
		}
		if stage.Next == nil {
			return acc, errors.New("ctxcore: non-terminal stage has no continuation")
		}
		cur = stage.Next
	}
}

// Effect applies ONE effect as a tracked, armed disposal — the single mutation
// primitive of §5.1.1. Every caller (Set, Use, RegisterNav, OpenChannel, …) goes
// through here; nothing else mutates the tables.
func (s *Store) Effect(name string, forward func(*Store) (Inverse, error)) (*Dispose, error) {
	acc, err := execute(s, func() (Stage, error) {
		return Stage{Effect: Effect{Name: name, Forward: forward}, Done: true}, nil
	}, nil)
	if err != nil {
		return nil, err
	}
	return &Dispose{acc: acc, s: s}, nil
}

// EffectIterator runs a whole multi-stage load as ONE tracked effect (Def 18:
// "an iterator is an effect in its own right"). The guard is supplied by the
// fiber's target-stability check.
func (s *Store) EffectIterator(name string, iter Iterator, guard Guard) (*Dispose, error) {
	acc, err := execute(s, iter, guard)
	if err != nil {
		// Partial recovery: a failed/diverted iteration must leave NOTHING behind
		// (Cor 69), even though the caller never receives a Dispose handle.
		if rerr := acc.Recover(s); rerr != nil {
			return nil, fmt.Errorf("%w (recovery also failed: %v)", err, rerr)
		}
		return nil, err
	}
	return &Dispose{acc: acc, s: s}, nil
}

// Set is ctx.set(key, value): coeffect PROVISION IS AN EFFECT (ch04 eq. 21,
// §5.1.1 Algorithm 2). It binds under a unique tag, notifies watchers, and returns
// an inverse that removes the binding and notifies again. The inverse is keyed by
// TAG — the table is commutative, so withdrawing one registration while others
// stand cannot disturb them (Thm 43/47).
func (s *Store) Set(k Key, tag Tag, provider string, value any) (*Dispose, error) {
	return s.Effect("set:"+string(k)+"/"+string(tag), func(st *Store) (Inverse, error) {
		tbl := st.table(k)
		if err := tbl.insert(Binding{Tag: tag, Provider: provider, Value: value}); err != nil {
			return nil, err
		}
		st.log.record(provider, "set", k, tag)
		st.notify(k)
		return func(st2 *Store) error {
			st2.log.record(provider, "unset", k, tag)
			if err := tbl.remove(tag); err != nil {
				return err
			}
			st2.notify(k)
			return nil
		}, nil
	})
}

// Intercept is Σinter (Def 26/27): right-biased metadata merged at ACCESS time.
// It is a DERIVED realization, NOT an effect — nothing in the shared table changes,
// so there is no inverse to track and changing a policy triggers NO reload (AD-3:
// Safe Mode is an interception key, not an effect).
func (s *Store) Intercept(k Key, metadata any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Right bias: context-carried metadata overrides the component's declaration.
	s.intercept[k] = metadata
}

func (s *Store) InterceptOf(k Key) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intercept[k]
}

// ---------- public table operations (for hosts and effect stages) ----------

// TableInsert binds one entry into a key's commutative table. Callers running
// INSIDE an effect stage use this directly (the stage's Forward returns the
// remove inverse — the iterator's accumulator owns it, exactly how Cordis's
// execute collects each `iter.next()` yield); callers OUTSIDE any effect use
// Set, which wraps the same mutation as a tracked, armed effect.
func (s *Store) TableInsert(k Key, tag Tag, provider string, value any) error {
	return s.table(k).insert(Binding{Tag: tag, Provider: provider, Value: value})
}

// TableRemove withdraws one entry by its unique tag. Revoking an absent tag is
// refused (CodeRevokeAbsent): the precondition is what keeps every withdrawal
// paired with the insertion it undoes.
func (s *Store) TableRemove(k Key, tag Tag) error {
	return s.table(k).remove(tag)
}

// Notify is the PUBLIC reactive dispatch (Algorithm 3): a host or effect stage
// that changed a key's table calls this so the affected fibers refresh. It is
// idempotent and re-entrancy-safe (Settle owns the converge loop).
func (s *Store) Notify(k Key) {
	s.notify(k)
}

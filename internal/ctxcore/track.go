package ctxcore

// Runtime effect tracking: effects applied AFTER a fiber's load completed
// (a channel opened when the user clicks "new terminal", a stats timer started)
// must be reclaimed by the SAME accumulator that reclaims the load's effects.
// This is AD-1 at the fiber level: "every context mutation flows through one
// primitive", and Thm 68 at the fiber level — one accumulator withdraws exactly
// the fiber's contribution, whatever mix of load stages and runtime effects
// built it.

import "fmt"

// Track applies ONE effect and appends its inverse to the fiber's accumulator.
//
// The fiber must be Active. An effect tracked against a fiber that is leaving
// would install a binding the deactivation is already unwinding — refuse it
// rather than race the unwind.
func (s *Store) Track(f *Fiber, name string, forward func(*Store) (Inverse, error)) error {
	f.mu.Lock()
	if f.state != Active || f.dispose == nil {
		state := f.state
		f.mu.Unlock()
		return fmt.Errorf("%s: fiber %s is not active (state=%s)", CodeInactive, f.ID(), state)
	}
	acc := f.dispose.acc
	f.mu.Unlock()

	one, err := execute(s, func() (Stage, error) {
		return Stage{Effect: Effect{Name: name, Forward: forward}, Done: true}, nil
	}, nil)
	if err != nil {
		return err
	}
	// execute returned an accumulator holding exactly this one effect's inverse;
	// move it onto the fiber's own accumulator so unload reclaims everything LIFO.
	// NOTE: Accumulator.track locks the mutex ITSELF — do not hold acc.mu across
	// the call, or the same goroutine re-enters the mutex and deadlocks.
	one.mu.Lock()
	pending := append([]Inverse(nil), one.inverses...)
	names := append([]string(nil), one.names...)
	one.inverses = nil
	one.names = nil
	one.mu.Unlock()
	for i, inv := range pending {
		acc.track(names[i], inv)
	}
	return nil
}

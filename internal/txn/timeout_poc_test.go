package txn

import (
	"sync"
	"testing"
	"time"
)

// Transaction-timeout PoC — see docs/DESIGN-txn-timeout-2026-11-03.md.
//
// The design picks an absolute deadline with a warn-ahead window and
// auto-rollback. This file models that policy in isolation so internal/txn's
// production code stays untouched until the design is reviewed. The prototype
// is the shape the real Session fields would take: a deadline set at Begin, a
// warn-ahead window, a sweep that rolls back exactly once, and a deliberate
// refusal to extend the deadline when statements run.

const (
	pocTimeout   = 10 * time.Minute
	pocWarnAhead = 1 * time.Minute
)

// pocTx is the deadline half of a transaction: what Session would carry once
// the design lands. Statements are counted but must not move the deadline.
type pocTx struct {
	mu         sync.Mutex
	id         string
	deadline   time.Time
	warnAhead  time.Duration
	note       string
	closed     bool // rolled back or committed; the sweep refuses to act again
	statements int
}

// remaining is how long is left before the deadline; negative once past it.
func (t *pocTx) remaining(now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.deadline.Sub(now)
}

// warned reports whether now is inside the warning window. A closed
// transaction has nothing left to warn about.
func (t *pocTx) warned(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed && t.deadline.Sub(now) <= t.warnAhead
}

// record models a statement that ran inside the transaction. In the real
// Session this is Record(err); the prototype only needs the point that it does
// not move the deadline — the chosen policy is absolute, not idle.
func (t *pocTx) record() {
	t.mu.Lock()
	t.statements++
	t.mu.Unlock()
}

// sweep rolls the transaction back once past the deadline and records why. It
// returns whether it acted, so a second sweep is a no-op. The check and the
// close are one step under the lock, mirroring how Session.Begin holds its
// lock across the opener round trip: two concurrent sweeps cannot both see an
// open transaction and double-rollback it.
func (t *pocTx) sweep(now time.Time, open Opener) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || now.Before(t.deadline) {
		return false, nil
	}
	t.closed = true
	t.note = "事务已超时并自动回滚"
	return true, open.Rollback(t.id)
}

func TestTransactionTimeoutPoC(t *testing.T) {
	base := time.Unix(1_762_000_000, 0)

	t.Run("warns only inside the warn-ahead window", func(t *testing.T) {
		tx := &pocTx{id: "txn-app", deadline: base.Add(pocTimeout), warnAhead: pocWarnAhead}

		if got := tx.remaining(base); got != pocTimeout {
			t.Fatalf("remaining at Begin = %v, want %v", got, pocTimeout)
		}
		if tx.warned(base) {
			t.Fatal("no warning at Begin")
		}
		// 90s left: still outside the 1m window.
		if tx.warned(base.Add(pocTimeout - 90*time.Second)) {
			t.Fatal("no warning at 90s left")
		}
		// 30s left: inside the window.
		if !tx.warned(base.Add(pocTimeout - 30*time.Second)) {
			t.Fatal("want a warning inside the window")
		}
	})

	t.Run("statements do not extend the deadline", func(t *testing.T) {
		tx := &pocTx{id: "txn-app", deadline: base.Add(pocTimeout), warnAhead: pocWarnAhead}
		for i := 0; i < 5; i++ {
			tx.record()
		}
		// The deadline is fixed at Begin; running statements must not move it.
		// (An idle policy would have pushed the deadline forward on each
		// record; this pins the absolute choice.)
		if got := tx.remaining(base.Add(9 * time.Minute)); got != time.Minute {
			t.Fatalf("remaining after 9m of statements = %v, want 1m (absolute, not idle)", got)
		}
	})

	t.Run("sweep rolls back once past the deadline", func(t *testing.T) {
		op := &fakeOpener{}
		tx := &pocTx{id: "txn-app", deadline: base.Add(pocTimeout), warnAhead: pocWarnAhead}

		acted, err := tx.sweep(base.Add(pocTimeout-time.Second), op)
		if acted || err != nil {
			t.Fatalf("sweep before the deadline must not act: acted=%v err=%v", acted, err)
		}
		acted, err = tx.sweep(base.Add(pocTimeout+time.Second), op)
		if !acted || err != nil {
			t.Fatalf("sweep past the deadline must act: acted=%v err=%v", acted, err)
		}
		if len(op.rollback) != 1 || op.rollback[0] != "txn-app" {
			t.Fatalf("rollbacks = %v, want exactly one for txn-app", op.rollback)
		}
		if tx.note != "事务已超时并自动回滚" {
			t.Fatalf("note = %q", tx.note)
		}
		if tx.warned(base.Add(pocTimeout + time.Hour)) {
			t.Fatal("a closed transaction must not warn")
		}
		// A second sweep is a no-op: the check and the close are one step.
		if acted, _ := tx.sweep(base.Add(pocTimeout+time.Hour), op); acted {
			t.Fatal("a second sweep must not roll back again")
		}
		if len(op.rollback) != 1 {
			t.Fatalf("rollbacks = %v, want still exactly one", op.rollback)
		}
	})

	t.Run("commit before the deadline does not roll back", func(t *testing.T) {
		op := &fakeOpener{}
		tx := &pocTx{id: "txn-app", deadline: base.Add(pocTimeout), warnAhead: pocWarnAhead}
		// A commit closes the transaction without touching the deadline.
		tx.closed = true

		acted, err := tx.sweep(base.Add(pocTimeout+time.Hour), op)
		if acted || err != nil {
			t.Fatalf("sweep after a commit must not act: acted=%v err=%v", acted, err)
		}
		if len(op.rollback) != 0 {
			t.Fatalf("rollbacks = %v, want none after commit", op.rollback)
		}
	})

	t.Run("concurrent sweeps roll back exactly once", func(t *testing.T) {
		op := &fakeOpener{}
		tx := &pocTx{id: "txn-app", deadline: base.Add(pocTimeout), warnAhead: pocWarnAhead}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = tx.sweep(base.Add(pocTimeout+time.Second), op)
			}()
		}
		wg.Wait()
		if len(op.rollback) != 1 || op.rollback[0] != "txn-app" {
			t.Fatalf("rollbacks = %v, want exactly one across concurrent sweeps", op.rollback)
		}
	})
}

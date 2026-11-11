package txn

import (
	"sync"
	"testing"
	"time"
)

// Transaction-timeout production tests — see
// docs/DESIGN-txn-timeout-2026-11-03.md (policy B: absolute deadline +
// warn-ahead + auto-rollback, §7.3: Failed sessions are swept too).
//
// These pin the Session behavior that replaced the PoC model: the deadline is
// fixed at Begin, running statements must not move it, the sweep rolls back
// exactly once, a commit wins over a sweep, and a session without a timeout
// behaves exactly as it did before the design landed.

func TestTimeoutWarnsOnlyInsideTheWindow(t *testing.T) {
	var s Session
	op := &fakeOpener{}
	// 10m deadline, 1m warn-ahead (Begin's built-in ratio).
	if err := s.Begin(op, "app", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	base := time.Now()

	if got := s.Remaining(base); got <= 9*time.Minute || got > 10*time.Minute {
		t.Fatalf("remaining at Begin = %v, want ~10m", got)
	}
	if s.Warned(base) {
		t.Fatal("no warning at Begin")
	}
	soon := base.Add(9*time.Minute + 30*time.Second) // 30s left: inside the window
	if !s.Warned(soon) {
		t.Fatal("want a warning inside the warn-ahead window")
	}
	early := base.Add(8 * time.Minute) // 2m left: outside the 1m window
	if s.Warned(early) {
		t.Fatal("no warning before the window")
	}
	// A custom warn-ahead overrides the ratio.
	var custom Session
	if err := custom.Begin(op, "app", 10*time.Minute, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if !custom.Warned(base.Add(6 * time.Minute)) { // 4m left, inside the 5m window
		t.Fatal("custom warn-ahead must widen the window")
	}
}

func TestTimeoutStatementsDoNotExtendTheDeadline(t *testing.T) {
	var s Session
	op := &fakeOpener{}
	if err := s.Begin(op, "app", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	first := s.Remaining(time.Now())
	for i := 0; i < 5; i++ {
		s.Record(nil) // statements run; the deadline is absolute, not idle
	}
	second := s.Remaining(time.Now())
	// Only wall-clock time moved between the two reads; 5 records must not
	// have pushed the deadline out. The second read is smaller or equal.
	if second > first {
		t.Fatalf("deadline moved forward on statements: %v -> %v", first, second)
	}
}

func TestTimeoutSweepRollsBackOncePastTheDeadline(t *testing.T) {
	var s Session
	op := &fakeOpener{}
	begin := time.Now()
	if err := s.Begin(op, "app", 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	// Before the deadline: no action.
	if acted, err := s.Sweep(op, begin.Add(9*time.Minute)); acted || err != nil {
		t.Fatalf("sweep before the deadline must not act: acted=%v err=%v", acted, err)
	}
	// Past it: exactly one rollback, session closed, note explains why.
	acted, err := s.Sweep(op, begin.Add(10*time.Minute+time.Second))
	if !acted || err != nil {
		t.Fatalf("sweep past the deadline must act: acted=%v err=%v", acted, err)
	}
	if len(op.rollback) != 1 || op.rollback[0] != "txn-app" {
		t.Fatalf("rollbacks = %v, want exactly one for txn-app", op.rollback)
	}
	if got := s.Note(); got != "事务已超时并自动回滚" {
		t.Fatalf("note = %q", got)
	}
	if s.HasTx() {
		t.Fatalf("swept session must be back to autocommit, status=%s", s.Status)
	}
	if s.Warned(begin.Add(10 * time.Minute)) {
		t.Fatal("a closed session must not warn")
	}
	// A second sweep is a no-op.
	if acted, _ := s.Sweep(op, begin.Add(10*time.Minute+time.Hour)); acted {
		t.Fatal("a second sweep must not roll back again")
	}
	if len(op.rollback) != 1 {
		t.Fatalf("rollbacks = %v, want still exactly one", op.rollback)
	}
}

func TestTimeoutCommitWinsOverSweep(t *testing.T) {
	var s Session
	op := &fakeOpener{}
	begin := time.Now()
	if err := s.Begin(op, "app", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(op); err != nil {
		t.Fatal(err)
	}
	// Long past the deadline: the committed session has nothing to sweep.
	if acted, err := s.Sweep(op, begin.Add(10*time.Minute+time.Hour)); acted || err != nil {
		t.Fatalf("sweep after a commit must not act: acted=%v err=%v", acted, err)
	}
	if len(op.rollback) != 0 {
		t.Fatalf("rollbacks = %v, want none after commit", op.rollback)
	}
}

func TestTimeoutFailedSessionIsSweptToo(t *testing.T) {
	var s Session
	op := &fakeOpener{}
	begin := time.Now()
	if err := s.Begin(op, "app", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	s.Record(errBoom{}) // the statement aborts the transaction (§7.3)
	if s.Status != Failed {
		t.Fatalf("status = %s, want failed", s.Status)
	}
	acted, err := s.Sweep(op, begin.Add(10*time.Minute+time.Second))
	if !acted || err != nil {
		t.Fatalf("a failed session past its deadline must be swept: acted=%v err=%v", acted, err)
	}
	if len(op.rollback) != 1 {
		t.Fatalf("rollbacks = %v, want one", op.rollback)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

func TestTimeoutConcurrentSweepsRollBackExactlyOnce(t *testing.T) {
	var s Session
	op := &fakeOpener{}
	begin := time.Now()
	if err := s.Begin(op, "app", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	past := begin.Add(10*time.Minute + time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Sweep(op, past)
		}()
	}
	wg.Wait()
	if len(op.rollback) != 1 || op.rollback[0] != "txn-app" {
		t.Fatalf("rollbacks = %v, want exactly one across concurrent sweeps", op.rollback)
	}
}

func TestTimeoutTimedOutDistinguishesNoDeadlineFromDue(t *testing.T) {
	var bounded Session
	op := &fakeOpener{}
	begin := time.Now()
	if err := bounded.Begin(op, "app", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if bounded.TimedOut(begin.Add(9 * time.Minute)) {
		t.Fatal("not yet due")
	}
	if !bounded.TimedOut(begin.Add(10 * time.Minute)) {
		t.Fatal("deadline exactly now counts as due")
	}

	var unbounded Session
	if err := unbounded.Begin(op, "app"); err != nil {
		t.Fatal(err)
	}
	if unbounded.TimedOut(begin.Add(1000 * time.Hour)) {
		t.Fatal("a session without a timeout never times out")
	}
}

func TestTimeoutNoTimeoutKeepsUnboundedBehavior(t *testing.T) {
	var s Session
	op := &fakeOpener{}
	if err := s.Begin(op, "app"); err != nil {
		t.Fatal(err)
	}
	if got := s.Remaining(time.Now()); got != 0 {
		t.Fatalf("no-timeout Remaining = %v, want 0 (no limit)", got)
	}
	if s.Warned(time.Now()) {
		t.Fatal("no-timeout sessions never warn")
	}
	if want := "事务中 · 0 条语句 · app"; s.Describe() != want {
		t.Fatalf("Describe = %q, want the unchanged line %q", s.Describe(), want)
	}
}

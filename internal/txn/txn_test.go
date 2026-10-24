package txn

import (
	"fmt"
	"testing"
)

type fakeOpener struct {
	begun    []string
	commits  []string
	rollback []string
	err      error
}

func (f *fakeOpener) Begin(database string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.begun = append(f.begun, database)
	return "txn-" + database, nil
}

func (f *fakeOpener) Commit(id string) error {
	f.commits = append(f.commits, id)
	return f.err
}

func (f *fakeOpener) Rollback(id string) error {
	f.rollback = append(f.rollback, id)
	return f.err
}

func TestBeginCommitRoundTrip(t *testing.T) {
	op := &fakeOpener{}
	var s Session

	if err := s.Begin(op, "app"); err != nil {
		t.Fatal(err)
	}
	if s.Status != Active || s.ID != "txn-app" || s.Database != "app" {
		t.Fatalf("session = %+v", s)
	}
	s.Record(nil)
	s.Record(nil)
	if s.Statements != 2 {
		t.Fatalf("statements = %d", s.Statements)
	}
	if err := s.Commit(op); err != nil {
		t.Fatal(err)
	}
	if s.Status != None || s.ID != "" {
		t.Fatalf("after commit = %+v", s)
	}
	if len(op.commits) != 1 || op.commits[0] != "txn-app" {
		t.Fatalf("commits = %v", op.commits)
	}
}

func TestBeginTwiceIsRefused(t *testing.T) {
	op := &fakeOpener{}
	var s Session
	if err := s.Begin(op, "app"); err != nil {
		t.Fatal(err)
	}
	// A second Begin would leak the first transaction, so it is refused.
	if err := s.Begin(op, "app"); err == nil {
		t.Fatal("a second Begin must be refused")
	}
	if len(op.begun) != 1 {
		t.Fatalf("begun = %v", op.begun)
	}
}

func TestCommitAndRollbackNeedAnOpenTransaction(t *testing.T) {
	op := &fakeOpener{}
	var s Session
	if err := s.Commit(op); err != ErrNotActive {
		t.Fatalf("commit err = %v", err)
	}
	if err := s.Rollback(op); err != ErrNotActive {
		t.Fatalf("rollback err = %v", err)
	}
	if len(op.commits) != 0 || len(op.rollback) != 0 {
		t.Fatalf("no calls expected: %v %v", op.commits, op.rollback)
	}
}

// A statement that fails inside a transaction aborts it on most engines: the
// session must stop offering to run more, and only rollback gets out.
func TestFailedStatementAbortsTheSession(t *testing.T) {
	op := &fakeOpener{}
	var s Session
	if err := s.Begin(op, "app"); err != nil {
		t.Fatal(err)
	}
	s.Record(fmt.Errorf("duplicate key value violates unique constraint"))
	if s.Status != Failed {
		t.Fatalf("status = %s", s.Status)
	}
	if err := s.Guard("app"); err == nil {
		t.Fatal("a statement must be refused after the transaction aborted")
	}
	// Rollback is the way out, and it works from the failed state.
	if err := s.Rollback(op); err != nil {
		t.Fatal(err)
	}
	if s.Status != None {
		t.Fatalf("after rollback = %+v", s)
	}
	// The next statement runs in autocommit.
	if err := s.Guard("app"); err != nil {
		t.Fatalf("autocommit guard = %v", err)
	}
}

func TestGuardRefusesAnotherDatabase(t *testing.T) {
	op := &fakeOpener{}
	var s Session
	if err := s.Begin(op, "app"); err != nil {
		t.Fatal(err)
	}
	if err := s.Guard("app"); err != nil {
		t.Fatalf("same database: %v", err)
	}
	// The other database has its own pool; the statement would run outside the
	// transaction the user thinks they are in.
	if err := s.Guard("logs"); err != ErrDatabaseMismatch {
		t.Fatalf("other database err = %v", err)
	}
}

func TestGuardAllowsEverythingInAutocommit(t *testing.T) {
	var s Session
	if err := s.Guard("anything"); err != nil {
		t.Fatalf("autocommit = %v", err)
	}
}

// The session forgets its transaction before the server answers, so a failed
// commit cannot leave a tab believing it still holds one.
func TestCommitResetsEvenWhenTheServerFails(t *testing.T) {
	op := &fakeOpener{err: fmt.Errorf("connection reset")}
	var s Session
	if err := s.Begin(&fakeOpener{}, "app"); err != nil {
		t.Fatal(err)
	}
	s.ID = "txn-app"
	if err := s.Commit(op); err == nil {
		t.Fatal("the server error must surface")
	}
	if s.Status != None {
		t.Fatalf("a failed commit must still close the session: %+v", s)
	}
}

func TestBeginPropagatesTheOpenerError(t *testing.T) {
	op := &fakeOpener{err: fmt.Errorf("cannot begin")}
	var s Session
	if err := s.Begin(op, "app"); err == nil {
		t.Fatal("want the opener error")
	}
	if s.Status != None {
		t.Fatalf("a failed begin must leave autocommit: %+v", s)
	}
}

func TestDescribe(t *testing.T) {
	var s Session
	if s.Describe() != "" {
		t.Fatalf("autocommit describes as %q", s.Describe())
	}
	if err := s.Begin(&fakeOpener{}, "app"); err != nil {
		t.Fatal(err)
	}
	s.Record(nil)
	if got := s.Describe(); got == "" || !contains(got, "1 条语句") || !contains(got, "app") {
		t.Fatalf("describe = %q", got)
	}
	s.Record(fmt.Errorf("boom\nsecond line"))
	got := s.Describe()
	if !contains(got, "boom") || contains(got, "second line") {
		t.Fatalf("describe should keep one line: %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

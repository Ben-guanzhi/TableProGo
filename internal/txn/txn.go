// Package txn holds one interactive transaction session per database tab.
//
// A GUI client has to keep a transaction open across several statement runs, while
// the *sql.Tx itself lives inside dbcore's TxManager. This package is the client's
// side of that: which transaction a tab currently has open, what the UI shows, and
// the rule that a transaction belongs to the tab (and database) that opened it.
//
// Kept out of package main because none of it needs the app: it is a small state
// machine over tx ids (see txn_test.go). The App supplies open/commit/rollback.
package txn

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Status is what the UI shows for a tab's transaction.
type Status string

// The constants are ordered so the zero value is autocommit: a Session is created
// as a plain value all over the client, and "" must not read as a live transaction.
const (
	// None means the tab runs in autocommit (the zero value).
	None Status = ""
	// Active means a transaction is open and statements run inside it.
	Active Status = "active"
	// Failed means a statement errored inside the transaction: most engines abort
	// the whole transaction at that point, so only rollback is offered.
	Failed Status = "failed"
)

// Session is the transaction one tab has open. The zero value is a tab in
// autocommit, which is the common case.
//
// The Session pointer is shared between the query paths and the transaction
// endpoints (two concurrent statements on one tab, or a commit arriving while
// one runs), so every mutation and hot-path read goes through the mutex —
// the methods below lock it; the exported fields remain for tests and must
// not be read by concurrent production code.
type Session struct {
	mu sync.Mutex

	Status Status
	ID     string
	// ConnID is the connection the transaction was opened on; the client needs it
	// to reach the manager when the tab goes away.
	ConnID string
	// Database is the database the transaction was opened against. A session is
	// bound to one database: statements on another pool cannot join it.
	Database string
	// Isolation is the level the transaction was opened with, as configured on
	// the connection ("read_committed"… or "" for the engine's default). The
	// UI shows it in the session's status line so the user can see which dial
	// their transaction actually runs under.
	Isolation string
	// Statements counts what ran inside, so the UI can say how much is at risk
	// before a rollback throws it away.
	Statements int
	// Err is the statement error that failed the transaction.
	Err string

	// --- transaction timeout (DESIGN-txn-timeout-2026-11-03, policy B) ---
	// deadline is fixed at Begin: now + TxTimeout, an absolute limit. Running
	// statements must NOT move it — the whole point of the absolute choice is
	// that a transaction the user walked away from still converges, even one
	// that keeps "running". Zero deadline (the zero value) means no limit,
	// which keeps every existing Session literal and test working unchanged.
	deadline  time.Time
	warnAhead time.Duration
	// note explains why a session ended on its own ("事务已超时并自动回滚");
	// hand-driven commit/rollback leave it empty.
	note string
}

// HasTx reports whether a transaction is open or aborted. Both states keep the
// transaction id: the engine decides what a statement or commit against an
// aborted transaction does.
func (s *Session) HasTx() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status == Active || s.Status == Failed
}

// TxID returns the open transaction's id ("" when none).
func (s *Session) TxID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ID
}

// ConnectionID returns the connection the transaction was opened on.
func (s *Session) ConnectionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ConnID
}

// BindConnection records which connection the tab's transaction lives on,
// before Begin.
func (s *Session) BindConnection(connID string) {
	s.mu.Lock()
	s.ConnID = connID
	s.mu.Unlock()
}

// DatabaseName returns the database the transaction is bound to.
func (s *Session) DatabaseName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Database
}

// Opener is what the client must provide: the three calls that reach dbcore,
// plus the isolation level the connection was configured with (read back for
// the session's status line; "" = the engine's default).
type Opener interface {
	Begin(database string) (string, error)
	Commit(id string) error
	Rollback(id string) error
	IsolationLevel() string
}

// ErrNotActive is returned for commit/rollback on a tab with no transaction.
var ErrNotActive = errors.New("no transaction is open")

// ErrDatabaseMismatch is returned when a statement targets another database than
// the open transaction; the statement would otherwise silently run outside it.
var ErrDatabaseMismatch = errors.New("the open transaction belongs to another database")

// Begin opens a transaction for the session's database. A tab that already has one
// is refused rather than silently nesting: SQL has no nested transactions, and a
// second Begin would leak the first.
//
// The check and the opener round trip run under one lock: with the round trip
// outside it, two concurrent Begins both pass the guard, both open a server-side
// transaction, and the second id overwrites the first — commit/rollback then
// address the wrong transaction and the other one leaks. The lock is per-tab, so
// this only serialises Begin against that tab's own commit/rollback/statements,
// which already share it.
//
// An optional final timeout argument (DESIGN-txn-timeout-2026-11-03, policy B)
// fixes an absolute deadline at Begin: now + timeout, with warnAhead as the
// window the UI starts warning in. Running statements never move the deadline —
// the absolute choice is what gives a transaction the user walked away from an
// upper bound on how long it can hold server-side resources. Passing no timeout
// keeps the session unbounded, which is the pre-existing behavior.
func (s *Session) Begin(open Opener, database string, timeout ...time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status == Active || s.Status == Failed {
		return fmt.Errorf("this tab already has an open transaction; commit or roll it back first")
	}
	id, err := open.Begin(database)
	if err != nil {
		return err
	}
	s.Status = Active
	s.ID = id
	s.Database = database
	s.Isolation = open.IsolationLevel()
	s.Statements = 0
	s.Err = ""
	s.note = ""
	s.deadline = time.Time{}
	s.warnAhead = 0
	if len(timeout) > 0 && timeout[0] > 0 {
		s.deadline = time.Now().Add(timeout[0])
		s.warnAhead = timeout[0] / 6 // 10m → 1m, the design's default ratio
		if len(timeout) > 1 && timeout[1] > 0 {
			s.warnAhead = timeout[1]
		}
	}
	return nil
}

// Remaining reports how long is left before the transaction's absolute deadline;
// negative once past it. A session without a deadline (Begin was called without
// a timeout) reports the zero duration, which callers show as "no limit".
func (s *Session) Remaining(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deadline.IsZero() || (s.Status != Active && s.Status != Failed) {
		return 0
	}
	return s.deadline.Sub(now)
}

// Warned reports whether now is inside the deadline's warn-ahead window. A
// session without a deadline never warns; a closed one has nothing left to
// warn about.
func (s *Session) Warned(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deadline.IsZero() || (s.Status != Active && s.Status != Failed) {
		return false
	}
	return s.deadline.Sub(now) <= s.warnAhead
}

// TimedOut reports whether the session carries an open (or aborted)
// transaction whose absolute deadline has passed. This — not Remaining — is
// what a sweeper asks: "no deadline" and "deadline exactly now" must not be
// conflated, and the HasTx guard lives here so callers cannot sweep a
// session that never had a transaction.
func (s *Session) TimedOut(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deadline.IsZero() || (s.Status != Active && s.Status != Failed) {
		return false
	}
	return !now.Before(s.deadline)
}

// Note explains why a session ended on its own ("事务已超时并自动回滚").
func (s *Session) Note() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.note
}

// Sweep rolls the transaction back once past its deadline and records why.
// It returns whether it acted, so a repeated sweep is a no-op. The deadline
// check and the close are one step under the lock — mirroring how Begin holds
// its lock across the opener round trip — so two concurrent sweeps cannot both
// see an open transaction and double-rollback it. A session without a deadline
// (or already closed) is left untouched. Rolling back outside the lock keeps
// the same discipline as Commit/Rollback: the session state resets first, the
// network round trip runs after.
func (s *Session) Sweep(open Opener, now time.Time) (bool, error) {
	s.mu.Lock()
	if s.deadline.IsZero() || (s.Status != Active && s.Status != Failed) {
		s.mu.Unlock()
		return false, nil
	}
	if now.Before(s.deadline) {
		s.mu.Unlock()
		return false, nil
	}
	id := s.ID
	s.resetLocked()
	s.note = "事务已超时并自动回滚"
	s.mu.Unlock()
	err := open.Rollback(id)
	if err != nil {
		// The rollback round trip failed: the server-side transaction may
		// still be open. Surface it — the sweeper logs it, and the next
		// sweep attempt sees a closed session, so it will not retry on its
		// own; the user's next explicit rollback (or tab close, which
		// forgetSession tolerates a missing id on) is the retry.
		return true, err
	}
	return true, nil
}

// Commit finalizes. A failed transaction can still be committed (the engine
// decides what survives); it is the user's explicit choice.
func (s *Session) Commit(open Opener) error {
	s.mu.Lock()
	if s.Status != Active && s.Status != Failed {
		s.mu.Unlock()
		return ErrNotActive
	}
	id := s.ID
	// Reset before the round trip: whatever the server says, this tab no longer
	// holds an open transaction, and retrying a half-committed one is worse than
	// reporting the error.
	s.resetLocked()
	s.mu.Unlock()
	return open.Commit(id)
}

// Rollback discards. It is always allowed, including after a failure — that is the
// only way out of an aborted transaction.
func (s *Session) Rollback(open Opener) error {
	s.mu.Lock()
	if s.Status != Active && s.Status != Failed {
		s.mu.Unlock()
		return ErrNotActive
	}
	id := s.ID
	s.resetLocked()
	s.mu.Unlock()
	return open.Rollback(id)
}

// Guard decides whether a statement may run in this session for database. A tab in
// autocommit always may. An open transaction only accepts statements bound to the
// database it was opened on: running one elsewhere would execute it outside the
// transaction the user believes they are in.
//
// An empty database means "the connection's own", which is what the statement
// paths pass when the tab's picker is untouched. It is folded onto the session's
// database so the common case is not a mismatch.
func (s *Session) Guard(database string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != Active && s.Status != Failed {
		return nil
	}
	if s.Status == Failed {
		return fmt.Errorf("the transaction is aborted (%s); roll it back before running more statements", s.Err)
	}
	if database == "" {
		database = s.Database
	}
	if database == "" || database == s.Database {
		return nil
	}
	return ErrDatabaseMismatch
}

// Record counts a statement that ran inside the transaction and, when it failed,
// marks the session aborted so the UI stops offering more statements.
func (s *Session) Record(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != Active {
		return
	}
	s.Statements++
	if err != nil {
		s.Status = Failed
		s.Err = err.Error()
	}
}

// resetLocked returns the session to autocommit; the caller holds mu.
// ConnID is kept: it identifies the tab's connection, not the transaction.
// note is kept too: a sweep sets it AFTER reset, and the UI reads it off the
// closed session to explain why the transaction vanished.
func (s *Session) resetLocked() {
	s.Status = None
	s.ID = ""
	s.Database = ""
	s.Statements = 0
	s.Err = ""
	s.deadline = time.Time{}
	s.warnAhead = 0
}

// Describe is the one-line status the UI shows next to the editor. A session
// with an absolute deadline appends the remaining time — the design's "呈现 C"
// riding on policy B — and a warning mark once inside the warn-ahead window.
func (s *Session) Describe() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.Status {
	case Active:
		line := fmt.Sprintf("事务中 · %d 条语句 · %s", s.Statements, s.Database)
		if s.Isolation != "" {
			line += fmt.Sprintf(" · 隔离 %s", s.Isolation)
		}
		if !s.deadline.IsZero() {
			remaining := time.Until(s.deadline)
			if remaining < 0 {
				remaining = 0
			}
			mark := ""
			if s.warnAhead > 0 && remaining <= s.warnAhead {
				mark = " · ⚠ 即将超时"
			}
			line += fmt.Sprintf(" · 还剩 %s%s", remaining.Round(time.Second), mark)
		}
		return line
	case Failed:
		return fmt.Sprintf("事务已中止 · %s", firstLine(s.Err))
	default:
		return ""
	}
}

// firstLine keeps a long driver error from filling the status bar.
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 80 {
		text = text[:80] + "…"
	}
	return text
}

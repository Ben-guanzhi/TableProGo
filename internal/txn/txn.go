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
type Session struct {
	Status Status
	ID     string
	// ConnID is the connection the transaction was opened on; the client needs it
	// to reach the manager when the tab goes away.
	ConnID string
	// Database is the database the transaction was opened against. A session is
	// bound to one database: statements on another pool cannot join it.
	Database string
	// Statements counts what ran inside, so the UI can say how much is at risk
	// before a rollback throws it away.
	Statements int
	// Err is the statement error that failed the transaction.
	Err string
}

// Opener is what the client must provide: the three calls that reach dbcore.
type Opener interface {
	Begin(database string) (string, error)
	Commit(id string) error
	Rollback(id string) error
}

// ErrNotActive is returned for commit/rollback on a tab with no transaction.
var ErrNotActive = errors.New("no transaction is open")

// ErrDatabaseMismatch is returned when a statement targets another database than
// the open transaction; the statement would otherwise silently run outside it.
var ErrDatabaseMismatch = errors.New("the open transaction belongs to another database")

// Begin opens a transaction for the session's database. A tab that already has one
// is refused rather than silently nesting: SQL has no nested transactions, and a
// second Begin would leak the first.
func (s *Session) Begin(open Opener, database string) error {
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
	s.Statements = 0
	s.Err = ""
	return nil
}

// Commit finalizes. A failed transaction can still be committed (the engine
// decides what survives); it is the user's explicit choice.
func (s *Session) Commit(open Opener) error {
	if s.Status != Active && s.Status != Failed {
		return ErrNotActive
	}
	id := s.ID
	// Reset before the round trip: whatever the server says, this tab no longer
	// holds an open transaction, and retrying a half-committed one is worse than
	// reporting the error.
	s.reset()
	return open.Commit(id)
}

// Rollback discards. It is always allowed, including after a failure — that is the
// only way out of an aborted transaction.
func (s *Session) Rollback(open Opener) error {
	if s.Status != Active && s.Status != Failed {
		return ErrNotActive
	}
	id := s.ID
	s.reset()
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
	if s.Status != Active {
		return
	}
	s.Statements++
	if err != nil {
		s.Status = Failed
		s.Err = err.Error()
	}
}

// reset returns the session to autocommit. ConnID is kept: it identifies the tab's
// connection, not the transaction.
func (s *Session) reset() {
	s.Status = None
	s.ID = ""
	s.Database = ""
	s.Statements = 0
	s.Err = ""
}

// Describe is the one-line status the UI shows next to the editor.
func (s *Session) Describe() string {
	switch s.Status {
	case Active:
		return fmt.Sprintf("事务中 · %d 条语句 · %s", s.Statements, s.Database)
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

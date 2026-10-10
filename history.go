package main

import (
	"log"
	"strings"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"
)

// HistoryEntry is one executed statement. The object view's history page and
// later change auditing read it back, so the object the statement targeted is
// stored alongside the text.
type HistoryEntry struct {
	ID       int64  `json:"id"`
	Time     int64  `json:"time"`
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Table    string `json:"table,omitempty"`
	SQL      string `json:"sql"`
	RowCount int    `json:"rowCount"`
	Affected int64  `json:"affectedRows"`
	// ExecutionTime is in seconds, matching QueryResult.
	ExecutionTime float64 `json:"executionTime"`
	Error         string  `json:"error,omitempty"`
}

// historyLimit keeps the on-disk log bounded; a GUI session produces orders of
// magnitude more browses than statements worth remembering.
const historyLimit = 500

// historySQLChars truncates stored statements so one pasted megabyte of SQL
// cannot bloat the file.
const historySQLChars = 4000

func (a *App) loadHistory() {
	entries, ok, err := readJSONFile[HistoryEntry](a.historyPath)
	if err != nil {
		log.Printf("Failed to load history: %v", err)
		return
	}
	if !ok {
		return
	}
	a.history = entries
	a.histObserved = append([]HistoryEntry(nil), entries...)
	for _, e := range entries {
		if e.ID > a.historySeq {
			a.historySeq = e.ID
		}
	}
}

// statementTable extracts the object a statement touches, the same way the
// comment lookup does, so history can be filtered per table.
func statementTable(query string) string {
	match := queryTableRe.FindStringSubmatch(sqlCommentRe.ReplaceAllString(query, " "))
	if match == nil {
		return ""
	}
	ref := parseTableRef(match[1])
	if ref.table == "" || !isIdentifierLike(ref.table) {
		return ""
	}
	return strings.ToLower(ref.table)
}

func (a *App) recordStatement(connID, database, query string, result QueryResult) {
	sql := strings.TrimSpace(query)
	if sql == "" {
		return
	}
	// Cut on runes so a Chinese comment in the statement cannot end mid-byte.
	if r := []rune(sql); len(r) > historySQLChars {
		sql = string(r[:historySQLChars]) + " …"
	}

	entry := HistoryEntry{
		Time:          time.Now().UnixMilli(),
		ConnID:        connID,
		Database:      strings.TrimSpace(database),
		Table:         statementTable(sql),
		SQL:           sql,
		Error:         result.Error,
		RowCount:      result.RowCount,
		Affected:      result.AffectedRows,
		ExecutionTime: result.ExecutionTime,
	}

	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	a.historySeq++
	entry.ID = a.historySeq
	a.history = append(a.history, entry)
	if len(a.history) > historyLimit {
		a.history = append([]HistoryEntry(nil), a.history[len(a.history)-historyLimit:]...)
	}

	// The Fyne client shares this file: fold in what it recorded meanwhile
	// before writing, so neither side's statements are lost.
	a.mergeHistory()
	a.writeHistory()
}

// History returns recorded statements newest first, optionally narrowed to one
// connection, database or table.
func (a *App) History(connID, database, table string, limit int) []HistoryEntry {
	if limit <= 0 || limit > historyLimit {
		limit = historyLimit
	}
	table = strings.ToLower(strings.TrimSpace(table))
	database = strings.ToLower(strings.TrimSpace(database))

	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	entries := make([]HistoryEntry, 0, limit)
	for i := len(a.history) - 1; i >= 0 && len(entries) < limit; i-- {
		entry := a.history[i]
		if connID != "" && entry.ConnID != connID {
			continue
		}
		if table != "" && entry.Table != table {
			continue
		}
		// Statements that ran without an explicit database inherit the
		// connection's own, which the caller cannot tell apart after the fact.
		if database != "" && entry.Database != "" && strings.ToLower(entry.Database) != database {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// ClearHistory drops recorded statements, all of them or those matching the
// given connection, database and table filters. It returns how many went away.
func (a *App) ClearHistory(connID, database, table string) int {
	connID = strings.TrimSpace(connID)
	database = strings.ToLower(strings.TrimSpace(database))
	table = strings.ToLower(strings.TrimSpace(table))

	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	// A filtered clear is a statement about every matching entry, including
	// ones another client recorded while we ran: merge first, then filter.
	a.mergeHistory()
	kept := a.history[:0]
	for _, entry := range a.history {
		if connID != "" && entry.ConnID != connID {
			kept = append(kept, entry)
			continue
		}
		if table != "" && entry.Table != table {
			kept = append(kept, entry)
			continue
		}
		if database != "" && entry.Database != "" && strings.ToLower(entry.Database) != database {
			kept = append(kept, entry)
			continue
		}
	}
	removed := len(a.history) - len(kept)
	if removed == 0 {
		return 0
	}
	a.history = append([]HistoryEntry(nil), kept...)
	a.writeHistory()
	return removed
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The state files are shared with the Fyne client. These tests pin the
// cross-client merge: additions from the other client survive a rewrite, this
// process's own deletions stick, and a truncated file can no longer be
// silently overwritten into a permanent loss.

func TestRecordKeepsOtherClientsStatements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")

	a := &App{historyPath: path}
	a.recordStatement("c1", "zs", "select 1", QueryResult{})

	b := &App{historyPath: path}
	b.loadHistory()
	b.recordStatement("c2", "zs", "select 2", QueryResult{})

	a.recordStatement("c1", "zs", "select 3", QueryResult{})
	if got := a.History("", "", "", 0); len(got) != 3 {
		t.Fatalf("a lost track of the shared log: %d entries", len(got))
	}
	ids := map[int64]bool{}
	for _, e := range a.history {
		if ids[e.ID] {
			t.Fatalf("duplicate history id %d after merge", e.ID)
		}
		ids[e.ID] = true
	}
}

func TestClearRemovesMatchingButKeepsForeignStatements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")

	a := &App{historyPath: path}
	a.recordStatement("c1", "zs", "select 1", QueryResult{})

	b := &App{historyPath: path}
	b.loadHistory()
	b.recordStatement("c2", "zs", "select 2", QueryResult{})

	if removed := a.ClearHistory("c1", "", ""); removed != 1 {
		t.Fatalf("cleared %d entries, want 1", removed)
	}
	// The other client's statement survives the filtered clear...
	if got := a.History("c2", "", "", 0); len(got) != 1 {
		t.Fatalf("the other client's statement was cleared: %d entries", len(got))
	}
	// ...and the cleared one does not come back on the next record.
	a.recordStatement("c1", "zs", "select 3", QueryResult{})
	if got := a.History("", "", "", 0); len(got) != 2 {
		t.Fatalf("cleared statement resurrected: %d entries", len(got))
	}
}

func TestConnectionSaveKeepsForeignAddsAndOwnDeletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")

	a := &App{filePath: path, savedConnections: []ConnectionConfig{
		{Name: "one", Type: "sqlite", Database: "a.db"},
	}}
	a.saveToFile()

	b := &App{filePath: path}
	b.loadFromFile()
	b.savedConnections = append(b.savedConnections, ConnectionConfig{
		Name: "two", Type: "postgres", Host: "h", Port: 5432, Database: "b",
	})
	b.saveToFile()

	// a saves again: b's connection must survive the rewrite...
	a.savedConnections = append(a.savedConnections, ConnectionConfig{
		Name: "three", Type: "mysql", Host: "h2", Port: 3306, Database: "c",
	})
	a.saveToFile()
	var file []ConnectionConfig
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read connections: %v", err)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("parse connections: %v", err)
	}
	if len(file) != 3 {
		t.Fatalf("connections after merge = %d, want 3: %s", len(file), data)
	}
	// ...and be adopted into a's memory, so a's next save keeps it too.
	if len(a.savedConnections) != 3 {
		t.Fatalf("the foreign connection was not adopted into memory: %d", len(a.savedConnections))
	}

	// a deletes its own first connection: the deletion must stick instead of
	// resurrecting from the file it was just removed from.
	a.savedConnections = a.savedConnections[1:]
	a.saveToFile()
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread connections: %v", err)
	}
	file = nil
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("parse connections: %v", err)
	}
	// The adopted connection sits at the end of a's list, so compare by name
	// set rather than position.
	if len(file) != 2 {
		t.Fatalf("saved connections = %s, want two entries", data)
	}
	names := map[string]bool{}
	for _, c := range file {
		names[c.Name] = true
	}
	if !names["two"] || !names["three"] {
		t.Fatalf("saved connections = %s, want [two three]", data)
	}
}

func TestCorruptStateFileIsQuarantined(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	corrupt := []byte("{not json")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	a := &App{historyPath: path}
	a.loadHistory()
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("the corrupt file was not moved aside: %v", err)
	}

	// Recording after a corrupt load starts a fresh file instead of failing.
	a.recordStatement("c1", "zs", "select 1", QueryResult{})
	if got := a.History("", "", "", 0); len(got) != 1 {
		t.Fatalf("history did not recover: %d entries", len(got))
	}
}

package main

// W11b: notebooks, end to end on a real SQLite database.
//
// The kernel tests pin the expansion rules; these pin the parts the app adds:
// persistence, the run path (including Safe Mode), and the fact that the statement
// the server runs is the expanded one rather than the cell's text.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func notebookApp(t *testing.T) (*App, string) {
	t.Helper()
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	for _, q := range []string{
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, amount INTEGER)`,
		`INSERT INTO orders (id, amount) VALUES (1,50),(2,200),(3,300)`,
	} {
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	return app, "c1"
}

func TestNotebookSavesAndLists(t *testing.T) {
	app, connID := notebookApp(t)
	nb := Notebook{
		ID: "nb1", Name: "营收", Connection: connID,
		Cells: []NotebookCellData{
			{ID: "a", SQL: "SELECT id, amount FROM orders WHERE amount > 100"},
		},
	}
	list, err := app.SaveNotebook(nb)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(list) != 1 || list[0].Name != "营收" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].UpdatedAt == "" {
		t.Fatal("a saved notebook needs a timestamp")
	}

	// A second save of the same id replaces rather than duplicates.
	nb.Name = "营收 v2"
	if _, err := app.SaveNotebook(nb); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.LoadNotebooks()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Name != "营收 v2" {
		t.Fatalf("loaded = %+v", loaded)
	}

	// Delete removes it; deleting again reports that it is gone.
	if _, err := app.DeleteNotebook("nb1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := app.DeleteNotebook("nb1"); err == nil {
		t.Fatal("deleting a missing notebook should say so")
	}
}

func TestNotebookSurvivesABOM(t *testing.T) {
	// A notebooks.json edited in Notepad starts with a BOM; refusing it would look
	// like the notebooks vanished.
	app, _ := notebookApp(t)
	path := notebookPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`[{"id":"x","name":"带BOM"}]`)...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := app.LoadNotebooks()
	if err != nil {
		t.Fatalf("a BOM must be tolerated: %v", err)
	}
	if len(list) != 1 || list[0].Name != "带BOM" {
		t.Fatalf("list = %+v", list)
	}
}

func TestNotebookRunExpandsReferences(t *testing.T) {
	app, connID := notebookApp(t)
	nb := Notebook{
		ID: "nb2", Name: "链", Connection: connID,
		Cells: []NotebookCellData{
			{ID: "a", SQL: "SELECT id, amount FROM orders WHERE amount > 100"},
			{ID: "b", SQL: "SELECT count(*) AS n FROM {{cell_1}}"},
		},
	}
	res := app.RunNotebookCell(NotebookRunRequest{Notebook: nb, Index: 1})
	if res.Error != "" {
		t.Fatalf("run: %s", res.Error)
	}
	if res.Result.Error != "" {
		t.Fatalf("statement: %s\n%s", res.Result.Error, res.Statement)
	}
	// The reported statement is the EXPANDED one — that is what makes a surprising
	// result explainable.
	if !strings.Contains(res.Statement, "WITH cell_1 AS") {
		t.Fatalf("statement not expanded: %s", res.Statement)
	}
	if len(res.Result.Rows) != 1 {
		t.Fatalf("rows = %d", len(res.Result.Rows))
	}
	// Values arrive as the JSON-ish cell type, so read them the way the grid does.
	if n, ok := cellInt(res.Result.Rows[0][0]); !ok || n != 2 {
		t.Fatalf("count = %+v, want 2", res.Result.Rows[0][0])
	}
	if len(res.DependsOn) != 1 || res.DependsOn[0] != 1 {
		t.Fatalf("dependsOn = %v", res.DependsOn)
	}
}

func TestNotebookRunRefusesBadReferences(t *testing.T) {
	app, connID := notebookApp(t)
	// Forward reference: the kernel's message names both cells.
	nb := Notebook{
		ID: "nb3", Connection: connID,
		Cells: []NotebookCellData{
			{ID: "a", SQL: "SELECT * FROM {{cell_2}}"},
			{ID: "b", SQL: "SELECT 1"},
		},
	}
	res := app.RunNotebookCell(NotebookRunRequest{Notebook: nb, Index: 0})
	if res.Error == "" || !strings.Contains(res.Error, "comes later") {
		t.Fatalf("res = %+v", res)
	}
	// Out of range.
	if res := app.RunNotebookCell(NotebookRunRequest{Notebook: nb, Index: 9}); res.Error == "" {
		t.Fatal("an out-of-range cell must be refused")
	}
	// No connection bound.
	nb2 := Notebook{Cells: []NotebookCellData{{ID: "a", SQL: "SELECT 1"}}}
	if res := app.RunNotebookCell(NotebookRunRequest{Notebook: nb2, Index: 0}); res.Error == "" {
		t.Fatal("a notebook with no connection must be refused")
	}
}

func TestNotebookRunObeysSafeMode(t *testing.T) {
	// A notebook is not a way around the write guard: a no-WHERE UPDATE in a cell is
	// blocked exactly as it would be in the editor.
	app, _ := newTestAppWithSQLite(t, "blockNoWhere")
	if r := app.ExecuteQuery("c1", `CREATE TABLE guard_t (a INTEGER)`, ""); r.Error != "" {
		t.Fatal(r.Error)
	}
	nb := Notebook{
		ID: "nb4", Connection: "c1",
		Cells: []NotebookCellData{{ID: "a", SQL: "UPDATE guard_t SET a = 1"}},
	}
	res := app.RunNotebookCell(NotebookRunRequest{Notebook: nb, Index: 0})
	if res.Result.Error == "" {
		t.Fatal("Safe Mode must block a no-WHERE UPDATE inside a notebook too")
	}
	if !strings.Contains(res.Result.Error, "WHERE") {
		t.Fatalf("error = %q", res.Result.Error)
	}
}
// cellInt reads an integer cell whatever numeric shape the driver produced.
func cellInt(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	case string:
		var n int64
		if _, err := fmt.Sscanf(x, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

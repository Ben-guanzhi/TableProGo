package main

// W11b: SQL notebooks — the cell model, its persistence, and running a cell.
//
// A notebook is an ordered list of cells against ONE connection. A cell may build
// on an earlier one with {{cell_N}}, which the kernel expands into a CTE
// (sqltext.ExpandNotebook). This file is the app-side half: where notebooks live on
// disk, and what happens when a cell runs.
//
// Two decisions worth stating:
//
//   - The EXPANSION is server-side, not in the editor. The editor holds the cell's
//     own text (so {{cell_1}} stays visible and editable) and the server decides
//     what actually runs — the same plan/apply split the transfer and sync paths
//     use, and the reason a user cannot fool the notebook by pasting a CTE name.
//   - A cell run goes through the normal statement path, so Safe Mode gates it
//     exactly as a typed statement is gated. A notebook is a convenience for
//     ordering decisions, not a way around them.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dbcore/dbcore/sqltext"
)

// NotebookCellData is one cell as the UI holds it.
type NotebookCellData struct {
	// ID is stable across renames so a cell's identity survives reordering.
	ID string `json:"id"`
	// SQL is the cell's own text, references included.
	SQL string `json:"sql"`
	// Title is optional; the UI falls back to "单元格 N".
	Title string `json:"title,omitempty"`
}

// Notebook is one saved notebook.
type Notebook struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Connection string             `json:"connection"`
	Database   string             `json:"database,omitempty"`
	Cells      []NotebookCellData `json:"cells"`
	// UpdatedAt is RFC3339; the list sorts by it so the last-worked notebook is
	// first.
	UpdatedAt string `json:"updatedAt"`
}

// NotebookRunRequest runs one cell.
type NotebookRunRequest struct {
	// Notebook is sent whole: the expansion needs the earlier cells, and having the
	// client's current text means the run matches what the screen shows even before
	// an autosave.
	Notebook Notebook `json:"notebook"`
	// Index is the 0-based cell to run.
	Index int `json:"index"`
}

// NotebookRunResult is what the cell produced.
type NotebookRunResult struct {
	Result QueryResult `json:"result"`
	// Statement is the EXPANDED SQL that actually ran. The UI shows it on demand:
	// when a cell behaves differently from what it looks like, this is the answer.
	Statement string `json:"statement"`
	// DependsOn lists the 1-based cells that were inlined, in CTE order.
	DependsOn []int `json:"dependsOn,omitempty"`
	// Error is a notebook-level failure (bad reference, empty cell). A statement
	// failure is in Result.Error, the same as the editor's.
	Error string `json:"error,omitempty"`
}

// notebookPath is where notebooks live: beside the other state files, so a profile
// carries its notebooks the way it carries its connections.
func notebookPath() string { return userDataFilePath("notebooks.json") }

// LoadNotebooks reads the saved notebooks.
func (a *App) LoadNotebooks() ([]Notebook, error) {
	data, err := os.ReadFile(notebookPath())
	if err != nil {
		if os.IsNotExist(err) {
			// No file is the normal first run, not a failure.
			return []Notebook{}, nil
		}
		return nil, err
	}
	var out []Notebook
	if err := json.Unmarshal(trimBOMBytes(data), &out); err != nil {
		return nil, fmt.Errorf("读取笔记本: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// SaveNotebook writes one notebook, replacing it by id and stamping UpdatedAt.
func (a *App) SaveNotebook(nb Notebook) ([]Notebook, error) {
	return a.saveNotebook(nb, true)
}

// saveNotebook is SaveNotebook with the stamp under test control.
func (a *App) saveNotebook(nb Notebook, stamp bool) ([]Notebook, error) {
	if strings.TrimSpace(nb.ID) == "" {
		return nil, fmt.Errorf("笔记本缺少 ID")
	}
	if strings.TrimSpace(nb.Name) == "" {
		nb.Name = "未命名笔记本"
	}
	if stamp {
		nb.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	existing, err := a.LoadNotebooks()
	if err != nil {
		return nil, err
	}
	replaced := false
	for i := range existing {
		if existing[i].ID == nb.ID {
			existing[i] = nb
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, nb)
	}
	if err := a.writeNotebooks(existing); err != nil {
		return nil, err
	}
	sort.SliceStable(existing, func(i, j int) bool { return existing[i].UpdatedAt > existing[j].UpdatedAt })
	return existing, nil
}

// DeleteNotebook removes one notebook by id.
func (a *App) DeleteNotebook(id string) ([]Notebook, error) {
	existing, err := a.LoadNotebooks()
	if err != nil {
		return nil, err
	}
	kept := existing[:0]
	found := false
	for _, nb := range existing {
		if nb.ID == id {
			found = true
			continue
		}
		kept = append(kept, nb)
	}
	if !found {
		return nil, fmt.Errorf("笔记本不存在: %s", id)
	}
	if err := a.writeNotebooks(kept); err != nil {
		return nil, err
	}
	return kept, nil
}

// writeNotebooks stores the list atomically (temp file + rename): a crash mid-write
// must not leave a half-parsed notebooks.json.
func (a *App) writeNotebooks(list []Notebook) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	path := notebookPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RunNotebookCell expands and runs one cell.
func (a *App) RunNotebookCell(req NotebookRunRequest) NotebookRunResult {
	nb := req.Notebook
	if req.Index < 0 || req.Index >= len(nb.Cells) {
		return NotebookRunResult{Error: fmt.Sprintf("单元格 %d 不存在", req.Index+1)}
	}
	if strings.TrimSpace(nb.Connection) == "" {
		return NotebookRunResult{Error: "笔记本没有绑定连接"}
	}
	cells := make([]sqltext.NotebookCell, 0, len(nb.Cells))
	for _, c := range nb.Cells {
		cells = append(cells, sqltext.NotebookCell{SQL: c.SQL})
	}
	stmt, err := sqltext.ExpandNotebook(cells, req.Index)
	if err != nil {
		// The kernel's messages already name the cell and the problem.
		return NotebookRunResult{Error: err.Error()}
	}
	deps := make([]int, 0, req.Index)
	for _, n := range sqltext.CellReferences(nb.Cells[req.Index].SQL) {
		deps = append(deps, n)
	}
	result := a.ExecuteQuery(nb.Connection, stmt, nb.Database)
	return NotebookRunResult{Result: result, Statement: stmt, DependsOn: deps}
}

// trimBOMBytes drops a UTF-8 BOM: a notebooks.json written by Notepad would
// otherwise fail to parse, and the failure would look like data loss.
func trimBOMBytes(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

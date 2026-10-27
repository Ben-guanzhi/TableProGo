package main

// Import: CSV/JSON/SQL files into the database.
//
// The parsing and SQL rendering live in internal/importfile and
// internal/sqlscript (pure, package-tested); this file is the wiring: read the
// connection, run the statements through the shared kernel driver, and report
// what happened.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dbcore/dbcore"
	tableprosql "github.com/dbcore/dbcore/sqltext"

	"tablepro-go/internal/importfile"
	"tablepro-go/internal/sqlscript"
)

// ImportOptions controls parsing and writing.
type ImportOptions struct {
	// Format is "csv", "json" or "sql"; empty means infer from the file extension.
	Format string `json:"format"`
	// HasHeader says the first CSV row holds column names (default true for CSV).
	HasHeader bool `json:"hasHeader"`
	// Delimiter is the CSV separator (default ",").
	Delimiter string `json:"delimiter"`
	// CreateTable creates the target table when it does not exist.
	CreateTable bool `json:"createTable"`
	// InferTypes picks INTEGER/REAL/TEXT for the created columns.
	InferTypes bool `json:"inferTypes"`
	// SkipErrors continues after a failing row instead of rolling the batch back.
	SkipErrors bool `json:"skipErrors"`
	// Mode is "append" (default), "truncate" (empty the table first) or
	// "upsert" (conflict on UpsertKeys updates the existing row).
	Mode string `json:"mode"`
	// UpsertKeys are the conflict columns for Mode="upsert"; empty means the
	// table's primary key.
	UpsertKeys []string `json:"upsertKeys"`
}

// ImportRequest is the API payload for one import.
type ImportRequest struct {
	ConnID   string        `json:"connId"`
	Database string        `json:"database"`
	Table    string        `json:"table"`
	Path     string        `json:"path"`
	Options  ImportOptions `json:"options"`
}

// ImportResult summarises one import run.
type ImportResult struct {
	Rows         int      `json:"rows"`
	Skipped      int      `json:"skipped"`
	Statements   int      `json:"statements"`
	Columns      []string `json:"columns"`
	CreatedTable bool     `json:"createdTable"`
	// Mapped is the file-column → table-column pairing when the import landed
	// on an existing table (MatchColumns). Empty when the table was created
	// from the file, where the two lists are identical by construction.
	Mapped []importfile.ColumnMap `json:"mapped,omitempty"`
	// FirstError is the first skipped row's error with its 1-based row number,
	// so "skipped 300" is not a dead end. Empty when nothing was skipped.
	FirstError string `json:"firstError,omitempty"`
	Error      string `json:"error,omitempty"`
}

// importBatchSize caps how many rows go into one multi-row INSERT: large enough
// to amortize round trips, small enough to stay under every engine's parameter
// limit (SQLite's is the tightest at 32766).
const importBatchSize = 200

// ImportFile parses path and writes its rows into table. A .sql path runs as a
// script instead, where the file itself decides the tables.
//
// CSV rows stream in chunks (importfile.CSVStream), so memory stays bounded by
// one chunk rather than the whole file; JSON is materialized (its array format
// needs the whole document anyway). Either way the run is one transaction
// unless skipErrors was asked for.
func (a *App) ImportFile(req ImportRequest) (ImportResult, error) {
	res := ImportResult{}
	opts := importfile.Normalize(importfile.Options{
		Format:     req.Options.Format,
		HasHeader:  req.Options.HasHeader,
		Delimiter:  req.Options.Delimiter,
		InferTypes: req.Options.InferTypes,
		Mode:       req.Options.Mode,
		UpsertKeys: req.Options.UpsertKeys,
	}, req.Path)
	if opts.Format == "sql" {
		return a.ImportSQLScript(ImportRequest{
			ConnID: req.ConnID, Database: req.Database, Path: req.Path, Options: req.Options,
		})
	}
	if req.Table == "" {
		return res, fmt.Errorf("import: target table is required")
	}

	conn, err := a.connection(req.ConnID)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	drv, err := a.execDriver(conn, req.Database)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	ctx, done := a.beginRun(req.ConnID)
	defer done()

	if opts.Mode == "truncate" {
		stmt, terr := tableprosql.TruncateStatement(conn.Type, importfile.Ident(conn.Type, req.Table))
		if terr != nil {
			res.Error = terr.Error()
			return res, terr
		}
		if _, err := drv.Execute(ctx, stmt); err != nil {
			res.Error = err.Error()
			return res, err
		}
	}

	w := &importWriter{
		ctx: ctx, drv: drv, dbType: conn.Type, table: req.Table,
		opts: opts, skipErrors: req.Options.SkipErrors, res: &res,
	}
	if err := w.begin(); err != nil {
		res.Error = err.Error()
		return res, err
	}
	if opts.Format == "json" {
		err = a.importJSONRows(req, opts, w)
	} else {
		err = a.importCSVRows(req, opts, w)
	}
	if err == nil {
		err = w.finish()
	} else {
		w.abort()
	}
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	// One summary entry per run: import statements are generated, so the
	// counts are the auditable part, not the SQL text.
	summary := fmt.Sprintf("-- 导入 %s：%d 行%s", req.Table, res.Rows,
		map[bool]string{true: fmt.Sprintf("（跳过 %d，首错：%s）", res.Skipped, res.FirstError), false: ""}[res.Skipped > 0])
	a.recordStatement(req.ConnID, req.Database, summary,
		QueryResult{AffectedRows: int64(res.Rows)})
	return res, nil
}

// importWriter owns the execution side of one import run. Everything goes
// through one transaction unless skipErrors was asked for, in which case rows
// are written in autocommit batches and the first failure is captured (with
// its 1-based row number) instead of aborting the run.
type importWriter struct {
	ctx        context.Context
	drv        dbcore.SQLDriver
	txDrv      dbcore.SQLTxDriver // nil without transaction support or with skipErrors
	tx         *sql.Tx
	dbType     string
	table      string
	opts       importfile.Options
	skipErrors bool
	res        *ImportResult
	cols       []string
	// keep lists the file-column indexes that landed on table columns (column
	// mapping); nil when every file column is kept. Each written row is
	// projected down to these indexes so values stay paired with w.cols.
	keep []int
}

func (w *importWriter) begin() error {
	if w.skipErrors {
		return nil
	}
	d, ok := w.drv.(dbcore.SQLTxDriver)
	if !ok {
		return nil // no transaction support: autocommit batches, as before
	}
	w.txDrv = d
	tx, err := d.Begin(w.ctx, dbcore.TxMode{})
	if err != nil {
		return err
	}
	w.tx = tx
	return nil
}

func (w *importWriter) finish() error {
	if w.txDrv == nil {
		return nil
	}
	tx := w.tx
	w.tx = nil
	return tx.Commit()
}

func (w *importWriter) abort() {
	if w.txDrv == nil || w.tx == nil {
		return
	}
	_ = w.tx.Rollback()
	w.tx = nil
}

// write sends one chunk of rows. firstRow is the chunk's 1-based position in
// the file, so an error names the rows the user can find.
func (w *importWriter) write(chunk [][]*string, firstRow int) error {
	if len(chunk) == 0 {
		return nil
	}
	if w.keep != nil {
		// Column mapping dropped some file columns: project every row onto the
		// kept indexes so values stay paired with the mapped w.cols.
		projected := make([][]*string, len(chunk))
		for i, row := range chunk {
			p := make([]*string, len(w.keep))
			for j, idx := range w.keep {
				if idx < len(row) {
					p[j] = row[idx]
				}
			}
			projected[i] = p
		}
		chunk = projected
	}
	batch := importBatchSize
	if w.dbType == "oracle" {
		// Oracle has no multi-row VALUES clause on older versions; one row per
		// statement keeps the import correct at the cost of round trips.
		batch = 1
	}
	for start := 0; start < len(chunk); start += batch {
		end := minInt(start+batch, len(chunk))
		stmt, args, err := importfile.InsertSQL(w.dbType, w.table, w.cols, chunk[start:end], w.opts)
		if err != nil {
			return err
		}
		if w.txDrv != nil {
			if _, err := w.txDrv.ExecuteIn(w.tx, w.ctx, stmt, args...); err != nil {
				return fmt.Errorf("import: insert rows %d-%d: %w", firstRow+start, firstRow+end-1, err)
			}
		} else if _, err := w.drv.Execute(w.ctx, stmt, args...); err != nil {
			if !w.skipErrors {
				return fmt.Errorf("import: insert rows %d-%d: %w", firstRow+start, firstRow+end-1, err)
			}
			// Retry the failed batch row by row so one bad row does not lose
			// the rest; the first failure is reported, not swallowed.
			for i := start; i < end; i++ {
				one, oneArgs, err := importfile.InsertSQL(w.dbType, w.table, w.cols, chunk[i:i+1], w.opts)
				if err != nil {
					return err
				}
				if _, err := w.drv.Execute(w.ctx, one, oneArgs...); err != nil {
					if w.res.FirstError == "" {
						w.res.FirstError = fmt.Sprintf("行 %d: %s", firstRow+i, err.Error())
					}
					w.res.Skipped++
					continue
				}
				w.res.Rows++
			}
			continue
		}
		w.res.Rows += end - start
		w.res.Statements++
	}
	return nil
}

// importCSVRows streams the file chunk by chunk. The first chunk doubles as
// the setup trigger (create table / upsert keys) and the type-inference sample.
func (a *App) importCSVRows(req ImportRequest, opts importfile.Options, w *importWriter) error {
	f, err := os.Open(req.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	base := 0 // 1-based file position of the next chunk's first row
	return importfile.CSVStream(f, opts, func(cols []string, chunk [][]*string, isFirst bool) error {
		if isFirst {
			if len(cols) == 0 {
				return fmt.Errorf("import: file is empty")
			}
			w.res.Columns = cols
			if err := a.prepareImport(w, req, opts, cols, chunk); err != nil {
				return err
			}
		}
		if err := w.write(chunk, base+1); err != nil {
			return err
		}
		base += len(chunk)
		return nil
	})
}

// importJSONRows materializes the JSON document (its array format needs the
// whole thing anyway) and writes it through the same chunked writer.
func (a *App) importJSONRows(req ImportRequest, opts importfile.Options, w *importWriter) error {
	f, err := os.Open(req.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	cols, rows, err := importfile.ParseJSON(importfile.DecodingReader(f))
	if err != nil {
		return err
	}
	w.res.Columns = cols
	if len(cols) == 0 {
		return fmt.Errorf("import: no columns found in %s", filepath.Base(req.Path))
	}
	if err := a.prepareImport(w, req, opts, cols, rows); err != nil {
		return err
	}
	for start := 0; start < len(rows); start += importfile.StreamChunkSize {
		end := minInt(start+importfile.StreamChunkSize, len(rows))
		if err := w.write(rows[start:end], start+1); err != nil {
			return err
		}
	}
	return nil
}

// prepareImport runs the once-per-import setup on the first chunk: create the
// table when asked (the chunk is the inference sample), align the file's
// columns with an existing table's catalog, and resolve the upsert conflict
// keys against the real columns.
func (a *App) prepareImport(w *importWriter, req ImportRequest, opts importfile.Options, cols []string, sample [][]*string) error {
	w.cols = cols
	created := false
	if req.Options.CreateTable {
		var err error
		created, err = a.ensureImportTable(w.ctx, w.drv, w.dbType, req.Database, req.Table, cols, sample, opts)
		if err != nil {
			return err
		}
		w.res.CreatedTable = created
	}
	if !created {
		// Landing on a table that already exists: the file's headers are not
		// automatically the table's columns. The mapping is reported in
		// res.Mapped, not guessed silently.
		if err := a.mapImportColumns(w, req, cols); err != nil {
			return err
		}
	}
	if opts.Mode == "upsert" {
		// User-named keys may spell file headers; translate them through the
		// mapping so the conflict clause names real table columns.
		keys := make([]string, len(opts.UpsertKeys))
		byFile := make(map[string]string, len(w.res.Mapped))
		for _, m := range w.res.Mapped {
			if m.Table != "" {
				byFile[m.File] = m.Table
			}
		}
		for i, k := range opts.UpsertKeys {
			if mapped, ok := byFile[k]; ok {
				k = mapped
			}
			keys[i] = k
		}
		resolved, err := a.resolveUpsertKeys(w.ctx, w.drv, w.dbType, req.Database, req.Table, w.cols, keys)
		if err != nil {
			return err
		}
		w.opts.UpsertKeys = resolved
	}
	return nil
}

// mapImportColumns aligns the file's columns with the target table's catalog:
// the file may spell its headers differently (case, separators), hold the
// columns in another order, or carry columns the table does not have — a
// straight "headers are the column list" INSERT fails on all three. A
// headerless file maps positionally when the arity agrees. File columns with
// no counterpart are dropped from the INSERT and the pairing is reported.
func (a *App) mapImportColumns(w *importWriter, req ImportRequest, fileCols []string) error {
	cols, err := w.drv.ListColumns(w.ctx, importSchema(w.dbType, req.Database), req.Table)
	if err != nil {
		return fmt.Errorf("import: 读取表 %s 的列失败：%w", req.Table, err)
	}
	if len(cols) == 0 {
		return fmt.Errorf("import: 目标表 %s 不存在（或没有列）", req.Table)
	}
	tableNames := make([]string, len(cols))
	for i, c := range cols {
		tableNames[i] = c.Name
	}
	mapping := importfile.MatchColumns(fileCols, tableNames, w.opts.HasHeader)
	kept := make([]string, 0, len(mapping))
	keepIdx := make([]int, 0, len(mapping))
	for i, m := range mapping {
		if m.Table == "" {
			continue
		}
		kept = append(kept, m.Table)
		keepIdx = append(keepIdx, i)
	}
	if len(kept) == 0 {
		return fmt.Errorf("import: 文件的列与表 %s 的列没有对应关系（文件 %v / 表 %v）", req.Table, fileCols, tableNames)
	}
	w.res.Mapped = mapping
	if len(keepIdx) == len(fileCols) {
		w.cols = kept // 1:1, order included: only the spellings changed
		return nil
	}
	w.cols = kept
	w.keep = keepIdx // the writer projects each row down to the kept columns
	return nil
}

// importSchema is the catalog namespace ListTables expects.
func importSchema(dbType, database string) string {
	switch dbType {
	case "mysql":
		return database
	case "postgresql":
		return "public"
	case "sqlserver":
		return "dbo"
	}
	return ""
}

// ensureImportTable creates the table when missing and reports whether it did.
func (a *App) ensureImportTable(ctx context.Context, drv dbcore.SQLDriver, dbType, database, table string, cols []string, rows [][]*string, opts importfile.Options) (bool, error) {
	tables, err := drv.ListTables(ctx, importSchema(dbType, database))
	if err != nil {
		return false, err
	}
	for _, t := range tables {
		if t.Name == table {
			return false, nil
		}
	}
	kinds := make([]string, len(cols))
	for i := range kinds {
		kinds[i] = "text"
	}
	if opts.InferTypes {
		kinds = importfile.InferTypes(cols, rows)
	}
	if _, err := drv.Execute(ctx, importfile.CreateTableSQL(dbType, table, cols, kinds)); err != nil {
		return false, fmt.Errorf("import: create table: %w", err)
	}
	return true, nil
}

// resolveUpsertKeys decides the conflict target. Explicit keys are validated
// against the imported columns; otherwise the table's primary key is used —
// *not* every column, because "ON CONFLICT (id, name)" does not match a primary
// key on id alone and the server rejects it.
func (a *App) resolveUpsertKeys(ctx context.Context, drv dbcore.SQLDriver, dbType, database, table string, cols, requested []string) ([]string, error) {
	known := make(map[string]bool, len(cols))
	for _, c := range cols {
		known[c] = true
	}
	if len(requested) > 0 {
		for _, k := range requested {
			if !known[k] {
				return nil, fmt.Errorf("import: upsert key %q is not one of the imported columns", k)
			}
		}
		return requested, nil
	}
	columns, err := drv.ListColumns(ctx, importSchema(dbType, database), table)
	if err != nil {
		return nil, fmt.Errorf("import: read primary key of %s: %w", table, err)
	}
	var keys []string
	for _, c := range columns {
		if c.IsPrimaryKey && known[c.Name] {
			keys = append(keys, c.Name)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("import: %s has no primary key among the imported columns; upsert needs one (create the table with a PRIMARY KEY, or use append/truncate)", table)
	}
	return keys, nil
}

// SplitSQLScript splits text into statements at top-level semicolons; kept as a
// package-level helper because the editor's script handling uses it too.
func SplitSQLScript(text string) []string { return sqlscript.Split(text) }

// minInt avoids importing math for one call site.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

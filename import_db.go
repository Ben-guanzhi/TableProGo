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
	"slices"
	"strings"

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
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	// Schema is the catalog namespace the import lands in. Empty means the
	// connection's own default (MySQL: the database, PostgreSQL: public,
	// SQL Server: dbo), so SQLite and Oracle keep writing unqualified names.
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Path   string `json:"path"`
	// Paths adds files; when it is set Path is ignored and each file is a
	// separate work unit landing in Table (or in its own sheet table for xlsx).
	Paths []string `json:"paths,omitempty"`
	// Sheet selects one worksheet of an xlsx workbook. Empty means the first
	// sheet, or every sheet when the workbook has more than one.
	Sheet  string        `json:"sheet,omitempty"`
	Sheets []SheetImport `json:"sheets,omitempty"`
	// ColumnMap is the user's explicit file-column → table-column mapping. When
	// it is non-empty it is authoritative: every file column it does not name
	// is skipped, and it is applied before type inference and CREATE TABLE.
	ColumnMap map[string]string `json:"columnMap,omitempty"`
	// SkipColumns lists file columns to drop. Without a ColumnMap it filters the
	// auto-matched pairing; with one it is redundant (skip is implied).
	SkipColumns []string      `json:"skipColumns,omitempty"`
	Options     ImportOptions `json:"options"`
}

// SheetImport names one worksheet and the table it lands in.
type SheetImport struct {
	Sheet string `json:"sheet"`
	Table string `json:"table"`
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
	// Tables is the per-table breakdown of a multi-table run (one xlsx sheet per
	// table, or one file per table). It is empty for a single-table import, where
	// the flat fields above already carry the whole story.
	Tables []ImportTableResult `json:"tables,omitempty"`
}

// ImportTableResult is one work unit of an aggregated ImportResult.
type ImportTableResult struct {
	Path         string                 `json:"path,omitempty"`
	Table        string                 `json:"table"`
	Sheet        string                 `json:"sheet,omitempty"`
	Rows         int                    `json:"rows"`
	Skipped      int                    `json:"skipped"`
	Statements   int                    `json:"statements"`
	Columns      []string               `json:"columns,omitempty"`
	CreatedTable bool                   `json:"createdTable"`
	Mapped       []importfile.ColumnMap `json:"mapped,omitempty"`
	FirstError   string                 `json:"firstError,omitempty"`
	Error        string                 `json:"error,omitempty"`
}

// importBatchSize caps how many rows go into one multi-row INSERT: large enough
// to amortize round trips. The engine's parameter limit is enforced against the
// real shape by importBatchFor — rows×cols placeholders, so a wide table needs
// a smaller batch than any constant can promise.
const importBatchSize = 200

// importParamCap is an engine's bound on bind parameters in one statement. An
// unknown engine gets the tightest known cap rather than a guess that it is
// larger.
func importParamCap(dbType string) int {
	switch dbType {
	case "mysql", "postgresql":
		return 65535
	case "sqlite":
		return 32766
	default:
		return 2100 // SQL Server; also the safe floor for anything unrecognized
	}
}

// importBatchFor sizes one multi-row INSERT so rows×cols stays under the
// engine's parameter cap: 200 rows × 100 columns into SQL Server would be
// 20 000 placeholders, and with skipErrors the failed batch would drop every
// row in it. Oracle has no multi-row VALUES clause, so it stays at one row per
// statement regardless of width.
func importBatchFor(dbType string, cols int) int {
	if dbType == "oracle" || cols <= 0 {
		return 1
	}
	batch := importParamCap(dbType) / cols
	if batch > importBatchSize {
		return importBatchSize
	}
	if batch < 1 {
		return 1
	}
	return batch
}

// ImportFile parses path and writes its rows into table. A .sql path runs as a
// script instead, where the file itself decides the tables.
//
// CSV rows stream in chunks (importfile.CSVStream), so memory stays bounded by
// one chunk rather than the whole file; JSON and xlsx are materialized. A
// request expands into one work unit per file and, for a multi-sheet workbook,
// one unit per sheet, each in its own transaction so a failure in one sheet
// cannot take the others down. The flat result fields carry the aggregate;
// per-table detail is in res.Tables.
func (a *App) ImportFile(req ImportRequest) (ImportResult, error) {
	res := ImportResult{}
	// A multi-file request carries no single path, so the format is inferred from
	// the first one; an explicit Format still overrides it for every file.
	opts := importfile.Normalize(importfile.Options{
		Format:     req.Options.Format,
		HasHeader:  req.Options.HasHeader,
		Delimiter:  req.Options.Delimiter,
		InferTypes: req.Options.InferTypes,
		Mode:       req.Options.Mode,
		UpsertKeys: req.Options.UpsertKeys,
	}, requestPath(req))
	if opts.Format == "sql" {
		return a.importSQLPaths(req)
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
	// The catalog namespace the run lands in. An explicit req.Schema wins;
	// empty keeps the connection's own default so SQLite/Oracle stay
	// unqualified and existing callers see no change.
	schema := req.Schema
	if schema == "" {
		schema = importSchema(conn.Type, req.Database)
	}
	jobs, err := buildImportJobs(req, opts)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}

	var firstErr error
	failed := 0
	for _, job := range jobs {
		// Each work unit gets its own run and its own transaction, so one
		// sheet's failure is isolated from the others.
		ctx, done := a.beginRunIn(context.Background(), req.ConnID, "import")
		jr, jerr := a.runImportJob(ctx, conn, drv, schema, req, opts, job)
		done()
		res.Rows += jr.Rows
		res.Skipped += jr.Skipped
		res.Statements += jr.Statements
		res.CreatedTable = res.CreatedTable || jr.CreatedTable
		res.Tables = append(res.Tables, jr)
		if len(res.Columns) == 0 {
			res.Columns = jr.Columns
		}
		if len(res.Mapped) == 0 {
			res.Mapped = jr.Mapped
		}
		if jr.FirstError != "" && res.FirstError == "" {
			res.FirstError = jr.FirstError
		}
		if jerr != nil {
			failed++
			if firstErr == nil {
				firstErr = jerr
			}
		}
	}
	if firstErr != nil {
		res.Error = firstErr.Error()
		// A single-table request keeps the original contract — the error is
		// returned and the body carries it too. A multi-table request reports
		// the successful units and keeps the failure in res.Error unless every
		// unit failed.
		if failed == len(jobs) {
			return res, firstErr
		}
	}
	return res, nil
}

// requestPath is the path an import infers its format from: an explicit Path
// wins, otherwise the first entry of Paths.
func requestPath(req ImportRequest) string {
	if req.Path != "" {
		return req.Path
	}
	if len(req.Paths) > 0 {
		return req.Paths[0]
	}
	return ""
}

// importSQLPaths runs one SQL script request per listed path and aggregates the
// results, so a multi-file import behaves the same for scripts as for tables.
func (a *App) importSQLPaths(req ImportRequest) (ImportResult, error) {
	paths := req.Paths
	if len(paths) == 0 && req.Path != "" {
		paths = []string{req.Path}
	}
	if len(paths) == 0 {
		return ImportResult{}, fmt.Errorf("import: 缺少文件路径")
	}
	var res ImportResult
	var firstErr error
	failed := 0
	for _, p := range paths {
		r, err := a.ImportSQLScript(ImportRequest{ConnID: req.ConnID, Database: req.Database, Path: p, Options: req.Options})
		res.Rows += r.Rows
		res.Skipped += r.Skipped
		res.Statements += r.Statements
		res.Tables = append(res.Tables, ImportTableResult{
			Path: p, Rows: r.Rows, Skipped: r.Skipped, Statements: r.Statements,
			FirstError: r.FirstError, Error: r.Error,
		})
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil && failed == len(paths) {
		return res, firstErr
	}
	return res, nil
}

// importJob is one (file, sheet) → table work unit. data is non-nil for xlsx,
// where the sheet is parsed once and shared by every job from that workbook.
type importJob struct {
	path  string
	sheet string
	table string
	data  *importfile.XLSXSheet
}

// buildImportJobs expands a request into work units. An xlsx workbook becomes
// one unit per selected worksheet; when no sheet is named every worksheet is
// imported, each into its own table named after the sheet (a single-sheet
// workbook keeps req.Table). CSV/JSON files always land in req.Table, one unit
// per file when Paths is used.
func buildImportJobs(req ImportRequest, opts importfile.Options) ([]importJob, error) {
	paths := req.Paths
	if len(paths) == 0 && req.Path != "" {
		paths = []string{req.Path}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("import: 缺少文件路径")
	}
	var jobs []importJob
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if opts.Format != "xlsx" {
			jobs = append(jobs, importJob{path: path, table: req.Table})
			continue
		}
		sheets, err := importfile.ParseXLSXFile(path)
		if err != nil {
			return nil, err
		}
		if len(sheets) == 0 {
			return nil, fmt.Errorf("import: %s 没有工作表", filepath.Base(path))
		}
		switch {
		case len(req.Sheets) > 0:
			for _, want := range req.Sheets {
				data := findSheet(sheets, want.Sheet)
				if data == nil {
					return nil, fmt.Errorf("import: 找不到工作表 %q", want.Sheet)
				}
				table := want.Table
				if table == "" {
					table = importfile.SanitizeSheetName(data.Name)
				}
				jobs = append(jobs, importJob{path: path, sheet: data.Name, table: table, data: data})
			}
		case req.Sheet != "":
			data := findSheet(sheets, req.Sheet)
			if data == nil {
				return nil, fmt.Errorf("import: 找不到工作表 %q", req.Sheet)
			}
			table := req.Table
			if table == "" {
				table = importfile.SanitizeSheetName(data.Name)
			}
			jobs = append(jobs, importJob{path: path, sheet: data.Name, table: table, data: data})
		default:
			for i := range sheets {
				table := req.Table
				if table == "" || len(sheets) > 1 {
					table = importfile.SanitizeSheetName(sheets[i].Name)
				}
				jobs = append(jobs, importJob{path: path, sheet: sheets[i].Name, table: table, data: &sheets[i]})
			}
		}
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("import: 没有可导入的工作单元")
	}
	return jobs, nil
}

func findSheet(sheets []importfile.XLSXSheet, name string) *importfile.XLSXSheet {
	for i := range sheets {
		if sheets[i].Name == name {
			return &sheets[i]
		}
	}
	return nil
}

// runImportJob drives one work unit: open its transaction, truncate if asked,
// parse, write, commit and record the statement summary. It returns the unit's
// result and the first error; the caller keeps going when other units remain.
func (a *App) runImportJob(ctx context.Context, conn *Connection, drv dbcore.SQLDriver, schema string, req ImportRequest, opts importfile.Options, job importJob) (ImportTableResult, error) {
	jr := ImportTableResult{Table: job.table, Sheet: job.sheet}
	if job.table == "" {
		err := fmt.Errorf("import: 目标表名不能为空")
		jr.Error = err.Error()
		return jr, err
	}
	res := ImportResult{}
	w := &importWriter{
		ctx: ctx, drv: drv, dbType: conn.Type, schema: schema, table: job.table,
		opts: opts, skipErrors: req.Options.SkipErrors, res: &res,
	}
	// Open the import's transaction BEFORE the destructive truncate, so the
	// truncate is part of it and rolls back with the row inserts when a later
	// row fails. Executing the truncate first (autocommit) used to leave the
	// table empty and the failed import's rows gone for good (TP-03).
	if err := w.begin(); err != nil {
		jr.Error = err.Error()
		return jr, err
	}
	if opts.Mode == "truncate" {
		if w.txDrv == nil {
			// There is no transaction to protect the truncate: with skipErrors
			// (or a driver without transaction support) every row is its own
			// statement, so a failure cannot rewind the truncate. Refuse rather
			// than truncate a table and lose its contents on the next bad row.
			w.abort()
			err := fmt.Errorf("import: truncate mode needs transaction support; turn off skip-errors")
			jr.Error = err.Error()
			return jr, err
		}
		stmt, terr := tableprosql.TruncateStatement(conn.Type, importfile.TableRef(conn.Type, schema, job.table))
		if terr != nil {
			w.abort()
			jr.Error = terr.Error()
			return jr, terr
		}
		if _, err := w.txDrv.ExecuteIn(w.tx, ctx, stmt); err != nil {
			w.abort()
			jr.Error = err.Error()
			return jr, err
		}
	}
	var jerr error
	switch {
	case opts.Format == "json":
		jerr = a.importJSONRows(job.path, req, opts, w)
	case opts.Format == "xlsx":
		jerr = a.importXLSXRows(job, req, opts, w)
	default:
		jerr = a.importCSVRows(job.path, req, opts, w)
	}
	if jerr == nil {
		jerr = w.finish()
	} else {
		w.abort()
	}
	jr.Rows = res.Rows
	jr.Skipped = res.Skipped
	jr.Statements = res.Statements
	jr.Columns = res.Columns
	jr.CreatedTable = res.CreatedTable
	jr.Mapped = res.Mapped
	jr.FirstError = res.FirstError
	if jerr != nil {
		jr.Error = jerr.Error()
	}
	// One summary entry per work unit that did something: import statements are
	// generated, so the counts are the auditable part, not the SQL text.
	if jerr == nil || res.Rows > 0 || res.Skipped > 0 {
		label := job.table
		if job.sheet != "" {
			label = job.sheet + " → " + job.table
		}
		summary := fmt.Sprintf("-- 导入 %s：%d 行%s", label, res.Rows,
			map[bool]string{true: fmt.Sprintf("（跳过 %d，首错：%s）", res.Skipped, res.FirstError), false: ""}[res.Skipped > 0])
		a.recordStatement(req.ConnID, req.Database, summary,
			QueryResult{AffectedRows: int64(res.Rows)})
	}
	return jr, jerr
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
	schema     string
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
	batch := importBatchFor(w.dbType, len(w.cols))
	for start := 0; start < len(chunk); start += batch {
		end := minInt(start+batch, len(chunk))
		stmt, args, err := importfile.InsertSQL(w.dbType, w.schema, w.table, w.cols, chunk[start:end], w.opts)
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
				one, oneArgs, err := importfile.InsertSQL(w.dbType, w.schema, w.table, w.cols, chunk[i:i+1], w.opts)
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
				// Each retried row is its own statement: without this the
				// summary undercounts executed statements in skipErrors mode.
				w.res.Statements++
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
func (a *App) importCSVRows(path string, req ImportRequest, opts importfile.Options, w *importWriter) error {
	f, err := os.Open(path)
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
func (a *App) importJSONRows(path string, req ImportRequest, opts importfile.Options, w *importWriter) error {
	f, err := os.Open(path)
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
		return fmt.Errorf("import: no columns found in %s", filepath.Base(path))
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

// importXLSXRows writes the selected worksheet through the same chunked
// writer. The workbook is already parsed by buildImportJobs, so a multi-sheet
// request parses it once instead of once per sheet.
func (a *App) importXLSXRows(job importJob, req ImportRequest, opts importfile.Options, w *importWriter) error {
	data := job.data
	if data == nil {
		sheets, err := importfile.ParseXLSXFile(job.path)
		if err != nil {
			return err
		}
		data = findSheet(sheets, job.sheet)
		if data == nil {
			return fmt.Errorf("import: 找不到工作表 %q", job.sheet)
		}
	}
	if data.Empty() {
		return fmt.Errorf("import: 工作表 %q 没有列", data.Name)
	}
	w.res.Columns = data.Cols
	rows := data.Rows
	sample := rows
	if len(sample) > importfile.StreamChunkSize {
		sample = sample[:importfile.StreamChunkSize]
	}
	if err := a.prepareImport(w, req, opts, data.Cols, sample); err != nil {
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

// prepareImport runs the once-per-import setup on the first chunk: apply the
// user's column mapping (authoritative when one was supplied), create the table
// when asked (the chunk is the inference sample), align the file's columns with
// an existing table's catalog, and resolve the upsert conflict keys against the
// real columns.
func (a *App) prepareImport(w *importWriter, req ImportRequest, opts importfile.Options, cols []string, sample [][]*string) error {
	w.cols = cols
	specs, authoritative, err := explicitColumnSpecs(req, cols)
	if err != nil {
		return err
	}
	if authoritative {
		resolved, keep := importfile.BuildColumnMap(specs, cols)
		if len(resolved) == 0 {
			return fmt.Errorf("import: 映射后没有可导入的列")
		}
		w.cols = resolved
		w.keep = keep
		w.res.Mapped = importfile.MappedColumns(specs, cols)
	} else if len(req.SkipColumns) > 0 {
		// Explicit skips apply before the table is created, so a skipped column
		// never reaches CREATE TABLE (and never reaches the header auto-match
		// against an existing table either).
		mapping := identityMapping(cols)
		importfile.DropSources(mapping, req.SkipColumns)
		w.cols = importfile.KeptTargets(mapping)
		w.keep = importfile.KeptIndexes(mapping)
		w.res.Mapped = mapping
	}
	created := false
	if req.Options.CreateTable {
		// The mapping may have dropped or reordered columns, so the inference
		// sample is projected onto the kept indexes before the CREATE.
		sampleForInference := sample
		if w.keep != nil {
			sampleForInference = projectRows(sample, w.keep)
		}
		var cerr error
		created, cerr = a.ensureImportTable(w.ctx, w.drv, w.dbType, w.schema, w.table, w.cols, sampleForInference, opts)
		if cerr != nil {
			return cerr
		}
		w.res.CreatedTable = created
	}
	if !created {
		if authoritative {
			// The mapping is explicit, so no auto-matching happens; the targets
			// still have to name real table columns or the INSERT fails late.
			if err := a.verifyExplicitTargets(w); err != nil {
				return err
			}
		} else {
			// Landing on a table that already exists: the file's headers are not
			// automatically the table's columns. The mapping is reported in
			// res.Mapped, not guessed silently.
			if err := a.mapImportColumns(w, req, cols); err != nil {
				return err
			}
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
		resolved, err := a.resolveUpsertKeys(w.ctx, w.drv, w.dbType, w.schema, w.table, w.cols, keys)
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
	cols, err := w.drv.ListColumns(w.ctx, w.schema, w.table)
	if err != nil {
		return fmt.Errorf("import: 读取表 %s 的列失败：%w", importName(w.dbType, w.schema, w.table), err)
	}
	if len(cols) == 0 {
		return fmt.Errorf("import: 目标表 %s 不存在（或没有列）", importName(w.dbType, w.schema, w.table))
	}
	tableNames := make([]string, len(cols))
	for i, c := range cols {
		tableNames[i] = c.Name
	}
	mapping := importfile.MatchColumns(fileCols, tableNames, w.opts.HasHeader)
	importfile.DropSources(mapping, req.SkipColumns)
	kept := importfile.KeptTargets(mapping)
	keepIdx := importfile.KeptIndexes(mapping)
	if len(kept) == 0 {
		return fmt.Errorf("import: 文件的列与表 %s 的列没有对应关系（文件 %v / 表 %v）", w.table, fileCols, tableNames)
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

// explicitColumnSpecs turns the request's columnMap/skipColumns into the
// authoritative spec list used before type inference and CREATE TABLE. The
// bool reports whether a mapping was supplied (authoritative) or the caller
// should fall back to auto-matching.
func explicitColumnSpecs(req ImportRequest, fileCols []string) ([]importfile.ColumnMapSpec, bool, error) {
	if len(req.ColumnMap) == 0 && len(req.SkipColumns) == 0 {
		return nil, false, nil
	}
	if len(req.ColumnMap) > 0 {
		// Authoritative: every file column is either listed or skipped, and a
		// source the map names must really be a file column (a typo would
		// otherwise silently drop a column).
		for src := range req.ColumnMap {
			if !slices.Contains(fileCols, src) {
				return nil, false, fmt.Errorf("import: 映射的源列 %q 不在文件列中", src)
			}
		}
		specs := make([]importfile.ColumnMapSpec, 0, len(fileCols))
		for _, c := range fileCols {
			if slices.Contains(req.SkipColumns, c) {
				specs = append(specs, importfile.ColumnMapSpec{Source: c})
				continue
			}
			target, ok := req.ColumnMap[c]
			if !ok {
				specs = append(specs, importfile.ColumnMapSpec{Source: c}) // skipped
				continue
			}
			specs = append(specs, importfile.ColumnMapSpec{Source: c, Target: strings.TrimSpace(target)})
		}
		if err := importfile.ValidateColumnMap(specs, fileCols); err != nil {
			return nil, false, err
		}
		return specs, true, nil
	}
	// Skip-only: not authoritative. Auto-matching fills in the pairing and
	// mapImportColumns drops the named sources.
	return nil, false, nil
}

// identityMapping builds the no-op pairing (every file column maps to itself),
// the base the explicit-skip filter starts from.
func identityMapping(cols []string) []importfile.ColumnMap {
	m := make([]importfile.ColumnMap, len(cols))
	for i, c := range cols {
		m[i] = importfile.ColumnMap{File: c, Table: c}
	}
	return m
}

// projectRows projects each row onto the keep indexes so a row that carries
// more file columns than the target (or fewer) stays paired with w.cols after a
// column mapping dropped or reordered columns.
func projectRows(rows [][]*string, keep []int) [][]*string {
	if keep == nil {
		return rows
	}
	out := make([][]*string, len(rows))
	for i, row := range rows {
		p := make([]*string, len(keep))
		for j, idx := range keep {
			if idx < len(row) {
				p[j] = row[idx]
			}
		}
		out[i] = p
	}
	return out
}

// verifyExplicitTargets checks that every mapped target names a real table
// column when the target table already exists. An explicit mapping skips
// auto-matching, so a target the table does not have would only fail at INSERT
// time, far from the mapping the user wrote.
func (a *App) verifyExplicitTargets(w *importWriter) error {
	cols, err := w.drv.ListColumns(w.ctx, w.schema, w.table)
	if err != nil {
		return fmt.Errorf("import: 读取表 %s 的列失败：%w", importName(w.dbType, w.schema, w.table), err)
	}
	if len(cols) == 0 {
		return fmt.Errorf("import: 目标表 %s 不存在（或没有列）", importName(w.dbType, w.schema, w.table))
	}
	known := make(map[string]bool, len(cols))
	for _, c := range cols {
		known[c.Name] = true
	}
	for _, c := range w.cols {
		if !known[c] {
			return fmt.Errorf("import: 映射目标列 %q 不在表 %s 中", c, importName(w.dbType, w.schema, w.table))
		}
	}
	return nil
}

// importSchema is the catalog namespace ListTables expects when the caller did
// not name one: MySQL names its database as the schema, PostgreSQL's
// conventional default is public, SQL Server's is dbo, and the rest have no
// schema layer at all.
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

// importName renders the target for a user-facing message. An empty schema
// leaves just the table, so the message reads the same as before.
func importName(dbType, schema, table string) string {
	if schema == "" {
		return table
	}
	q, err := qualifiedName(dbType, schema, table)
	if err != nil {
		return schema + "." + table
	}
	return q
}

// ensureImportTable creates the table when missing and reports whether it did.
func (a *App) ensureImportTable(ctx context.Context, drv dbcore.SQLDriver, dbType, schema, table string, cols []string, rows [][]*string, opts importfile.Options) (bool, error) {
	tables, err := drv.ListTables(ctx, schema)
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
	if _, err := drv.Execute(ctx, importfile.CreateTableSQL(dbType, schema, table, cols, kinds)); err != nil {
		return false, fmt.Errorf("import: create table: %w", err)
	}
	return true, nil
}

// resolveUpsertKeys decides the conflict target. Explicit keys are validated
// against the imported columns; otherwise the table's primary key is used —
// *not* every column, because "ON CONFLICT (id, name)" does not match a primary
// key on id alone and the server rejects it.
func (a *App) resolveUpsertKeys(ctx context.Context, drv dbcore.SQLDriver, dbType, schema, table string, cols, requested []string) ([]string, error) {
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
	columns, err := drv.ListColumns(ctx, schema, table)
	if err != nil {
		return nil, fmt.Errorf("import: read primary key of %s: %w", importName(dbType, schema, table), err)
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

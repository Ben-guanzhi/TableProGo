package main

// Import: CSV/JSON/SQL files into the database.
//
// The parsing and SQL rendering live in internal/importfile and
// internal/sqlscript (pure, package-tested); this file is the wiring: read the
// connection, run the statements through the shared kernel driver, and report
// what happened.

import (
	"context"
	"fmt"
	"path/filepath"
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
	Error        string   `json:"error,omitempty"`
}

// importBatchSize caps how many rows go into one multi-row INSERT: large enough
// to amortize round trips, small enough to stay under every engine's parameter
// limit (SQLite's is the tightest at 32766).
const importBatchSize = 200

// ImportFile parses path and writes its rows into table. A .sql path runs as a
// script instead, where the file itself decides the tables.
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
	cols, rows, err := importfile.Parse(req.Path, opts)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.Columns = cols
	if len(cols) == 0 {
		err := fmt.Errorf("import: no columns found in %s", filepath.Base(req.Path))
		res.Error = err.Error()
		return res, err
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
	ctx := context.Background()

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

	if req.Options.CreateTable {
		created, err := a.ensureImportTable(ctx, drv, conn.Type, req.Database, req.Table, cols, rows, opts)
		if err != nil {
			res.Error = err.Error()
			return res, err
		}
		res.CreatedTable = created
	}

	if opts.Mode == "upsert" {
		keys, err := a.resolveUpsertKeys(ctx, drv, conn.Type, req.Database, req.Table, cols, opts.UpsertKeys)
		if err != nil {
			res.Error = err.Error()
			return res, err
		}
		opts.UpsertKeys = keys
	}

	if err := a.insertImportedRows(ctx, drv, conn.Type, req.Database, req.Table, cols, rows, opts, req.Options.SkipErrors, &res); err != nil {
		res.Error = err.Error()
		return res, err
	}
	return res, nil
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

// insertImportedRows writes the parsed rows, batched when the engine allows a
// multi-row VALUES list. With skipErrors the batch is not wrapped in a
// transaction and failing rows are counted instead of aborting.
func (a *App) insertImportedRows(ctx context.Context, drv dbcore.SQLDriver, dbType, database, table string, cols []string, rows [][]*string, opts importfile.Options, skipErrors bool, res *ImportResult) error {
	if len(rows) == 0 {
		return nil
	}
	batch := importBatchSize
	if dbType == "oracle" {
		// Oracle has no multi-row VALUES clause on older versions; one row per
		// statement keeps the import correct at the cost of round trips.
		batch = 1
	}
	txDrv, hasTx := drv.(dbcore.SQLTxDriver)
	if hasTx && !skipErrors {
		tx, err := txDrv.Begin(ctx, dbcore.TxMode{})
		if err != nil {
			return err
		}
		for start := 0; start < len(rows); start += batch {
			end := minInt(start+batch, len(rows))
			stmt, args, err := importfile.InsertSQL(dbType, table, cols, rows[start:end], opts)
			if err != nil {
				_ = tx.Rollback()
				return err
			}
			if _, err := txDrv.ExecuteIn(tx, ctx, stmt, args...); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("import: insert rows %d-%d: %w", start+1, end, err)
			}
			res.Rows += end - start
			res.Statements++
		}
		return tx.Commit()
	}
	for start := 0; start < len(rows); start += batch {
		end := minInt(start+batch, len(rows))
		stmt, args, err := importfile.InsertSQL(dbType, table, cols, rows[start:end], opts)
		if err != nil {
			return err
		}
		if _, err := drv.Execute(ctx, stmt, args...); err != nil {
			if !skipErrors {
				return fmt.Errorf("import: insert rows %d-%d: %w", start+1, end, err)
			}
			// Retry the batch row by row so one bad row does not lose the rest.
			for i := start; i < end; i++ {
				one, oneArgs, err := importfile.InsertSQL(dbType, table, cols, rows[i:i+1], opts)
				if err != nil {
					return err
				}
				if _, err := drv.Execute(ctx, one, oneArgs...); err != nil {
					res.Skipped++
					continue
				}
				res.Rows++
			}
			res.Statements++
			continue
		}
		res.Rows += end - start
		res.Statements++
	}
	return nil
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

var _ = strings.TrimSpace

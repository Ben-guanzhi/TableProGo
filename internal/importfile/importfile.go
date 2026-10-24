// Package importfile parses CSV/JSON import files and renders the engine-specific
// SQL that writes them.
//
// It holds the pure half of the import feature: no App, no connection, no I/O
// beyond reading the file it is given. The impure half (transactions, driver
// calls) stays in the client, which is why the functions here are cheap to test
// and reusable by another front end that shares the kernel.
package importfile

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// Options controls parsing and SQL rendering.
type Options struct {
	// Format is "csv", "json" or "sql"; empty means infer from the extension.
	Format string
	// HasHeader says the first CSV row holds column names.
	HasHeader bool
	// Delimiter is the CSV separator (default ",").
	Delimiter string
	// InferTypes picks INTEGER/REAL/TEXT for generated columns.
	InferTypes bool
	// Mode is "append", "truncate" or "upsert".
	Mode string
	// UpsertKeys are the conflict columns for Mode="upsert".
	UpsertKeys []string
}

// Normalize applies defaults derived from the path and rejects unknown modes.
func Normalize(opts Options, path string) Options {
	if opts.Format == "" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json":
			opts.Format = "json"
		case ".sql":
			opts.Format = "sql"
		default:
			opts.Format = "csv"
		}
	}
	opts.Format = strings.ToLower(opts.Format)
	if opts.Delimiter == "" || len([]rune(opts.Delimiter)) > 1 {
		opts.Delimiter = ","
	}
	switch opts.Mode {
	case "truncate", "upsert":
	default:
		opts.Mode = "append"
	}
	return opts
}

// Parse returns column names and rows. JSON objects may omit keys; the missing
// cells become NULL. CSV cells are always text ("" stays an empty string, not
// NULL, because CSV has no null literal).
func Parse(path string, opts Options) ([]string, [][]*string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	if opts.Format == "json" {
		return ParseJSON(f)
	}
	return ParseCSV(f, opts)
}

// ParseCSV reads a delimited file. Ragged rows are reported per row rather than
// aborting the whole file.
func ParseCSV(r io.Reader, opts Options) ([]string, [][]*string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	// Normalize() fills the delimiter in; default here too so the package API
	// cannot panic on a zero Options (a missing rune would index []rune("")).
	delimiter := opts.Delimiter
	if delimiter == "" {
		delimiter = ","
	}
	cr.Comma = []rune(delimiter)[0]
	records, err := cr.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("import: parse csv: %w", err)
	}
	if len(records) == 0 {
		return nil, nil, fmt.Errorf("import: file is empty")
	}
	cols := records[0]
	start := 0
	if opts.HasHeader {
		start = 1
	} else {
		cols = make([]string, len(records[0]))
		for i := range cols {
			cols[i] = "column" + strconv.Itoa(i+1)
		}
	}
	rows := make([][]*string, 0, len(records)-start)
	for _, rec := range records[start:] {
		row := make([]*string, len(cols))
		for i := range cols {
			if i < len(rec) {
				cell := rec[i]
				row[i] = &cell
			}
		}
		rows = append(rows, row)
	}
	return cols, rows, nil
}

// ParseJSON reads an array of objects (or a single object) into rows.
func ParseJSON(r io.Reader) ([]string, [][]*string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	var objects []map[string]any
	if err := json.Unmarshal(data, &objects); err != nil {
		var one map[string]any
		if err2 := json.Unmarshal(data, &one); err2 != nil {
			return nil, nil, fmt.Errorf("import: parse json (expected an object or an array of objects): %w", err)
		}
		objects = []map[string]any{one}
	}
	if len(objects) == 0 {
		return nil, nil, fmt.Errorf("import: json array is empty")
	}
	seen := map[string]bool{}
	var cols []string
	for _, obj := range objects {
		for k := range obj {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	sort.Strings(cols)
	rows := make([][]*string, 0, len(objects))
	for _, obj := range objects {
		row := make([]*string, len(cols))
		for i, c := range cols {
			v, ok := obj[c]
			if !ok || v == nil {
				continue // NULL
			}
			var cell string
			switch val := v.(type) {
			case string:
				cell = val
			case float64:
				cell = strconv.FormatFloat(val, 'f', -1, 64)
			case bool:
				cell = strconv.FormatBool(val)
			default:
				b, _ := json.Marshal(val)
				cell = string(b)
			}
			row[i] = &cell
		}
		rows = append(rows, row)
	}
	return cols, rows, nil
}

// InferTypes picks a storage class ("int"/"real"/"text") per column from the
// sample values.
func InferTypes(cols []string, rows [][]*string) []string {
	kinds := make([]string, len(cols))
	for i := range kinds {
		kinds[i] = "text"
	}
	for _, row := range rows {
		for i := range cols {
			if i >= len(row) || row[i] == nil || *row[i] == "" {
				continue
			}
			v := *row[i]
			switch {
			case kinds[i] == "text":
				if _, err := strconv.ParseInt(v, 10, 64); err == nil {
					kinds[i] = "int"
				} else if _, err := strconv.ParseFloat(v, 64); err == nil {
					kinds[i] = "real"
				}
			case kinds[i] == "int":
				if _, err := strconv.ParseInt(v, 10, 64); err != nil {
					if _, err := strconv.ParseFloat(v, 64); err == nil {
						kinds[i] = "real"
					} else {
						kinds[i] = "text"
					}
				}
			case kinds[i] == "real":
				if _, err := strconv.ParseFloat(v, 64); err != nil {
					kinds[i] = "text"
				}
			}
		}
	}
	return kinds
}

// ColumnType maps an inferred kind onto the engine's column type.
func ColumnType(dbType, kind string) string {
	switch kind {
	case "int":
		switch dbType {
		case "oracle":
			return "NUMBER(19)"
		case "sqlite":
			return "INTEGER"
		default:
			return "BIGINT"
		}
	case "real":
		switch dbType {
		case "mysql":
			return "DOUBLE"
		case "postgresql":
			return "DOUBLE PRECISION"
		case "sqlserver":
			return "FLOAT"
		case "oracle":
			return "BINARY_DOUBLE"
		default:
			return "REAL"
		}
	default:
		switch dbType {
		case "sqlserver":
			return "NVARCHAR(MAX)"
		case "oracle":
			return "CLOB"
		default:
			return "TEXT"
		}
	}
}

// simpleIdent matches identifiers Oracle stores upper-case without quoting.
var simpleIdent = func(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// Ident renders an identifier for CREATE/INSERT. Oracle is special: a quoted
// lower-case name creates a case-sensitive object that later unquoted
// references cannot find, so simple names stay unquoted there.
func Ident(dbType, name string) string {
	if dbType == "oracle" && simpleIdent(name) {
		return name
	}
	q, err := sqltext.QuoteIdent(dbType, name)
	if err != nil {
		return name
	}
	return q
}

// CreateTableSQL renders the CREATE TABLE for the parsed columns.
func CreateTableSQL(dbType, table string, cols []string, kinds []string) string {
	defs := make([]string, len(cols))
	for i, c := range cols {
		defs[i] = Ident(dbType, c) + " " + ColumnType(dbType, kinds[i])
	}
	return "CREATE TABLE " + Ident(dbType, table) + " (\n  " + strings.Join(defs, ",\n  ") + "\n)"
}

// Placeholder renders the idx-th bind marker (1-based) in the engine's syntax.
func Placeholder(dbType string, idx int) string {
	switch dbType {
	case "postgresql":
		return "$" + strconv.Itoa(idx)
	case "sqlserver":
		return "@p" + strconv.Itoa(idx)
	case "oracle":
		return ":" + strconv.Itoa(idx)
	}
	return "?"
}

// InsertSQL renders one (optionally multi-row) INSERT. In "upsert" mode the
// statement gets the engine's conflict clause; Oracle accepts one row per
// statement, which the caller enforces by batching per row.
func InsertSQL(dbType, table string, cols []string, rows [][]*string, opts Options) (string, []dbcore.Value, error) {
	if dbType == "oracle" && len(rows) > 1 {
		return "", nil, fmt.Errorf("import: oracle accepts one row per INSERT statement")
	}
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = Ident(dbType, c)
	}
	var tuples []string
	var args []dbcore.Value
	n := 0
	for _, row := range rows {
		marks := make([]string, len(cols))
		for i := range cols {
			n++
			marks[i] = Placeholder(dbType, n)
			var cell *string
			if i < len(row) {
				cell = row[i]
			}
			if cell == nil {
				args = append(args, dbcore.Value{Kind: dbcore.ValNull})
			} else {
				args = append(args, dbcore.Value{Kind: dbcore.ValText, S: *cell})
			}
		}
		tuples = append(tuples, "("+strings.Join(marks, ", ")+")")
	}
	stmt := "INSERT INTO " + Ident(dbType, table) + " (" + strings.Join(quotedCols, ", ") + ") VALUES " + strings.Join(tuples, ", ")
	if opts.Mode == "upsert" {
		clause, err := UpsertClause(dbType, cols, opts.UpsertKeys)
		if err != nil {
			return "", nil, err
		}
		stmt += clause
	}
	return stmt, args, nil
}

// UpsertClause returns the engine's conflict clause for Mode="upsert".
//
// SQL Server and Oracle need a full MERGE statement (source values, matched and
// not-matched branches); fabricating one here would be a second, untested write
// path, so those two report a clear error and the user keeps append mode.
func UpsertClause(dbType string, cols, keys []string) (string, error) {
	if len(keys) == 0 {
		return "", fmt.Errorf("import: upsert needs a conflict target (the table's primary key or a unique column)")
	}
	known := make(map[string]bool, len(cols))
	for _, c := range cols {
		known[c] = true
	}
	quotedKeys := make([]string, 0, len(keys))
	for _, k := range keys {
		if !known[k] {
			return "", fmt.Errorf("import: upsert key %q is not one of the imported columns", k)
		}
		quotedKeys = append(quotedKeys, Ident(dbType, k))
	}
	assignments := make([]string, 0, len(cols))
	switch dbType {
	case "mysql":
		// MySQL ignores the key list: any unique index can conflict.
		for _, c := range cols {
			q := Ident(dbType, c)
			assignments = append(assignments, q+"=VALUES("+q+")")
		}
		return " ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ", "), nil
	case "postgresql", "sqlite":
		for _, c := range cols {
			q := Ident(dbType, c)
			assignments = append(assignments, q+"=excluded."+q)
		}
		return " ON CONFLICT (" + strings.Join(quotedKeys, ", ") + ") DO UPDATE SET " + strings.Join(assignments, ", "), nil
	case "sqlserver", "oracle":
		return "", fmt.Errorf("import: %s does not support upsert import (needs MERGE); use append or truncate mode", dbType)
	}
	return "", fmt.Errorf("import: upsert is not supported for %s", dbType)
}

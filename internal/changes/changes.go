// Package changes rebuilds the grid's queued edits into SQL statements.
//
// Nothing the client sends is SQL: the rows carry the values the user typed and
// the row exactly as the grid read it, and the statements are rendered here from
// the table's own catalog rows. That is what keeps a request from smuggling a
// statement of its own through the grid, and it is why the preview and the
// applied batch are produced by one function.
package changes

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore/sqltext"
)

// CellEdit is one value typed into the result grid. The column is named as the
// result named it, so the builder can tell an alias from a real field.
type CellEdit struct {
	Column string      `json:"column"`
	Value  interface{} `json:"value"`
}

// How a queued row touches the table. An edited cell is the default, so a client
// that says nothing means an UPDATE.
const (
	KindUpdate = ""
	KindInsert = "insert"
	KindDelete = "delete"
)

// RowChange carries the row exactly as the grid read it, which is how the key
// columns are picked out without the client having to know which they are. A new
// row has no original, and only the values the user actually typed count as its
// edits.
type RowChange struct {
	Kind     string        `json:"kind,omitempty"`
	Original []interface{} `json:"original"`
	Edits    []CellEdit    `json:"edits"`
}

// ChangeSet describes the edits made to one table.
type ChangeSet struct {
	ConnID   string      `json:"connId"`
	Database string      `json:"database"`
	Schema   string      `json:"schema"`
	Table    string      `json:"table"`
	Columns  []string    `json:"columns"`
	Changes  []RowChange `json:"changes"`
}

// ValueLiteral turns what the grid holds into a literal for the column it
// belongs to. The declared type decides whether text is a number, which is what
// lets a cleared numeric cell mean NULL instead of zero.
//
// Text is the shape this client actually receives: every value reaches the
// server through a JSON body, and a JSON number decodes as float64, so the cells
// an edit names arrive as text or as a float - never as the int64 a read yielded,
// which is the shape the Fyne client has to answer. A plain number written as
// text keeps its digits (exactNumberLiteral), because a double cannot hold it
// past 2^53 and cannot name a DECIMAL's twentieth digit at all.
func ValueLiteral(dbType, colType string, value interface{}) (string, error) {
	switch v := value.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case float64:
		return NumberLiteral(v), nil
	// Integer shapes reach the builder when the caller read the row locally
	// (a Go caller) rather than through the JSON grid, which decodes numbers as
	// float64. Rendering them as-is avoids the float round trip entirely.
	case int:
		return strconv.FormatInt(int64(v), 10), nil
	case int8:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case string:
		if isNumericColumn(colType) {
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				return "NULL", nil
			}
			if literal, exact := exactNumberLiteral(trimmed); exact {
				return literal, nil
			}
			number, err := strconv.ParseFloat(trimmed, 64)
			if err != nil {
				return "", fmt.Errorf("%q is not a valid %s", v, colType)
			}
			return NumberLiteral(number), nil
		}
		if strings.ContainsRune(v, 0) {
			return "", fmt.Errorf("a value cannot contain NUL")
		}
		return sqltext.QuotedLiteral(dbType, v), nil
	}
	return "", fmt.Errorf("unsupported value of type %T", value)
}

// Predicate renders one "column = value" test, with NULL compared the only way
// SQL allows.
func Predicate(dbType, colType, quotedColumn string, value interface{}) (string, error) {
	literal, err := ValueLiteral(dbType, colType, value)
	if err != nil {
		return "", err
	}
	if literal == "NULL" {
		return quotedColumn + " IS NULL", nil
	}
	return quotedColumn + " = " + literal, nil
}

func isNumericColumn(colType string) bool {
	return sqltext.NewColumnType(colType).IsNumeric()
}

// NumberLiteral renders a float as a plain number (no scientific notation),
// which is what every engine accepts as a numeric literal.
func NumberLiteral(value float64) string {
	if math.Trunc(value) == value && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	// Never scientific notation: "1e+18" is not what every engine accepts.
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// exactNumberLiteral renders text that denotes a plain number - an optional
// sign, digits, and at most one point with digits on both sides - and reports
// whether it recognised that shape. The float path can't: a double keeps 53 bits
// of mantissa, so "9007199254740993" comes back one lower and
// "123456789012345678.90" comes back with three wrong digits at the end, and a
// key predicate built on either names a neighbouring row or none.
//
// Digits past what 64 bits hold, or past what a double can name, go out as the
// digits themselves rather than as a rounded float or a refusal - a server reads
// a numeric literal at its own precision, so a DECIMAL(30,0) column still gets
// the value the user named, while a bigint column answers an impossible one with
// its own out-of-range error. That error is the outcome the client must not
// pre-empt by quietly rounding: it is loud, and it names the column.
//
// Only the shape is recognised, never the size: ".5" and "1." and "1e3" stay on
// the path that has always rendered them, "0x10" stays refused.
func exactNumberLiteral(text string) (string, bool) {
	body := strings.TrimPrefix(text, "+")
	digits := strings.TrimPrefix(body, "-")
	if digits == "" {
		return "", false
	}
	if dot := strings.IndexByte(digits, '.'); dot >= 0 {
		if dot == 0 || dot == len(digits)-1 || !allDigits(digits[:dot]) || !allDigits(digits[dot+1:]) {
			return "", false
		}
		return body, true
	}
	if !allDigits(digits) {
		return "", false
	}
	if number, err := strconv.ParseInt(text, 10, 64); err == nil {
		return strconv.FormatInt(number, 10), true
	}
	if !strings.HasPrefix(body, "-") {
		if number, err := strconv.ParseUint(digits, 10, 64); err == nil {
			return strconv.FormatUint(number, 10), true
		}
	}
	return body, true
}

func allDigits(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

// context holds what every statement in a batch needs: where each result column
// came from and how a row is located. Building it once means a projection that
// left the key out is caught before any statement is rendered.
type context struct {
	dbType       string
	name         string
	columns      []string
	positions    map[string]int
	keyPositions []int
	types        map[string]string
}

func newContext(dbType, name string, resultColumns, keyColumns []string, types map[string]string) (*context, error) {
	c := &context{
		dbType:    dbType,
		name:      name,
		columns:   resultColumns,
		types:     types,
		positions: make(map[string]int, len(resultColumns)),
	}
	for i, col := range resultColumns {
		c.positions[strings.ToLower(col)] = i
	}
	// A table with no key is still usable here: only the statements that locate
	// an existing row by key need one, and an insert does not.
	for _, key := range keyColumns {
		i, ok := c.positions[strings.ToLower(key)]
		if !ok {
			return nil, fmt.Errorf("the result does not include key column %s; select it to edit rows", key)
		}
		c.keyPositions = append(c.keyPositions, i)
	}
	return c, nil
}

// resolve matches a result column against the table, which is how an alias or a
// computed field is caught before it reaches the server.
func (c *context) resolve(resultName string) (string, string, error) {
	i, ok := c.positions[strings.ToLower(resultName)]
	if !ok {
		return "", "", fmt.Errorf("column %s is not in the result", resultName)
	}
	colType, ok := c.types[strings.ToLower(c.columns[i])]
	if !ok {
		return "", "", fmt.Errorf("%s is not a plain column of the table, so it cannot be edited", c.columns[i])
	}
	ident, err := sqltext.QuoteIdent(c.dbType, c.columns[i])
	if err != nil {
		return "", "", err
	}
	return ident, colType, nil
}

func (c *context) literals(edits []CellEdit) ([]string, []string, []string, error) {
	columns := make([]string, 0, len(edits))
	values := make([]string, 0, len(edits))
	assignments := make([]string, 0, len(edits))
	for _, edit := range edits {
		ident, colType, err := c.resolve(edit.Column)
		if err != nil {
			return nil, nil, nil, err
		}
		literal, err := ValueLiteral(c.dbType, colType, edit.Value)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", edit.Column, err)
		}
		columns = append(columns, ident)
		values = append(values, literal)
		assignments = append(assignments, ident+" = "+literal)
	}
	return columns, values, assignments, nil
}

// keyWhere is the WHERE every write that finds an existing row needs, with NULL
// compared the only way SQL allows.
func (c *context) keyWhere(original []interface{}) (string, error) {
	if len(c.keyPositions) == 0 {
		return "", fmt.Errorf("%s has no primary key, so rows cannot be located", c.name)
	}
	conditions := make([]string, 0, len(c.keyPositions))
	for _, i := range c.keyPositions {
		ident, colType, err := c.resolve(c.columns[i])
		if err != nil {
			return "", err
		}
		var value interface{}
		if i < len(original) {
			value = original[i]
		}
		test, err := Predicate(c.dbType, colType, ident, value)
		if err != nil {
			return "", err
		}
		conditions = append(conditions, test)
	}
	return strings.Join(conditions, " AND "), nil
}

// update returns an empty statement for a row whose edits all cancelled out.
func (c *context) update(change RowChange) (string, error) {
	_, _, assignments, err := c.literals(change.Edits)
	if err != nil {
		return "", err
	}
	if len(assignments) == 0 {
		return "", nil
	}
	where, err := c.keyWhere(change.Original)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UPDATE %s SET %s WHERE %s", c.name, strings.Join(assignments, ", "), where), nil
}

// insert writes only the columns the grid filled in, so an auto-increment key
// and every column default keep working.
func (c *context) insert(change RowChange) (string, error) {
	if len(change.Edits) == 0 {
		return "", fmt.Errorf("a new row needs at least one value")
	}
	columns, values, _, err := c.literals(change.Edits)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		c.name, strings.Join(columns, ", "), strings.Join(values, ", ")), nil
}

func (c *context) remove(change RowChange) (string, error) {
	where, err := c.keyWhere(change.Original)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("DELETE FROM %s WHERE %s", c.name, where), nil
}

// Build is the pure half of the work: given how the result is shaped and what
// the table's columns are, render the whole queue - UPDATE, INSERT and DELETE
// rows in the order the client queued them.
func Build(dbType, name string, resultColumns, keyColumns []string, types map[string]string, changes []RowChange) ([]string, error) {
	if len(changes) == 0 {
		return nil, fmt.Errorf("nothing to save")
	}
	c, err := newContext(dbType, name, resultColumns, keyColumns, types)
	if err != nil {
		return nil, err
	}

	statements := make([]string, 0, len(changes))
	for _, change := range changes {
		var statement string
		switch change.Kind {
		case KindInsert:
			statement, err = c.insert(change)
		case KindDelete:
			statement, err = c.remove(change)
		case KindUpdate:
			statement, err = c.update(change)
		default:
			// A kind nobody recognises must not quietly become an UPDATE: the
			// row would be written differently from what the grid promised.
			return nil, fmt.Errorf("unknown row change kind %q", change.Kind)
		}
		if err != nil {
			return nil, err
		}
		if statement != "" {
			statements = append(statements, statement)
		}
	}
	if len(statements) == 0 {
		return nil, fmt.Errorf("nothing to save")
	}
	return statements, nil
}

// BuildUpdates is Build narrowed to a queue of edited cells; it is the shape the
// grid has always sent.
func BuildUpdates(dbType, name string, resultColumns, keyColumns []string, types map[string]string, changes []RowChange) ([]string, error) {
	var updates []RowChange
	for _, change := range changes {
		if change.Kind == KindUpdate {
			updates = append(updates, change)
		}
	}
	if len(resultColumns) == 0 {
		return nil, fmt.Errorf("nothing to save")
	}
	return Build(dbType, name, resultColumns, keyColumns, types, updates)
}

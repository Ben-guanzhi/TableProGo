package main

import (
	"fmt"
	"strconv"
	"strings"

	"tableprosql"
)

// Keyset paging for the data grid.
//
// OFFSET m makes the server read and discard m rows before it returns one
// page, so stepping to page k costs Θ(k²) rows in total; seeking from the key
// of the page already on screen reads exactly a page. The grid only offers
// 上一页/下一页, so the page it shows is always the one a cursor can be taken
// from - which is what makes the second form usable here at all.
//
// The cursor travels as text, never as a JSON number: a 64-bit key does not
// survive JavaScript's double, and a key that came back rounded would skip or
// repeat a row without any error to show for it.

type keyKind int

const (
	keyNotSeekable keyKind = iota
	keyInteger
	keyText
)

// keyColumn is one column of the page's key: the name the catalog answers with,
// the identifier to write into SQL, and how its values must be read back.
type keyColumn struct {
	name   string
	quoted string
	kind   keyKind
}

// keysetKeys returns the key to seek by, in the order pageOrderFrom sorts by,
// or nil when this page cannot be seeked. A key holding a type the grid cannot
// round-trip is treated as no key at all: the page is then read by OFFSET,
// which is what it used to be read by.
//
// SQL Server and Oracle are left out on purpose. Their parameter conventions
// cannot be checked from here, and their paging form is already a server-side
// cursor statement, so the risk of getting the placeholder wrong outweighs a
// saving those engines have not been measured to give.
func keysetKeys(dbType string, columns []ColumnInfo, columnsErr error) []keyColumn {
	switch dbType {
	case "mysql", "postgresql", "sqlite":
	default:
		return nil
	}
	if columnsErr != nil {
		return nil
	}
	var keys []keyColumn
	for _, col := range columns {
		if !col.IsPrimaryKey {
			continue
		}
		kind := seekableKeyType(col.Type)
		if kind == keyNotSeekable {
			return nil
		}
		quoted, err := quoteIdent(dbType, col.Name)
		if err != nil {
			return nil
		}
		keys = append(keys, keyColumn{name: col.Name, quoted: quoted, kind: kind})
	}
	return keys
}

// seekableKeyType admits whole numbers and text by their declared type. Floats
// and decimals print differently on the way back, dates and times come out of
// the driver formatted rather than stored, and binary arrives as text that is
// not the bytes - so those keys are refused instead of guessed at.
// The type knowledge lives in tableprosql.ColumnType: IsSeekable is the gate
// and IsInteger picks the cursor encoding, so the fold happens once in the
// constructor and nowhere else.
func seekableKeyType(colType string) keyKind {
	ct := tableprosql.NewColumnType(colType)
	if !ct.IsSeekable() {
		return keyNotSeekable
	}
	if ct.IsInteger() {
		return keyInteger
	}
	return keyText
}

// keysetOrder orders a seek by the key, descending when stepping backwards so
// the rows nearest the cursor are the ones a page holds.
func keysetOrder(keys []keyColumn, backward bool) string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = key.quoted
		if backward {
			parts[i] += " DESC"
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

// keysetWhere keeps the rows strictly after (or before) a cursor. A key of more
// than one column cannot be compared as a row value on every engine, so the
// tuple is expanded the portable way: greater in the first column, or equal so
// far and greater in the next. The values go in as parameters, never as text
// pasted into the statement.
func keysetWhere(dbType string, keys []keyColumn, cursor []string, backward bool) (string, []interface{}, error) {
	if len(cursor) != len(keys) {
		return "", nil, fmt.Errorf("游标有 %d 个值，键有 %d 列", len(cursor), len(keys))
	}
	comparison := ">"
	if backward {
		comparison = "<"
	}

	var args []interface{}
	// A repeated key column needs one parameter per mention, because "?" cannot
	// be named twice.
	param := func(key keyColumn, raw string) (string, error) {
		arg, err := cursorArg(key, raw)
		if err != nil {
			return "", err
		}
		args = append(args, arg)
		return cursorParam(dbType, len(args)), nil
	}

	terms := make([]string, 0, len(keys))
	for i := range keys {
		parts := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			place, err := param(keys[j], cursor[j])
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, keys[j].quoted+" = "+place)
		}
		place, err := param(keys[i], cursor[i])
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, keys[i].quoted+" "+comparison+" "+place)
		term := strings.Join(parts, " AND ")
		if len(parts) > 1 {
			term = "(" + term + ")"
		}
		terms = append(terms, term)
	}
	return "(" + strings.Join(terms, " OR ") + ")", args, nil
}

// cursorParam spells the nth parameter (counting from one) the way the engine
// named in dbType wants it.
func cursorParam(dbType string, position int) string {
	if dbType == "postgresql" {
		return "$" + strconv.Itoa(position)
	}
	return "?"
}

// cursorArg turns one echoed key back into a value of the type its column
// declares, which is what lets the seek run down the key's index instead of
// casting every row it passes.
func cursorArg(key keyColumn, raw string) (interface{}, error) {
	switch key.kind {
	case keyInteger:
		text := strings.TrimSpace(raw)
		if number, err := strconv.ParseInt(text, 10, 64); err == nil {
			return number, nil
		}
		// An unsigned 64-bit key above the signed range is still digits.
		if number, err := strconv.ParseUint(text, 10, 64); err == nil {
			return number, nil
		}
		return nil, fmt.Errorf("游标值 %q 不是 %s 列的整数", raw, key.name)
	case keyText:
		if strings.ContainsRune(raw, 0) {
			return nil, fmt.Errorf("游标值不能包含 NUL")
		}
		return raw, nil
	}
	return nil, fmt.Errorf("%s 列不能作为游标", key.name)
}

// pageCursors reads the key of the first and the last row of a page back into
// the cursors the next step is taken from. Anything that stops it - a key
// column the result does not carry, a value whose shape is not the declared
// one - leaves the page without cursors, and the following page is read by
// OFFSET.
func pageCursors(rows [][]interface{}, keys []keyColumn, columns []Column) (start, end []string) {
	if len(rows) == 0 {
		return nil, nil
	}
	positions := make([]int, len(keys))
	for i, key := range keys {
		positions[i] = -1
		for j, col := range columns {
			if strings.EqualFold(col.Name, key.name) {
				positions[i] = j
				break
			}
		}
		if positions[i] < 0 {
			return nil, nil
		}
	}
	pick := func(row []interface{}) ([]string, bool) {
		if len(row) == 0 {
			return nil, false
		}
		values := make([]string, len(keys))
		for i, key := range keys {
			value, ok := cursorValue(key, row[positions[i]])
			if !ok {
				return nil, false
			}
			values[i] = value
		}
		return values, true
	}
	if first, ok := pick(rows[0]); ok {
		start = first
	}
	if last, ok := pick(rows[len(rows)-1]); ok {
		end = last
	}
	if start == nil || end == nil {
		return nil, nil
	}
	return start, end
}

// cursorValue spells one stored key as the text a client echoes back.
func cursorValue(key keyColumn, value interface{}) (string, bool) {
	switch key.kind {
	case keyInteger:
		switch number := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return fmt.Sprintf("%d", number), true
		case string:
			if _, err := strconv.ParseInt(strings.TrimSpace(number), 10, 64); err == nil {
				return strings.TrimSpace(number), true
			}
		}
	case keyText:
		switch text := value.(type) {
		case string:
			return text, true
		case []byte:
			return string(text), true
		}
	}
	return "", false
}

// reversePage puts a page read backwards back into the order the grid shows.
func reversePage(rows [][]interface{}) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

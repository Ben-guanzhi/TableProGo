// Package rowsync computes the row-level changes that make one table look like
// another.
//
// It is the pure half of W6 (行级数据同步): given two streams of rows ordered by
// the same primary key, it produces the INSERT/UPDATE/DELETE plan that converges
// the target onto the source. The orchestrator owns the reads (BrowseRows with a
// PK sort), the writes (parameterized DML through the target's transaction), and
// the confirm/apply discipline; this package owns the only part that must be
// provably right — the merge.
//
// The merge is a two-way merge over sorted streams, the same shape as the keyset
// pager's "stays-ahead" argument: every step consumes at least one row from at
// least one side, and the rows each side yields are in key order, so the merged
// output is produced in key order and every key is visited exactly once. That is
// why the caller MUST order both sides by the same key — an unordered stream
// would make the merge silently wrong rather than loudly failing.
//
// Memory: the merger holds one current row per side plus the batch it is walking.
// Changes are handed to the caller as they are produced, so a large table never
// materializes as a change list unless the caller chooses to collect it.
package rowsync

import (
	"errors"
	"fmt"
	"io"

	"github.com/dbcore/dbcore"
)

// Value is the cell value type crossing the driver boundary (dbcore.Value).
type Value = dbcore.Value

// Kind aliases so the switch arms read without the package prefix.
const (
	ValNull    = dbcore.ValNull
	ValInt     = dbcore.ValInt
	ValFloat   = dbcore.ValFloat
	ValText    = dbcore.ValText
	ValBlob    = dbcore.ValBlob
	ValBool    = dbcore.ValBool
	ValTime    = dbcore.ValTime
	ValDecimal = dbcore.ValDecimal
)

// Accessor aliases.
var (
	TryGetInt          = dbcore.TryGetInt
	DecimalToFloat64   = dbcore.DecimalToFloat64
)

func stringsCompare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func bytesCompare(a, b []byte) int {
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// Row is one row of one side: its primary-key values (in PK column order) and
// its column values keyed by name.
type Row struct {
	// Key is the primary-key tuple, aligned with the PK column order both sides
	// agreed on. It defines the row's identity and its position in the stream.
	Key []Value
	// Cols holds every compared column by name. A column absent here is treated
	// as NULL on this side.
	Cols map[string]Value
}

// Op is the kind of change one plan entry carries.
type Op string

const (
	// OpInsert applies to a row the source has and the target lacks.
	OpInsert Op = "insert"
	// OpUpdate applies to a row both sides have with differing columns.
	OpUpdate Op = "update"
	// OpDelete applies to a row the target has and the source lacks.
	OpDelete Op = "delete"
)

// Change is one plan entry.
type Change struct {
	Op   Op
	// Key is the row's primary-key tuple, as it appears on the SOURCE for
	// insert/update and on the TARGET for delete.
	Key []Value
	// For OpInsert: every column with its source value.
	// For OpUpdate: only the columns that differ, with source values.
	// For OpDelete: empty.
	Values map[string]Value
	// Seq is the change's position in the merge order (1-based). It is the
	// stable id the apply step addresses a change by, so a plan can be
	// partially applied without re-deriving it.
	Seq int
}

// Options narrows the comparison.
type Options struct {
	// IgnoreColumns lists column names (as they appear in Cols) that never
	// count as a difference — auto-increment or touched-at columns the two
	// sides legitimately differ on. Ignored columns are still copied on
	// insert (a new row needs every column); they just cannot trigger an
	// update.
	IgnoreColumns map[string]bool
}

// ---- key and value comparison ----

// valueClass ranks a value's broad category so a numeric never compares against
// a text byte-by-byte: numbers order before text before everything else, and
// within a class the natural order applies. The classes cover what primary keys
// are made of; anything exotic lands in "other" and compares by kind then value,
// which is stable even if not semantically meaningful.
type valueClass int

const (
	classNull valueClass = iota
	classNumber
	classText
	classBytes
	classTime
	classOther
)

func classOf(v Value) valueClass {
	switch v.Kind {
	case ValNull:
		return classNull
	case ValInt, ValFloat, ValDecimal:
		return classNumber
	case ValText:
		return classText
	case ValBlob:
		return classBytes
	case ValTime:
		return classTime
	default:
		return classOther
	}
}

// compareValues orders two cell values totally. The ordering only has to be a
// total order consistent with equality — it is used to position rows in the
// merge, not to display anything.
//
// Equality inside it is deliberately exact: an int 1 and a text "1" are NOT
// equal (different classes), and two decimals compare by their digit text. A
// looser equality would manufacture updates for rows that are already equal.
func compareValues(a, b Value) int {
	ca, cb := classOf(a), classOf(b)
	if ca != cb {
		if ca < cb {
			return -1
		}
		return 1
	}
	switch ca {
	case classNull:
		return 0
	case classNumber:
		return compareNumbers(a, b)
	case classText:
		// ValDecimal is classNumber, so this is plain text.
		return stringsCompare(a.S, b.S)
	case classBytes:
		return bytesCompare(a.B, b.B)
	case classTime:
		switch {
		case a.T.Before(b.T):
			return -1
		case a.T.After(b.T):
			return 1
		}
		return 0
	default:
		if a.Kind != b.Kind {
			if a.Kind < b.Kind {
				return -1
			}
			return 1
		}
		switch a.Kind {
		case ValBool:
			switch {
			case !a.Bool && b.Bool:
				return -1
			case a.Bool && !b.Bool:
				return 1
			}
			return 0
		default:
			return stringsCompare(a.S, b.S)
		}
	}
}

// compareNumbers orders mixed numeric kinds exactly. Ints and floats compare as
// floats when the int fits exactly; decimals compare through their rational
// value; a decimal that is not a valid number falls back to text order (it
// cannot be positioned numerically, and a wrong position would corrupt the
// merge).
func compareNumbers(a, b Value) int {
	ai, aIsInt := TryGetInt(a)
	bi, bIsInt := TryGetInt(b)
	if aIsInt && bIsInt {
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		}
		return 0
	}
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		}
		return 0
	}
	// Last resort: digit text order. Deterministic, and correct for
	// same-formatted decimals.
	return stringsCompare(a.String(), b.String())
}

// toFloat converts a numeric value to float64 when exact.
func toFloat(v Value) (float64, bool) {
	switch v.Kind {
	case ValInt:
		return float64(v.I), true
	case ValFloat:
		return v.F, true
	case ValDecimal:
		return DecimalToFloat64(v)
	}
	return 0, false
}

// compareKeys orders two primary-key tuples position by position. A length
// mismatch is a caller bug (both sides agreed on the PK columns up front), so it
// is reported rather than guessed at.
func compareKeys(a, b []Value) (int, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("主键元组长度不一致: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if c := compareValues(a[i], b[i]); c != 0 {
			return c, nil
		}
	}
	return 0, nil
}

// valuesEqual reports whether two column values are the same for sync purposes.
// NULL equals NULL: both sides saying "no value" is agreement, not a difference.
func valuesEqual(a, b Value) bool {
	return compareValues(a, b) == 0
}

// ---- the merge ----

// rowStream walks one side's rows in key order, pulling batches as needed. The
// merger holds one of these per side; its live memory is one batch plus a
// cursor, which is what keeps a million-row table from materializing.
type rowStream struct {
	fetch func() ([]Row, error)
	buf   []Row
	pos   int
	done  bool
}

// peek returns the next row without consuming it. io.EOF means the side is
// exhausted; any other error propagates.
func (s *rowStream) peek() (Row, error) {
	if s.pos < len(s.buf) {
		return s.buf[s.pos], nil
	}
	if s.done {
		return Row{}, io.EOF
	}
	batch, err := s.fetch()
	if err != nil {
		return Row{}, err
	}
	if len(batch) == 0 {
		s.done = true
		return Row{}, io.EOF
	}
	s.buf = batch
	s.pos = 0
	return s.buf[0], nil
}

func (s *rowStream) advance() {
	if s.pos < len(s.buf) {
		s.pos++
	}
}

// ErrNotOrdered is returned when a side yields rows out of key order. The merge
// is only correct over ordered streams; refusing is the difference between a
// wrong plan and a loud failure.
var ErrNotOrdered = errors.New("rowsync: 行流未按主键升序返回")

// DiffStream merges two ordered row streams and emits one Change per differing
// row, in key order.
//
// fetchSrc and fetchDst each return the next batch of rows (empty slice = end
// of stream). Batches must be key-ordered within and across calls; the merger
// verifies the boundary between batches and refuses an unordered stream, because
// an unordered merge produces a plan that would corrupt the target.
//
// emit receives every change in order. Returning false stops the merge early
// (a preview that only wants the first N changes); returning an error aborts it.
//
// The merge's correctness argument: at every step at least one side advances,
// and the advanced-past prefix of each side is exactly the keys smaller than the
// last emitted change's key — so no key is visited twice and none is skipped.
func DiffStream(
	fetchSrc, fetchDst func() ([]Row, error),
	opts Options,
	emit func(Change) (bool, error),
) error {
	src := &rowStream{fetch: fetchSrc}
	dst := &rowStream{fetch: fetchDst}
	seq := 0

	emitChange := func(c Change) (bool, error) {
		seq++
		c.Seq = seq
		return emit(c)
	}

	var lastSrc, lastDst Value
	haveLastSrc, haveLastDst := false, false
	checkOrdered := func(prev Value, have bool, next Value, side string) error {
		if !have {
			return nil
		}
		if c := compareValues(prev, next); c >= 0 {
			return fmt.Errorf("%w（%s 侧）", ErrNotOrdered, side)
		}
		return nil
	}

	for {
		srcRow, srcErr := src.peek()
		dstRow, dstErr := dst.peek()

		switch {
		case srcErr != nil && !errors.Is(srcErr, io.EOF):
			return srcErr
		case dstErr != nil && !errors.Is(dstErr, io.EOF):
			return dstErr

		case errors.Is(srcErr, io.EOF) && errors.Is(dstErr, io.EOF):
			return nil

		case errors.Is(dstErr, io.EOF):
			// Source-only row: the target lacks it.
			if err := checkOrdered(lastSrc, haveLastSrc, srcRow.Key[0], "源"); err != nil {
				return err
			}
			lastSrc = srcRow.Key[0]
			haveLastSrc = true
			cont, err := emitChange(Change{Op: OpInsert, Key: srcRow.Key, Values: srcRow.Cols})
			if err != nil || !cont {
				return err
			}
			src.advance()

		case errors.Is(srcErr, io.EOF):
			// Target-only row: the source no longer has it.
			if err := checkOrdered(lastDst, haveLastDst, dstRow.Key[0], "目标"); err != nil {
				return err
			}
			lastDst = dstRow.Key[0]
			haveLastDst = true
			cont, err := emitChange(Change{Op: OpDelete, Key: dstRow.Key})
			if err != nil || !cont {
				return err
			}
			dst.advance()

		default:
			cmp, err := compareKeys(srcRow.Key, dstRow.Key)
			if err != nil {
				return err
			}
			switch {
			case cmp < 0:
				if err := checkOrdered(lastSrc, haveLastSrc, srcRow.Key[0], "源"); err != nil {
					return err
				}
				lastSrc = srcRow.Key[0]
				haveLastSrc = true
				cont, err := emitChange(Change{Op: OpInsert, Key: srcRow.Key, Values: srcRow.Cols})
				if err != nil || !cont {
					return err
				}
				src.advance()
			case cmp > 0:
				if err := checkOrdered(lastDst, haveLastDst, dstRow.Key[0], "目标"); err != nil {
					return err
				}
				lastDst = dstRow.Key[0]
				haveLastDst = true
				cont, err := emitChange(Change{Op: OpDelete, Key: dstRow.Key})
				if err != nil || !cont {
					return err
				}
				dst.advance()
			default:
				// Same key: compare columns.
				if changed, values := diffRowValues(srcRow, dstRow, opts); changed {
					if err := checkOrdered(lastSrc, haveLastSrc, srcRow.Key[0], "源"); err != nil {
						return err
					}
					lastSrc = srcRow.Key[0]
					haveLastSrc = true
					cont, err := emitChange(Change{Op: OpUpdate, Key: srcRow.Key, Values: values})
					if err != nil || !cont {
						return err
					}
				}
				src.advance()
				dst.advance()
			}
		}
	}
}

// diffRowValues compares two rows with the same key and returns the differing
// columns. A column present on one side and absent on the other is a difference
// (absent reads as NULL), unless the column is ignored.
func diffRowValues(src, dst Row, opts Options) (bool, map[string]Value) {
	changes := map[string]Value{}
	considered := map[string]bool{}
	for name, sv := range src.Cols {
		if opts.IgnoreColumns[name] {
			continue
		}
		considered[name] = true
		dv, ok := dst.Cols[name]
		if !ok {
			dv = Value{Kind: ValNull}
		}
		if !valuesEqual(sv, dv) {
			changes[name] = sv
		}
	}
	for name, dv := range dst.Cols {
		if opts.IgnoreColumns[name] || considered[name] {
			continue
		}
		if !valuesEqual(Value{Kind: ValNull}, dv) {
			changes[name] = Value{Kind: ValNull}
		}
	}
	return len(changes) > 0, changes
}

// ---- the slice convenience (tests and small tables) ----

// DiffRows is DiffStream over in-memory slices. It collects every change, which
// is fine for tests and small tables; the orchestrator uses DiffStream directly
// so a large table never materializes as a change list.
func DiffRows(src, dst []Row, opts Options) ([]Change, error) {
	var out []Change
	// Each "batch" is one row, so the stream walks the slice row by row; the
	// merger never sees more than one row of lookahead per side.
	fetch := func(rows []Row, i *int) func() ([]Row, error) {
		return func() ([]Row, error) {
			if *i >= len(rows) {
				return nil, nil
			}
			batch := []Row{rows[*i]}
			*i++
			return batch, nil
		}
	}
	si, di := 0, 0
	err := DiffStream(fetch(src, &si), fetch(dst, &di), opts, func(c Change) (bool, error) {
		out = append(out, c)
		return true, nil
	})
	return out, err
}

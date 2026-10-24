// Package paging adapts the shared kernel's keyset paging (dbcore/keyset) to
// the grid's catalogmodel shapes; call sites and tests are unchanged.
//
// The seek algorithm — which types can round-trip a text cursor, how a composite
// key expands into a portable WHERE, parameter spelling per engine — lives only
// in the kernel (INTEGRATION-PLAN P1-7). This file maps catalogmodel.ColumnInfo
// onto dbcore.ColumnInfo and result columns onto plain names; nothing here may
// grow engine knowledge of its own.
package paging

import (
	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/keyset"

	"tablepro-go/internal/catalogmodel"
)

type Kind = keyset.Kind

const (
	NotSeekable = keyset.NotSeekable
	Integer     = keyset.Integer
	Text        = keyset.Text
)

type Key = keyset.Key

// Keys returns the seek key for the page's primary key columns, or nil when the
// page must be read by OFFSET (see keyset.Keys).
func Keys(dbType string, columns []catalogmodel.ColumnInfo, columnsErr error) []Key {
	kcols := make([]dbcore.ColumnInfo, len(columns))
	for i, col := range columns {
		kcols[i] = dbcore.ColumnInfo{
			Name:         col.Name,
			Type:         col.Type,
			IsPrimaryKey: col.IsPrimaryKey,
		}
	}
	return keyset.Keys(dbType, kcols, columnsErr)
}

func SeekableKeyType(colType string) Kind { return keyset.SeekableKeyType(colType) }

func Order(keys []Key, backward bool) string { return keyset.Order(keys, backward) }

func Where(dbType string, keys []Key, cursor []string, backward bool) (string, []interface{}, error) {
	return keyset.Where(dbType, keys, cursor, backward)
}

func PageCursors(rows [][]interface{}, keys []Key, columns []catalogmodel.Column) (start, end []string) {
	names := make([]string, len(columns))
	for i, col := range columns {
		names[i] = col.Name
	}
	return keyset.PageCursors(rows, keys, names)
}

func ReversePage(rows [][]interface{}) { keyset.ReversePage(rows) }

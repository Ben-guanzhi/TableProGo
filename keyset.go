package main

// Keyset paging lives in internal/paging; these aliases keep the call sites in
// query.go (and the surface the tests exercise) unchanged.

import (
	"tablepro-go/internal/catalogmodel"
	"tablepro-go/internal/paging"
)

type keyKind = paging.Kind

const (
	keyNotSeekable = paging.NotSeekable
	keyInteger     = paging.Integer
	keyText        = paging.Text
)

type keyColumn = paging.Key

func keysetKeys(dbType string, columns []ColumnInfo, columnsErr error) []keyColumn {
	return paging.Keys(dbType, columns, columnsErr)
}

func seekableKeyType(colType string) keyKind { return paging.SeekableKeyType(colType) }

func keysetOrder(keys []keyColumn, backward bool) string { return paging.Order(keys, backward) }

func keysetWhere(dbType string, keys []keyColumn, cursor []string, backward bool) (string, []interface{}, error) {
	return paging.Where(dbType, keys, cursor, backward)
}

func pageCursors(rows [][]interface{}, keys []keyColumn, columns []Column) (start, end []string) {
	return paging.PageCursors(rows, keys, columns)
}

func reversePage(rows [][]interface{}) { paging.ReversePage(rows) }

var _ = catalogmodel.Column{}

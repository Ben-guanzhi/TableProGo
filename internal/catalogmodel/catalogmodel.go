// Package catalogmodel holds the metadata shapes the clients exchange with the
// kernel: tables, columns, indexes, foreign keys and the reconstructed DDL.
//
// They live outside package main so a future internal package (paging, filters,
// snapshots) can take them without importing the whole application. The client
// keeps type aliases, so existing code and JSON tags are unaffected.
package catalogmodel

// TableInfo is one object in the navigator.
type TableInfo struct {
	Name    string `json:"name"`
	Schema  string `json:"schema"`
	Type    string `json:"type"`
	Comment string `json:"comment"`
}

// ColumnInfo describes one column of a table.
type ColumnInfo struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	DefaultValue string `json:"defaultValue,omitempty"`
	IsPrimaryKey bool   `json:"isPrimaryKey"`
	Comment      string `json:"comment"`
}

// IndexInfo describes one index; composite keys keep their column order.
type IndexInfo struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
	Columns []string `json:"columns"`
}

// ForeignKeyInfo describes one constraint pointing at another table; composite
// keys keep their column pairs in catalog order.
type ForeignKeyInfo struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete"`
	OnUpdate   string   `json:"onUpdate"`
}

// TableStructure backs the object tab's structure page. CreateTable is rendered
// from the same rows the UI shows, so what the user reads is what they copy.
type TableStructure struct {
	Columns     []ColumnInfo     `json:"columns"`
	Indexes     []IndexInfo      `json:"indexes"`
	ForeignKeys []ForeignKeyInfo `json:"foreignKeys"`
	CreateTable string           `json:"createTable"`
}

// Column is the presence-only column descriptor a query result carries.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

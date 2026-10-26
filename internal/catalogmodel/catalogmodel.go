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
	// Method is the engine's access method (btree/BTREE/NONCLUSTERED/NORMAL…),
	// empty when the engine reports none.
	Method string `json:"method,omitempty"`
	// Parts is the ordered key list for display: a plain column name, or the
	// expression text of an expression part, with " DESC" appended for a
	// descending part. Columns keeps only plain names so DDL building never
	// quotes an expression as an identifier.
	Parts []string `json:"parts,omitempty"`
	// Predicate is a partial/filtered index's WHERE clause.
	Predicate string `json:"predicate,omitempty"`
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
	// Triggers lists the table's triggers; empty when the engine has none or
	// the read failed (the error is logged, not fatal to the structure page).
	Triggers []TriggerInfo `json:"triggers"`
	// Checks lists the table's CHECK constraints; same empty-on-unsupported
	// discipline as Triggers.
	Checks []CheckInfo `json:"checks"`
	// Partitions lists a partitioned table's partitions (empty for a plain
	// table, and when the engine has no partitioning at all).
	Partitions []PartitionInfo `json:"partitions"`
	// ViewDefinition is the view's DDL when the object is a view (empty for a
	// table, or when the engine cannot produce it).
	ViewDefinition string `json:"viewDefinition,omitempty"`
	CreateTable    string `json:"createTable"`
}

// PartitionInfo is one partition of a partitioned table: the engine's label,
// its boundary text and an approximate row count (-1 when unknown).
type PartitionInfo struct {
	Name  string `json:"name"`
	Bound string `json:"bound"`
	Rows  int64  `json:"rows"`
}

// EnumTypeInfo is one user-defined enum type and its labels in declaration
// order (PostgreSQL only).
type EnumTypeInfo struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// CheckInfo is one CHECK constraint as the structure page shows it.
type CheckInfo struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
}

// RoutineInfo is one stored function or procedure in the schema-level list.
type RoutineInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // FUNCTION | PROCEDURE
}

// TriggerInfo is one catalog trigger as the structure page shows it.
type TriggerInfo struct {
	Name    string `json:"name"`
	Timing  string `json:"timing"`
	Event   string `json:"event"`
	Enabled string `json:"enabled"`
}

// Column is the presence-only column descriptor a query result carries.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	// IsDecimal marks an exact-decimal column (DECIMAL/NUMERIC/MONEY…). Its
	// cells cross the JSON wire as digit TEXT to survive the round trip, so
	// the grid needs the column's type to sort and sum them as numbers.
	IsDecimal bool `json:"isDecimal,omitempty"`
}

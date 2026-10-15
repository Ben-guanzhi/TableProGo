package main

import (
	"github.com/Ben-guanzhi/TableProGo/tableprosql"

	"database/sql"
	"fmt"
	"strings"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"
)

type TableInfo struct {
	Name    string `json:"name"`
	Schema  string `json:"schema"`
	Type    string `json:"type"`
	Comment string `json:"comment"`
}

type ColumnInfo struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	DefaultValue string `json:"defaultValue,omitempty"`
	IsPrimaryKey bool   `json:"isPrimaryKey"`
	Comment      string `json:"comment"`
}

func (a *App) GetDatabases(connID string) ([]string, error) {
	a.connectionsMu.RLock()
	conn, ok := a.connections[connID]
	a.connectionsMu.RUnlock()

	if !ok || conn.DB == nil {
		return nil, fmt.Errorf("not connected")
	}

	if conn.Type == "sqlite" {
		return []string{conn.Database}, nil
	}

	query, err := tableprosql.DatabasesQuery(conn.Type)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return nil, nil
	}

	rows, err := conn.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var databases []string
	filter := a.savedDatabaseFilter(connID)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if filter.allows(name) {
			databases = append(databases, name)
		}
	}
	return databases, nil
}

// savedDatabaseFilter reads the filter from the persisted config so a change
// applies to an already-open connection without reconnecting. IDs are stable
// UUIDs, so the cache is keyed by the connection's own ID.
// 使用缓存 map 将线性查找 O(n) 降为 O(1)（CLRS ch11 哈希表）。
//
// 缓存键是连接的稳定 UUID，因此新增连接不需要失效缓存；删除连接时只需删除
// 对应键；更新连接时需删除该连接的缓存键。
// 缓存本身的写入走写锁：早先在 RLock 内写 map 会与并发的读路径竞态。
func (a *App) savedDatabaseFilter(connID string) DatabaseFilter {
	a.connectionsMu.RLock()
	filter, ok := a.connFilterCache[connID]
	a.connectionsMu.RUnlock()
	if ok {
		return filter
	}
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()
	if filter, ok := a.connFilterCache[connID]; ok {
		return filter
	}
	if a.connFilterCache == nil {
		a.connFilterCache = make(map[string]DatabaseFilter, len(a.savedConnections))
	}
	for i := range a.savedConnections {
		if a.savedConnections[i].ID == connID {
			a.connFilterCache[connID] = a.savedConnections[i].DatabaseFilter
			return a.savedConnections[i].DatabaseFilter
		}
	}
	return DatabaseFilter{}
}

// invalidateConnFilterCacheLocked 丢弃整个过滤器缓存。调用方必须已持有
// connectionsMu 写锁。保留为兜底手段：saveToFile 折叠其它客户端的连接时
// 不确定影响了哪些键，直接清空最安全。
func (a *App) invalidateConnFilterCacheLocked() {
	a.connFilterCache = nil
}

func (a *App) GetSchemas(connID string, database string) ([]string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}

	// One collection loop serves both schema engines; only the catalog query
	// differs. PostgreSQL answers through its per-database pool, SQL Server
	// through a qualified name on the main pool.
	var db *sql.DB
	query, err := tableprosql.SchemasQuery(conn.Type, database)
	if err != nil {
		return nil, err
	}
	if query == "" {
		// mysql uses the database as its schema, sqlite has a single main schema,
		// oracle is introspected through the connected user.
		return nil, nil
	}
	if conn.Type == "sqlserver" {
		db = conn.DB
	} else {
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		schemas = append(schemas, name)
	}
	return schemas, nil
}

func (a *App) GetTables(connID string, database string, schema string) ([]TableInfo, error) {
	return a.getTablesFiltered(connID, database, schema, "", false)
}

// getTablesFiltered is GetTables with an optional predicate on the object's
// name. A caller that needs to know whether the database holds one particular
// object asks directly instead of pulling the whole catalog: the payload stays
// one row no matter how many tables the schema has.
func (a *App) getTablesFiltered(connID string, database string, schema string, exact string, insensitive bool) ([]TableInfo, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}

	schema = defaultSchema(conn.Type, schema)

	db := conn.DB
	if conn.Type == "postgresql" {
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
	}
	query, args, err := tablesQuery(conn.Type, database, schema, exact, insensitive)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Name, &t.Type, &t.Comment); err != nil {
			return nil, err
		}
		t.Type = normalizeTableType(t.Type)
		t.Schema = schema
		tables = append(tables, t)
	}
	return tables, nil
}

// tablesQuery delegates to the shared SQL text package. The catalog statement
// behind GetTables is one dialect switch kept honest by tests.
func tablesQuery(dbType, database, schema, exact string, insensitive bool) (string, []interface{}, error) {
	return tableprosql.TablesQuery(dbType, database, schema, exact, insensitive)
}

// namePredicate is the inlined-literal form of the exact-name filter, for the
// engines whose drivers make binds unreliable.
func namePredicate(dbType, column, exact string, insensitive bool) string {
	if exact == "" {
		return ""
	}
	literal := quotedLiteral(dbType, exact)
	if insensitive {
		return fmt.Sprintf(` AND LOWER(%s) = LOWER(%s)`, column, literal)
	}
	return fmt.Sprintf(` AND %s = %s`, column, literal)
}

// lookupObject answers "is this name in this schema, and what is it" without
// dragging the whole catalog back with it.
func (a *App) lookupObject(connID, database, schema, name string, insensitive bool) (*TableInfo, error) {
	if name == "" {
		return nil, nil
	}
	matches, err := a.getTablesFiltered(connID, database, schema, name, insensitive)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &matches[0], nil
}

func (a *App) GetColumns(connID string, database string, schema string, table string) ([]ColumnInfo, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}

	if schema == "" {
		schema = resolveSchema(conn, database, schema)
	}

	db := conn.DB
	query, args, err := tableprosql.ColumnsQuery(conn.Type, database, schema, table)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return nil, fmt.Errorf("unsupported type: %s", conn.Type)
	}
	if conn.Type == "postgresql" {
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var defaultValue sql.NullString
		var isPrimaryKey int

		if conn.Type == "sqlite" {
			// table_info's fourth field is *notnull*, the opposite of the
			// YES/NO the other engines answer, so it gets its own reading. Its
			// sixth is not a yes/no flag either: pk numbers the key columns
			// 1..n, so a composite key used to lose every column but its first -
			// which is the key rows are paged and written back by.
			var cid, notnull, pk int
			if err := rows.Scan(&cid, &col.Name, &col.Type, &notnull, &defaultValue, &pk); err != nil {
				return nil, err
			}
			col.IsPrimaryKey = pk > 0
			col.Nullable = notnull == 0
		} else {
			var nullable string
			if err := rows.Scan(&col.Name, &col.Type, &nullable, &defaultValue, &isPrimaryKey, &col.Comment); err != nil {
				return nil, err
			}
			col.IsPrimaryKey = isPrimaryKey == 1
			col.Nullable = nullable == "YES" || nullable == "1" || nullable == "Y"
		}

		if defaultValue.Valid {
			col.DefaultValue = defaultValue.String
		}
		columns = append(columns, col)
	}
	return columns, nil
}

// IndexInfo describes an index or a unique/primary key constraint on a table.
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

func resolveSchema(conn *Connection, database, schema string) string {
	switch conn.Type {
	case "sqlserver":
		return "dbo"
	case "postgresql":
		return "public"
	case "mysql":
		return database
	}
	return schema
}

// indexRow is the flat, one-row-per-column shape the index catalogs return;
// the FK catalogs answer in the shared tableprosql.FKRow, and the aggregated
// lists are what the UI and the DDL want.
type indexRow struct {
	name    string
	kind    string
	column  string
	unique  bool
	primary bool
}

func aggregateIndexes(rows []indexRow) []IndexInfo {
	indexes := []IndexInfo{}
	byName := map[string]int{}
	for _, row := range rows {
		i, ok := byName[row.name]
		if !ok {
			byName[row.name] = len(indexes)
			indexes = append(indexes, IndexInfo{
				Name:    row.name,
				Kind:    row.kind,
				Unique:  row.unique,
				Primary: row.primary,
			})
			i = len(indexes) - 1
		}
		indexes[i].Columns = append(indexes[i].Columns, row.column)
	}
	return indexes
}

func aggregateForeignKeys(rows []tableprosql.FKRow) []ForeignKeyInfo {
	foreignKeys := []ForeignKeyInfo{}
	byName := map[string]int{}
	for _, row := range rows {
		i, ok := byName[row.Name]
		if !ok {
			byName[row.Name] = len(foreignKeys)
			foreignKeys = append(foreignKeys, ForeignKeyInfo{
				Name:      row.Name,
				RefSchema: row.RefSchema,
				RefTable:  row.RefTable,
				OnDelete:  row.OnDelete,
				OnUpdate:  row.OnUpdate,
			})
			i = len(foreignKeys) - 1
		}
		foreignKeys[i].Columns = append(foreignKeys[i].Columns, row.Column)
		foreignKeys[i].RefColumns = append(foreignKeys[i].RefColumns, row.RefColumn)
	}
	for i := range foreignKeys {
		foreignKeys[i].OnDelete = tableprosql.ReferenceAction(foreignKeys[i].OnDelete)
		foreignKeys[i].OnUpdate = tableprosql.ReferenceAction(foreignKeys[i].OnUpdate)
	}
	return foreignKeys
}

func (a *App) tableIndexes(conn *Connection, database, schema, table string) ([]IndexInfo, error) {
	db := conn.DB
	query, args, err := tableprosql.IndexesQuery(conn.Type, database, schema, table)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return a.sqliteIndexes(db, table)
	}
	if conn.Type == "postgresql" {
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flat []indexRow
	for rows.Next() {
		var row indexRow
		var unique, primary int
		// Five destinations, in the order tableprosql.IndexesQuery promises:
		// name, kind, is-unique, is-primary, column. The statement derives both
		// flags, so nothing here re-reads the engine's own naming to guess one.
		if err := rows.Scan(&row.name, &row.kind, &unique, &primary, &row.column); err != nil {
			return nil, err
		}
		row.unique, row.primary = unique == 1, primary == 1
		flat = append(flat, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aggregateIndexes(flat), nil
}

// sqliteIndexes walks index_list/index_info because pragmas cannot be joined.
func (a *App) sqliteIndexes(db *sql.DB, table string) ([]IndexInfo, error) {
	name, err := quoteIdent("sqlite", table)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(fmt.Sprintf("PRAGMA index_list(%s)", name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type entry struct {
		name   string
		origin string
		unique bool
	}
	var entries []entry
	for rows.Next() {
		var seq int
		var e entry
		var unique int
		var partial string
		if err := rows.Scan(&seq, &e.name, &unique, &e.origin, &partial); err != nil {
			return nil, err
		}
		e.unique = unique == 1
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	indexes := []IndexInfo{}
	for _, e := range entries {
		// Implicit indexes SQLite builds for UNIQUE/PRIMARY KEY constraints are
		// already covered by the constraint itself.
		if strings.HasPrefix(e.name, "sqlite_autoindex") {
			continue
		}
		columns, err := sqliteIndexColumns(db, e.name)
		if err != nil {
			return nil, err
		}
		indexes = append(indexes, IndexInfo{
			Name:    e.name,
			Kind:    e.origin,
			Unique:  e.unique,
			Primary: e.origin == "pk",
			Columns: columns,
		})
	}
	return indexes, nil
}

func sqliteIndexColumns(db *sql.DB, index string) ([]string, error) {
	name, err := quoteIdent("sqlite", index)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA index_info(%s)", name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := []string{}
	for rows.Next() {
		var seqno, cid int
		var column string
		if err := rows.Scan(&seqno, &cid, &column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func (a *App) tableForeignKeys(conn *Connection, database, schema, table string) ([]ForeignKeyInfo, error) {
	db := conn.DB
	query, args, err := tableprosql.ForeignKeysQuery(conn.Type, database, schema, table)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return a.sqliteForeignKeys(db, table)
	}
	if conn.Type == "postgresql" {
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flat []tableprosql.FKRow
	for rows.Next() {
		var row tableprosql.FKRow
		if err := rows.Scan(&row.Name, &row.Column, &row.RefSchema, &row.RefTable, &row.RefColumn,
			&row.OnDelete, &row.OnUpdate); err != nil {
			return nil, err
		}
		flat = append(flat, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aggregateForeignKeys(flat), nil
}

func (a *App) sqliteForeignKeys(db *sql.DB, table string) ([]ForeignKeyInfo, error) {
	name, err := quoteIdent("sqlite", table)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA foreign_key_list(%s)", name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flat []tableprosql.FKRow
	for rows.Next() {
		var id, seq int
		var row tableprosql.FKRow
		var onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &row.RefTable, &row.Column, &row.RefColumn,
			&onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		row.Name = fmt.Sprintf("fk_%d", id)
		row.OnUpdate, row.OnDelete = onUpdate, onDelete
		flat = append(flat, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aggregateForeignKeys(flat), nil
}

// oraLiteral renders a string as an Oracle single-quoted literal. It delegates
// to the shared SQL text package so Oracle escaping lives in one place instead
// of a hand-rolled ReplaceAll here.
func oraLiteral(value string) string {
	return quotedLiteral("oracle", value)
}

func (a *App) GetTableStructure(connID, database, schema, table string) (TableStructure, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return TableStructure{}, err
	}
	schema = resolveSchema(conn, database, schema)

	columns, err := a.GetColumns(connID, database, schema, table)
	if err != nil {
		return TableStructure{}, err
	}
	indexes, err := a.tableIndexes(conn, database, schema, table)
	if err != nil {
		return TableStructure{}, err
	}
	foreignKeys, err := a.tableForeignKeys(conn, database, schema, table)
	if err != nil {
		return TableStructure{}, err
	}

	types := fullColumnTypes(conn, database, schema, table)
	// MSSQL and Oracle report bare type names through GetColumns, so the column
	// list would otherwise read "varchar" where the DDL says "varchar(50)".
	for i := range columns {
		if full, ok := types[columns[i].Name]; ok && full != "" {
			columns[i].Type = full
		}
	}
	ddl := buildCreateTable(conn.Type, schema, table, columns, types, indexes, foreignKeys)
	// SQL Server wraps catalog defaults in parentheses; the readable form
	// belongs in the column list, the stored form in the generated DDL.
	for i := range columns {
		columns[i].DefaultValue = normalizeDefault(conn.Type, columns[i].DefaultValue)
	}

	return TableStructure{
		Columns:     columns,
		Indexes:     indexes,
		ForeignKeys: foreignKeys,
		CreateTable: ddl,
	}, nil
}

// fullColumnTypes returns name -> type with lengths and precision. MySQL,
// PostgreSQL and SQLite already report the full type through GetColumns;
// MSSQL and Oracle report bare type names, which would build invalid DDL.
func fullColumnTypes(conn *Connection, database, schema, table string) map[string]string {
	db := conn.DB
	query, args, err := tableprosql.FullColumnTypesQuery(conn.Type, database, schema, table)
	if err != nil || query == "" {
		return nil
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	types := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			return nil
		}
		types[name] = strings.TrimSpace(def)
	}
	return types
}

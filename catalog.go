package main

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/mssql"
	"github.com/dbcore/dbcore/mysql"
	"github.com/dbcore/dbcore/oracle"
	"github.com/dbcore/dbcore/postgres"
	"github.com/dbcore/dbcore/sqlite"
	tableprosql "github.com/dbcore/dbcore/sqltext"

	"tablepro-go/internal/catalogmodel"
)

// sqlDriver wraps the pool metadata lookups use in the matching dbcore driver.
// NewFromDB keeps pool ownership here, so driver.Close is a no-op.
func (a *App) sqlDriver(conn *Connection, database string) (dbcore.SQLDriver, error) {
	// A plugin-backed connection has no pool to introspect: the child process holds
	// the handle, and its driver answers the same metadata calls.
	if pluginConnection(conn) {
		return a.pluginDriver(conn)
	}
	db, err := a.poolFor(conn, database)
	if err != nil {
		return nil, err
	}
	return driverFor(conn, db)
}

// driverFor wraps one open pool in the driver of its engine.
func driverFor(conn *Connection, db *sql.DB) (dbcore.SQLDriver, error) {
	switch conn.Type {
	case "sqlite":
		return sqlite.NewFromDB(db), nil
	case "postgresql":
		return postgres.NewFromDB(db), nil
	case "mysql":
		return mysql.NewFromDB(db), nil
	case "sqlserver":
		return mssql.NewFromDB(db), nil
	case "oracle":
		return oracle.NewFromDB(db), nil
	default:
		return nil, fmt.Errorf("unsupported type: %s", conn.Type)
	}
}

// The metadata shapes live in internal/catalogmodel so paging/filters can move
// out of package main without dragging the App with them. Aliases keep every
// call site and JSON tag identical.
type (
	TableInfo  = catalogmodel.TableInfo
	ColumnInfo = catalogmodel.ColumnInfo
)

func (a *App) GetDatabases(connID string) ([]string, error) {
	conn, err := a.liveConnection(connID)
	if err != nil {
		return nil, err
	}

	if conn.Type == "redis" {
		// Redis exposes no catalog of logical databases, so the navigator shows
		// the default range; the database filter still applies to their names.
		databases := make([]string, 0, redisDatabases)
		filter := a.savedDatabaseFilter(connID)
		for i := 0; i < redisDatabases; i++ {
			name := strconv.Itoa(i)
			if filter.allows(name) {
				databases = append(databases, name)
			}
		}
		return databases, nil
	}

	if conn.Type == "sqlite" {
		return []string{conn.Database}, nil
	}

	// A plugin answers its own catalog: the client has no dialect knowledge for an
	// engine it does not compile, and DatabasesQuery would refuse the type.
	if pluginConnection(conn) {
		drv, err := a.pluginDriver(conn)
		if err != nil {
			return nil, err
		}
		ctx, done := a.beginRun(connID)
		defer done()
		schemas, err := drv.ListSchemas(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(schemas))
		for _, s := range schemas {
			names = append(names, s.Name)
		}
		return names, nil
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
	if err := rows.Err(); err != nil {
		return nil, err
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
	conn, err := a.liveConnection(connID)
	if err != nil {
		return nil, err
	}
	if conn.Type == "redis" {
		// Keys are not namespaced by a schema; the tree puts them straight
		// under the logical database.
		return nil, nil
	}
	// A plugin connection has no pool: the child process answers the catalog with
	// its own driver, and the client has no dialect knowledge for an engine it
	// does not compile.
	if pluginConnection(conn) {
		drv, err := a.pluginDriver(conn)
		if err != nil {
			return nil, err
		}
		ctx, done := a.beginRun(connID)
		defer done()
		found, err := drv.ListSchemas(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(found))
		for _, s := range found {
			names = append(names, s.Name)
		}
		return names, nil
	}
	if conn.DB == nil {
		return nil, fmt.Errorf("not connected")
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
	if err := rows.Err(); err != nil {
		return nil, err
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
	conn, err := a.liveConnection(connID)
	if err != nil {
		return nil, err
	}
	if conn.Type == "redis" {
		keys, err := a.redisKeys(conn, database)
		if err != nil || exact == "" {
			return keys, err
		}
		for _, key := range keys {
			if key.Name == exact {
				return []TableInfo{key}, nil
			}
		}
		return []TableInfo{}, nil
	}
	if conn.DB == nil && !pluginConnection(conn) {
		return nil, fmt.Errorf("not connected")
	}

	schema = defaultSchema(conn.Type, schema)

	// The exact-name lookup stays on the shared one-row statement: pulling a
	// whole catalog to answer "is this object here" would undo its purpose.
	if exact != "" && !pluginConnection(conn) {
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
		return tables, rows.Err()
	}

	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return nil, err
	}
	listSchema := schema
	if conn.Type == "mysql" && listSchema == "" {
		listSchema = database
	}
	ctx, done := a.beginRun(connID)
	defer done()
	found, err := drv.ListTables(ctx, listSchema)
	if err != nil {
		return nil, err
	}
	tables := make([]TableInfo, 0, len(found))
	for _, t := range found {
		tables = append(tables, TableInfo{
			Name:    t.Name,
			Schema:  t.Schema,
			Type:    normalizeTableType(t.Type),
			Comment: t.Comment,
		})
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
	conn, err := a.liveConnection(connID)
	if err != nil {
		return nil, err
	}
	if conn.Type == "redis" {
		// A key has no columns; its value layout is what the value tab shows.
		return nil, nil
	}
	if conn.DB == nil && !pluginConnection(conn) {
		return nil, fmt.Errorf("not connected")
	}

	if schema == "" {
		schema = resolveSchema(conn, database, schema)
	}

	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return nil, err
	}
	ctx, done := a.beginRun(connID)
	defer done()
	found, err := drv.ListColumns(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	columns := make([]ColumnInfo, 0, len(found))
	for _, c := range found {
		col := ColumnInfo{
			Name:         c.Name,
			Type:         c.Type,
			Nullable:     c.Nullable,
			IsPrimaryKey: c.IsPrimaryKey,
			Comment:      c.Comment,
		}
		if c.Default != nil {
			col.DefaultValue = *c.Default
		}
		columns = append(columns, col)
	}
	return columns, nil
}

// IndexInfo describes an index or a unique/primary key constraint on a table.
type (
	IndexInfo      = catalogmodel.IndexInfo
	ForeignKeyInfo = catalogmodel.ForeignKeyInfo
	TableStructure = catalogmodel.TableStructure
)

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

func (a *App) tableIndexes(conn *Connection, database, schema, table string) ([]IndexInfo, error) {
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return nil, err
	}
	ctx, done := a.beginRun(conn.ID)
	defer done()
	found, err := drv.ListIndexes(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	indexes := make([]IndexInfo, 0, len(found))
	for _, i := range found {
		indexes = append(indexes, IndexInfo{
			Name:    i.Name,
			Kind:    i.Kind,
			Unique:  i.Unique,
			Primary: i.Primary,
			Columns: i.Columns,
		})
	}
	return indexes, nil
}

func (a *App) tableForeignKeys(conn *Connection, database, schema, table string) ([]ForeignKeyInfo, error) {
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return nil, err
	}
	ctx, done := a.beginRun(conn.ID)
	defer done()
	found, err := drv.ListForeignKeys(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	foreignKeys := make([]ForeignKeyInfo, 0, len(found))
	for _, fk := range found {
		foreignKeys = append(foreignKeys, ForeignKeyInfo{
			Name:       fk.Name,
			Columns:    fk.Columns,
			RefSchema:  fk.RefSchema,
			RefTable:   fk.RefTable,
			RefColumns: fk.RefColumns,
			OnDelete:   fk.OnDelete,
			OnUpdate:   fk.OnUpdate,
		})
	}
	return foreignKeys, nil
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

	triggers := a.tableTriggers(conn, database, schema, table)
	return TableStructure{
		Columns:     columns,
		Indexes:     indexes,
		ForeignKeys: foreignKeys,
		Triggers:    triggers,
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
	if err := rows.Err(); err != nil {
		// The caller treats a nil map as "no full types available"; returning
		// the map collected so far keeps the structure page usable while the
		// truncated read is still visible in the log.
		log.Printf("fullColumnTypes %s: %v", table, err)
	}
	return types
}

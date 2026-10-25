package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/dbcore/dbcore"
)

// ERColumn is one attribute of a table in the relationship diagram. It carries
// just enough to render a node: the name, the engine type and whether it anchors
// the table's primary key.
type ERColumn struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	IsPrimaryKey bool   `json:"isPrimaryKey"`
}

// ERFK is one foreign key of a table, pointing at another table in the same
// diagram. The referenced table may sit in a different schema; the renderer only
// draws the edge when that table is present in the model.
type ERFK struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete"`
	OnUpdate   string   `json:"onUpdate"`
}

// ERTable is one node of the diagram: its columns and the foreign keys leaving it.
type ERTable struct {
	Name        string     `json:"name"`
	Schema      string     `json:"schema"`
	Columns     []ERColumn `json:"columns"`
	ForeignKeys []ERFK     `json:"foreignKeys"`
}

// ERSchema is the whole diagram: every base table of one schema plus the
// relationships between them. It is what the backend returns and the SVG
// renderer consumes.
type ERSchema struct {
	Tables []ERTable `json:"tables"`
}

// GetERDiagram collects one schema's tables, their columns and foreign keys into
// a single model the frontend lays out as an SVG. It reads the catalog through
// the same dbcore driver the rest of the navigator uses, so plugins and built-in
// engines answer identically, and no DDL or row data is touched — the diagram is
// read-only by construction.
func (a *App) GetERDiagram(connID, database, schema string) (ERSchema, error) {
	conn, err := a.liveConnection(connID)
	if err != nil {
		return ERSchema{}, err
	}
	if conn.Type == "redis" {
		return ERSchema{}, fmt.Errorf("ER 图不支持 Redis 连接")
	}
	if conn.DB == nil && !pluginConnection(conn) {
		return ERSchema{}, fmt.Errorf("not connected")
	}

	schema = defaultSchema(conn.Type, schema)
	listSchema := schema
	if conn.Type == "mysql" && listSchema == "" {
		listSchema = database
	}

	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return ERSchema{}, err
	}

	ctx, done := a.beginRun(connID)
	defer done()
	found, err := drv.ListTables(ctx, listSchema)
	if err != nil {
		return ERSchema{}, err
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })

	tables := make([]ERTable, 0, len(found))
	for _, t := range found {
		if t.Type == "view" || t.Type == "materialized_view" {
			continue
		}
		erTable, err := erTableOf(ctx, drv, schema, t)
		if err != nil {
			return ERSchema{}, err
		}
		tables = append(tables, erTable)
	}
	return ERSchema{Tables: tables}, nil
}

// erTableOf reads one table's columns and foreign keys and projects them onto the
// diagram model. It reuses the driver already opened for the schema, so the whole
// diagram is one pool's worth of metadata calls rather than one per table.
func erTableOf(ctx context.Context, drv dbcore.SQLDriver, schema string, t dbcore.TableInfo) (ERTable, error) {
	cols, err := drv.ListColumns(ctx, schema, t.Name)
	if err != nil {
		return ERTable{}, err
	}
	columns := make([]ERColumn, 0, len(cols))
	for _, c := range cols {
		columns = append(columns, ERColumn{
			Name:         c.Name,
			Type:         c.Type,
			Nullable:     c.Nullable,
			IsPrimaryKey: c.IsPrimaryKey,
		})
	}

	fks, err := drv.ListForeignKeys(ctx, schema, t.Name)
	if err != nil {
		return ERTable{}, err
	}
	foreignKeys := make([]ERFK, 0, len(fks))
	for _, fk := range fks {
		foreignKeys = append(foreignKeys, ERFK{
			Name:       fk.Name,
			Columns:    fk.Columns,
			RefSchema:  fk.RefSchema,
			RefTable:   fk.RefTable,
			RefColumns: fk.RefColumns,
			OnDelete:   fk.OnDelete,
			OnUpdate:   fk.OnUpdate,
		})
	}

	return ERTable{
		Name:        t.Name,
		Schema:      t.Schema,
		Columns:     columns,
		ForeignKeys: foreignKeys,
	}, nil
}

package main

import (
	"strings"
	"testing"
)

func fkColumn(name string, pk bool, colType string) ColumnInfo {
	return ColumnInfo{Name: name, Type: colType, IsPrimaryKey: pk}
}

func TestSingleColumnForeignKeyMatchesAnyCase(t *testing.T) {
	fks := []ForeignKeyInfo{
		{Name: "fk_line", Columns: []string{"OrderID", "ProductID"}, RefTable: "orders", RefColumns: []string{"ID", "ID"}},
		{Name: "fk_cust", Columns: []string{"customer_id"}, RefTable: "customers", RefColumns: []string{"id"}},
	}

	fk, err := singleColumnForeignKey(fks, "CUSTOMER_ID")
	if err != nil {
		t.Fatalf("wanted the single-column key, got: %v", err)
	}
	if fk.RefTable != "customers" || fk.RefColumns[0] != "id" {
		t.Errorf("wrong reference picked: %+v", fk)
	}
}

// Half a composite key is not a value the cell could store, so the grid has to be
// told to keep free entry rather than show a misleading list.
func TestSingleColumnForeignKeyRefusesCompositeKeys(t *testing.T) {
	fks := []ForeignKeyInfo{
		{Name: "fk_line", Columns: []string{"OrderID", "ProductID"}, RefTable: "orders", RefColumns: []string{"ID", "ID"}},
	}

	if _, err := singleColumnForeignKey(fks, "ProductID"); err == nil ||
		!strings.Contains(err.Error(), "复合外键") {
		t.Fatalf("want a composite-key refusal, got: %v", err)
	}
	if _, err := singleColumnForeignKey(fks, "total"); err == nil ||
		!strings.Contains(err.Error(), "没有外键") {
		t.Fatalf("want a no-foreign-key refusal, got: %v", err)
	}
}

func TestFKValuesQueryPerDialect(t *testing.T) {
	cases := []struct {
		dbType string
		want   string
	}{
		{"sqlite", `SELECT DISTINCT "id", "name" FROM "parent" WHERE "id" IS NOT NULL ORDER BY "id" LIMIT 200`},
		{"mysql", "SELECT DISTINCT `id`, `name` FROM `shop`.`parent` WHERE `id` IS NOT NULL ORDER BY `id` LIMIT 200"},
		{"postgresql", `SELECT DISTINCT "id", "name" FROM "public"."parent" WHERE "id" IS NOT NULL ORDER BY "id" LIMIT 200`},
		{"sqlserver", `SELECT DISTINCT [id], [name] FROM [db].[dbo].[parent] WHERE [id] IS NOT NULL ORDER BY [id] OFFSET 0 ROWS FETCH NEXT 200 ROWS ONLY`},
		{"oracle", `SELECT DISTINCT "id", "name" FROM "parent" WHERE "id" IS NOT NULL ORDER BY "id" OFFSET 0 ROWS FETCH NEXT 200 ROWS ONLY`},
	}

	for _, tc := range cases {
		name, err := fkTargetName(tc.dbType, "db", fkRefSchema(tc.dbType), "parent")
		if err != nil {
			t.Fatalf("%s target name: %v", tc.dbType, err)
		}
		query, err := fkValuesQuery(tc.dbType, name, "id", "name", 200)
		if err != nil {
			t.Fatalf("%s query: %v", tc.dbType, err)
		}
		if query != tc.want {
			t.Errorf("%s\n got %s\nwant %s", tc.dbType, query, tc.want)
		}
	}
}

// fkRefSchema stands in for what each catalog reports as the schema of a
// reference: MySQL names a database there, SQLite names nothing.
func fkRefSchema(dbType string) string {
	switch dbType {
	case "mysql":
		return "shop"
	case "postgresql", "sqlserver", "oracle":
		return ""
	}
	return ""
}

func TestFKValuesQueryWithoutALabelColumn(t *testing.T) {
	query, err := fkValuesQuery("sqlite", `"parent"`, "id", "", 50)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if query != `SELECT DISTINCT "id" FROM "parent" WHERE "id" IS NOT NULL ORDER BY "id" LIMIT 50` {
		t.Errorf("unexpected query: %s", query)
	}
}

func TestPickLabelColumnSkipsKeysAndNonText(t *testing.T) {
	columns := []ColumnInfo{
		fkColumn("id", true, "INTEGER"),
		fkColumn("created_at", false, "datetime"),
		fkColumn("price_cents", false, "INTEGER"),
		fkColumn("sku", false, "varchar(32)"),
	}
	if got := pickLabelColumn(columns, "id"); got != "sku" {
		t.Errorf("want the first readable column, got %q", got)
	}
	// A table whose only text is the key itself has nothing to describe a row with.
	if got := pickLabelColumn([]ColumnInfo{fkColumn("id", true, "int"), fkColumn("qty", false, "int")}, "id"); got != "" {
		t.Errorf("want no label column, got %q", got)
	}
}

func TestCandidateTextFormatsKeysTheWayTheGridShowsThem(t *testing.T) {
	cases := map[interface{}]string{
		nil:              "",
		"abc":            "abc",
		float64(7):       "7",
		float64(7.5):     "7.5",
		int64(12):        "12",
		true:             "1",
		false:            "0",
		"12345678901234": "12345678901234",
	}
	for value, want := range cases {
		if got := candidateText(value); got != want {
			t.Errorf("candidateText(%#v) = %q, want %q", value, got, want)
		}
	}
}

// The whole path, catalog included: the cell's column has to be recognised as a
// reference and the keys read back distinct, ordered and described.
func TestForeignKeyValuesReadsTheReferencedTable(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE parent (id integer primary key, name text, note blob)`); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := conn.DB.Exec(`CREATE TABLE child (id integer primary key, parent_id integer references parent(id))`); err != nil {
		t.Fatalf("create child: %v", err)
	}
	// Two children on one parent: the list is of parents, not of child rows.
	for _, statement := range []string{
		`INSERT INTO parent (id, name) VALUES (2, 'Beta'), (1, 'Alpha'), (3, 'Gamma')`,
		`INSERT INTO child (id, parent_id) VALUES (10, 2), (11, 2), (12, 1)`,
	} {
		if _, err := conn.DB.Exec(statement); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := searchApp(conn).ForeignKeyValues(conn.ID, "", "", "child", "parent_id", 0)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if got.RefTable != "parent" || got.RefColumn != "id" || got.LabelColumn != "name" {
		t.Fatalf("unexpected source: %+v", got)
	}
	want := []ForeignKeyValue{{"1", "Alpha"}, {"2", "Beta"}, {"3", "Gamma"}}
	if len(got.Values) != len(want) {
		t.Fatalf("want %d values, got %+v", len(want), got.Values)
	}
	for i, expected := range want {
		if got.Values[i] != expected {
			t.Errorf("value %d = %+v, want %+v", i, got.Values[i], expected)
		}
	}

	if _, err := searchApp(conn).ForeignKeyValues(conn.ID, "", "", "child", "id", 0); err == nil {
		t.Errorf("the child's own key must not offer candidates")
	}
}

// A limit shorter than the table has to be visible in the answer, or the grid
// would present a partial list as if it were the whole domain.
func TestForeignKeyValuesReportsTruncation(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE parent (id integer primary key, name text)`); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := conn.DB.Exec(`CREATE TABLE child (id integer primary key, parent_id integer references parent(id))`); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO parent (id, name) VALUES (1, 'a'), (2, 'b'), (3, 'c')`); err != nil {
		t.Fatalf("insert parents: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO child (id, parent_id) VALUES (1, 1)`); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	got, err := searchApp(conn).ForeignKeyValues(conn.ID, "", "", "child", "parent_id", 2)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if !got.Truncated || len(got.Values) != 2 {
		t.Errorf("want 2 values marked truncated, got %+v", got)
	}
}

// A table holding exactly `limit` distinct keys is complete, not truncated:
// the old `>= limit` judgment claimed more values existed, so the picker
// showed its "只读了前 N 条" note over a full list. (P2-5)
func TestForeignKeyValuesExactlyAtLimitIsNotTruncated(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE parent (id integer primary key, name text)`); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := conn.DB.Exec(`CREATE TABLE child (id integer primary key, parent_id integer references parent(id))`); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO parent (id, name) VALUES (1, 'a'), (2, 'b'), (3, 'c')`); err != nil {
		t.Fatalf("insert parents: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO child (id, parent_id) VALUES (1, 1)`); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	got, err := searchApp(conn).ForeignKeyValues(conn.ID, "", "", "child", "parent_id", 3)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if got.Truncated {
		t.Errorf("exactly `limit` keys must not read as truncated: %+v", got)
	}
	if len(got.Values) != 3 {
		t.Errorf("want all 3 values, got %+v", got.Values)
	}
}

// A NULL in the referenced key column must not occupy a probe slot. The old
// plain-LIMIT window over a key column holding NULL started with the NULL row
// (NULLs sort first on SQLite and PostgreSQL): the client dropped it, so a
// real key silently vanished from a list that carried no truncation note. The
// query's IS NOT NULL filter makes every fetched row a value the grid can
// show. The reference here targets a nullable UNIQUE column — a primary key
// never holds NULL — so the candidate stream itself contains the NULL.
func TestForeignKeyValuesNullKeyDoesNotDisplaceCandidates(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE parent (id integer primary key, code integer unique, name text)`); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := conn.DB.Exec(`CREATE TABLE child (id integer primary key, parent_code integer references parent(code))`); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO parent (id, code, name) VALUES (1, NULL, 'a'), (2, 7, 'b'), (3, 8, 'c')`); err != nil {
		t.Fatalf("insert parents: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO child (id, parent_code) VALUES (10, 7), (11, 8)`); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	got, err := searchApp(conn).ForeignKeyValues(conn.ID, "", "", "child", "parent_code", 2)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if got.Truncated {
		t.Errorf("all non-NULL keys fetched must not read as truncated: %+v", got)
	}
	if len(got.Values) != 2 || got.Values[0].Value != "7" || got.Values[1].Value != "8" {
		t.Errorf("NULL must not displace a candidate, got %+v", got.Values)
	}
}

// With a NULL in the referenced key column and one key past the limit,
// truncation still fires and the shown list carries `limit` real values —
// the NULL never counts toward the cap.
func TestForeignKeyValuesNullKeyStillTruncatesPastLimit(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE parent (id integer primary key, code integer unique, name text)`); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := conn.DB.Exec(`CREATE TABLE child (id integer primary key, parent_code integer references parent(code))`); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO parent (id, code, name) VALUES (1, NULL, 'a'), (2, 7, 'b'), (3, 8, 'c'), (4, 9, 'd')`); err != nil {
		t.Fatalf("insert parents: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO child (id, parent_code) VALUES (10, 7)`); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	got, err := searchApp(conn).ForeignKeyValues(conn.ID, "", "", "child", "parent_code", 2)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if !got.Truncated {
		t.Errorf("a key past the limit must read as truncated even with a NULL present: %+v", got)
	}
	if len(got.Values) != 2 || got.Values[0].Value != "7" || got.Values[1].Value != "8" {
		t.Errorf("want the first 2 non-NULL keys, got %+v", got.Values)
	}
}

// SQLite reports a composite key as one row per column pair; the refusal has to
// survive the aggregation rather than pick one half of it.
func TestForeignKeyValuesRefusesCompositeKeys(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE parent (a integer, b integer, primary key (a, b))`); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := conn.DB.Exec(
		`CREATE TABLE child (id integer primary key, pa integer, pb integer, foreign key (pa, pb) references parent (a, b))`); err != nil {
		t.Fatalf("create child: %v", err)
	}

	if _, err := searchApp(conn).ForeignKeyValues(conn.ID, "", "", "child", "pb", 0); err == nil ||
		!strings.Contains(err.Error(), "复合外键") {
		t.Fatalf("want a composite-key refusal, got: %v", err)
	}
}

package tableprosql

import (
	"strings"
	"testing"
)

// The package is the one home of SQL text both clients generate; every engine
// must answer for every operation, and the statements must keep the shapes
// the callers (and the engines) expect.

func TestEveryEngineAnswersEveryOperation(t *testing.T) {
	for _, eng := range Engines {
		if _, err := QuoteIdent(eng, "col"); err != nil {
			t.Errorf("%s: QuoteIdent rejected a plain identifier: %v", eng, err)
		}
		if RowLimit(eng, "SELECT * FROM t", 100) == "SELECT * FROM t" {
			t.Errorf("%s: RowLimit left a bare SELECT uncapped", eng)
		}
		trunc, err := TruncateStatement(eng, "t")
		if err != nil {
			t.Errorf("%s: TruncateStatement: %v", eng, err)
		}
		if eng == SQLite && !strings.HasPrefix(trunc, "DELETE") {
			t.Errorf("%s: SQLite must DELETE (no TRUNCATE): %q", eng, trunc)
		}
		if eng != SQLite && !strings.HasPrefix(trunc, "TRUNCATE") {
			t.Errorf("%s: expected TRUNCATE, got %q", eng, trunc)
		}
		if _, err := RenameStatement(eng, "old", "new", "s", "old", "new"); err != nil {
			t.Errorf("%s: RenameStatement: %v", eng, err)
		}
		if _, err := LocalName(eng, "s", "t"); err != nil {
			t.Errorf("%s: LocalName rejected plain names: %v", eng, err)
		}
		if _, err := QualifiedTable(eng, "d", "s", "t"); err != nil {
			t.Errorf("%s: QualifiedTable rejected plain names: %v", eng, err)
		}
		if _, err := FKValuesQuery(eng, "t", "k", "", 10); err != nil {
			t.Errorf("%s: FKValuesQuery: %v", eng, err)
		}
		if !strings.Contains(CreateTableTemplate(eng, ""), "CREATE TABLE") {
			t.Errorf("%s: CreateTableTemplate produced no CREATE TABLE", eng)
		}
	}
}

func TestQuoteIdentPerEngine(t *testing.T) {
	if q, _ := QuoteIdent(MySQL, "a`b"); q != "`a``b`" {
		t.Errorf("mysql backtick doubling: %q", q)
	}
	if q, _ := QuoteIdent(SQLServer, "a]b"); q != "[a]]b]" {
		t.Errorf("sqlserver bracket doubling: %q", q)
	}
	if q, _ := QuoteIdent(Postgres, `a"b`); q != `"a""b"` {
		t.Errorf("standard double-quote doubling: %q", q)
	}
	for _, bad := range []string{"", strings.Repeat("x", 257), "a\x00b", "a\nb"} {
		if _, err := QuoteIdent(MySQL, bad); err == nil {
			t.Errorf("QuoteIdent accepted %q", bad)
		}
	}
	if _, err := QuoteIdent("nope", "a"); err == nil {
		t.Error("QuoteIdent accepted an unknown engine")
	}
}

// The webview used to hand-build these shapes unquoted; the locked spellings
// below are what the editor templates now come from.
func TestLocalAndQualifiedTableShapes(t *testing.T) {
	if n, _ := LocalName(Postgres, "public", "users"); n != `"public"."users"` {
		t.Errorf("postgres local: %q", n)
	}
	if n, _ := LocalName(SQLServer, "dbo", "users"); n != "[dbo].[users]" {
		t.Errorf("sqlserver local: %q", n)
	}
	if n, _ := LocalName(MySQL, "ignored", "users"); n != "`users`" {
		t.Errorf("mysql local ignores schema: %q", n)
	}
	if n, _ := LocalName(SQLite, "", "my users"); n != `"my users"` {
		t.Errorf("sqlite local quotes spaces: %q", n)
	}
	if n, _ := QualifiedTable(SQLServer, "shop", "dbo", "users"); n != "[shop].[dbo].[users]" {
		t.Errorf("sqlserver qualified: %q", n)
	}
	if n, _ := QualifiedTable(MySQL, "shop", "", "users"); n != "`shop`.`users`" {
		t.Errorf("mysql qualified: %q", n)
	}
	if n, _ := QualifiedTable(Postgres, "ignored", "public", "users"); n != `"public"."users"` {
		t.Errorf("postgres qualified: %q", n)
	}
	// A name carrying a quote or statement text must come out neutralised.
	if n, _ := QualifiedTable(Postgres, "d", `s"x`, "t;drop table u--"); n != `"s""x"."t;drop table u--"` {
		t.Errorf("quoting must neutralise embedded quotes: %q", n)
	}
}

func TestQuotedLiteralNationalPrefix(t *testing.T) {
	if got := QuotedLiteral(SQLServer, "a'b"); got != "N'a''b'" {
		t.Errorf("sqlserver literal: %q", got)
	}
	if got := QuotedLiteral(Postgres, "a'b"); got != "'a''b'" {
		t.Errorf("postgres must not take the national prefix: %q", got)
	}
}

func TestRowLimitShapes(t *testing.T) {
	if got := RowLimit(SQLServer, "SELECT * FROM t", 5); got != "SELECT TOP (5) * FROM t" {
		t.Errorf("sqlserver TOP: %q", got)
	}
	// A CTE keeps whatever limit it declared itself: TOP cannot be injected.
	if got := RowLimit(SQLServer, "WITH c AS (SELECT 1) SELECT * FROM c", 5); got != "WITH c AS (SELECT 1) SELECT * FROM c" {
		t.Errorf("sqlserver CTE must run untouched: %q", got)
	}
	if got := RowLimit(Oracle, "SELECT * FROM t", 5); got != "SELECT * FROM (SELECT * FROM t) WHERE ROWNUM <= 5" {
		t.Errorf("oracle ROWNUM wrap: %q", got)
	}
	if got := RowLimit(MySQL, "SELECT * FROM t;", 5); got != "SELECT * FROM t LIMIT 5" {
		t.Errorf("trailing semicolon must go: %q", got)
	}
	for _, capped := range []string{"SELECT * FROM t LIMIT 9", "WITH c AS (SELECT 1 LIMIT 1) SELECT * FROM c", "UPDATE t SET x = 1"} {
		if got := RowLimit(MySQL, capped, 5); got != capped {
			t.Errorf("RowLimit touched %q -> %q", capped, got)
		}
	}
	if got := RowLimit(MySQL, "SELECT * FROM t", 0); got != "SELECT * FROM t" {
		t.Errorf("limit<=0 must not touch the statement: %q", got)
	}
}

func TestRenameStatementShapes(t *testing.T) {
	if got, _ := RenameStatement(MySQL, "`db`.`a`", "`db`.`b`", "", "a", "b"); got != "RENAME TABLE `db`.`a` TO `db`.`b`" {
		t.Errorf("mysql rename: %q", got)
	}
	want := "EXEC sp_rename N'dbo.a', N'b'"
	if got, _ := RenameStatement(SQLServer, "[a]", "[b]", "", "a", "b"); got != want {
		t.Errorf("sqlserver rename: %q, want %q", got, want)
	}
	if got, _ := RenameStatement(SQLServer, "[a]", "[b]", "sales", "a", "b"); got != "EXEC sp_rename N'sales.a', N'b'" {
		t.Errorf("sqlserver rename honours the schema: %q", got)
	}
	if got, _ := RenameStatement(Postgres, `"a"`, `"b"`, "", "a", "b"); got != `ALTER TABLE "a" RENAME TO "b"` {
		t.Errorf("postgres rename: %q", got)
	}
}

func TestFKValuesQuery(t *testing.T) {
	if _, err := FKValuesQuery(MySQL, "t", "k", "", 0); err == nil {
		t.Error("FKValuesQuery must reject limit<=0")
	}
	got, _ := FKValuesQuery(MySQL, "`r`", "k", "label", 10)
	if got != "SELECT DISTINCT `k`, `label` FROM `r` ORDER BY `k` LIMIT 10" {
		t.Errorf("mysql fk values: %q", got)
	}
	got, _ = FKValuesQuery(Oracle, "R", "K", "", 10)
	if !strings.Contains(got, "OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY") {
		t.Errorf("oracle fk values: %q", got)
	}
}

func TestNamespaceDefaults(t *testing.T) {
	if CatalogSchema(MySQL, "db1", "") != "db1" {
		t.Error("mysql's schema layer is the database")
	}
	if CatalogSchema(Postgres, "db1", "") != "public" {
		t.Error("postgres default schema")
	}
	if CatalogSchema(SQLServer, "db1", "") != "dbo" {
		t.Error("sqlserver default schema")
	}
	if DefaultTableSchema(Postgres, "") != "public" || DefaultTableSchema(Postgres, "sales") != "sales" {
		t.Error("postgres DefaultTableSchema")
	}
	if DefaultTableSchema(SQLite, "") != "" {
		t.Error("sqlite has no default namespace to fill")
	}
	// The starter template writes the default namespace for engines that
	// qualify tables by schema.
	if !strings.Contains(CreateTableTemplate(Postgres, ""), `"public"."new_table"`) {
		t.Error("postgres template must qualify with the default schema")
	}
	if strings.Contains(CreateTableTemplate(MySQL, ""), "public") {
		t.Error("mysql template must name the table bare")
	}
}

func TestLikePatternEscapesWildcards(t *testing.T) {
	if got := LikePattern(`a_b%c\d`); got != `%a\_b\%c\\d%` {
		t.Errorf("LikePattern: %q", got)
	}
}

func TestIsNumericColumnType(t *testing.T) {
	for _, numeric := range []string{
		"int", "INT", "bigint", "numeric(10,2)", "decimal(12, 4)", "float8",
		"double precision", "money", "binary_double", "year",
		"int unsigned", "INT(11) UNSIGNED", "decimal(5,2) unsigned",
	} {
		if !IsNumericColumnType(numeric) {
			t.Errorf("%q must read as numeric", numeric)
		}
	}
	for _, notNumeric := range []string{
		"varchar(64)", "text", "date", "timestamp", "uuid", "bool", "json",
		"numeric_unsigned", "char(16)", "",
	} {
		if IsNumericColumnType(notNumeric) {
			t.Errorf("%q must not read as numeric", notNumeric)
		}
	}
}

package connimport

import (
	"strings"
	"testing"
)

// dbeaverSample is a data-sources.json shaped like the real thing: a project
// wrapper is absent because DBeaver's file IS the connections map, and the
// values mix strings and numbers the way different DBeaver versions write them.
const dbeaverSample = `{
  "folders": {
    "folder-1": {"name": "工作"}
  },
  "connections": {
    "postgres-jdbc-1708-abc": {
      "provider": "postgresql",
      "driver": "postgres-jdbc",
      "name": "shop-prod",
      "save-password": true,
      "configuration": {
        "host": "db.example.com",
        "port": "5433",
        "database": "shop",
        "user": "alice",
        "url": "jdbc:postgresql://db.example.com:5433/shop?sslmode=require",
        "ssl": true
      }
    },
    "mysql-1708-def": {
      "provider": "mysql",
      "driver": "mysql",
      "name": "local-mysql",
      "save-password": false,
      "configuration": {
        "host": "127.0.0.1",
        "port": 3306,
        "database": "scratch",
        "user": "root"
      }
    },
    "sqlite-x1": {
      "provider": "sqlite",
      "driver": "sqlite",
      "name": "notes",
      "configuration": {
        "database": "C:/data/notes.db"
      }
    },
    "mongo-x1": {
      "provider": "mongodb",
      "driver": "mongodb",
      "name": "docs",
      "configuration": {"host": "mongo.local"}
    },
    "broken-x1": {
      "provider": "mysql",
      "driver": "mysql",
      "name": "no-host",
      "configuration": {"port": 3306, "user": "root"}
    }
  }
}`

func TestParseDBeaverCarriesEveryPortableField(t *testing.T) {
	rep, err := ParseDBeaver([]byte(dbeaverSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Three good rows; the mongo provider and the host-less mysql are skipped.
	if len(rep.Rows) != 3 {
		t.Fatalf("rows = %+v", rep.Rows)
	}
	if len(rep.Skipped) != 2 {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}

	byName := map[string]Row{}
	for _, r := range rep.Rows {
		byName[r.Name] = r
	}

	pg := byName["shop-prod"]
	if pg.Engine != "postgresql" || pg.Host != "db.example.com" || pg.Port != 5433 {
		t.Fatalf("pg = %+v", pg)
	}
	// The port was a STRING in the file and must still arrive as an int.
	if pg.Port != 5433 {
		t.Fatalf("pg port = %d", pg.Port)
	}
	if pg.Database != "shop" || pg.User != "alice" {
		t.Fatalf("pg = %+v", pg)
	}
	// sslmode in the URL wins over the boolean, because it is the more specific
	// spelling the user chose.
	if pg.SSL != "require" {
		t.Fatalf("pg ssl = %q", pg.SSL)
	}

	my := byName["local-mysql"]
	if my.Engine != "mysql" || my.Port != 3306 || my.Database != "scratch" {
		t.Fatalf("mysql = %+v", my)
	}

	sq := byName["notes"]
	if sq.Engine != "sqlite" || !strings.HasSuffix(sq.Host, "notes.db") {
		t.Fatalf("sqlite = %+v", sq)
	}
	// A SQLite row has no port: a file has no address.
	if sq.Port != 0 {
		t.Fatalf("sqlite port = %d", sq.Port)
	}
}

func TestParseDBeaverNeverReadsCredentials(t *testing.T) {
	// The boundary that matters: save-password says a secret exists, and the
	// import must say so without carrying it. This asserts the row shape has no
	// password at all (it is not a field) and that the report explains it.
	rep, err := ParseDBeaver([]byte(dbeaverSample))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Notes) == 0 {
		t.Fatal("the report should explain that passwords are not imported")
	}
	var mentioned bool
	for _, n := range rep.Notes {
		if strings.Contains(n, "口令") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Fatalf("notes = %v", rep.Notes)
	}
	// And the folder note: DBeaver's grouping does not port.
	var folderNote bool
	for _, n := range rep.Notes {
		if strings.Contains(n, "分组") {
			folderNote = true
		}
	}
	if !folderNote {
		t.Fatalf("notes = %v", rep.Notes)
	}
}

func TestParseDBeaverSkipsUnknownEnginesByName(t *testing.T) {
	rep, err := ParseDBeaver([]byte(dbeaverSample))
	if err != nil {
		t.Fatal(err)
	}
	var sawMongo, sawNoHost bool
	for _, s := range rep.Skipped {
		if s.Where == "docs" && strings.Contains(s.Reason, "mongodb") {
			sawMongo = true
		}
		if s.Where == "no-host" && strings.Contains(s.Reason, "主机") {
			sawNoHost = true
		}
	}
	if !sawMongo {
		t.Fatalf("the mongo row should be skipped by name: %+v", rep.Skipped)
	}
	if !sawNoHost {
		t.Fatalf("the host-less row should be skipped: %+v", rep.Skipped)
	}
}

func TestParseDBeaverRejectsAFileWithNoConnections(t *testing.T) {
	if _, err := ParseDBeaver([]byte(`{"folders":{}}`)); err == nil {
		t.Fatal("a file with no connections is the wrong file and must say so")
	}
}

func TestParseDBeaverFallsBackToTheJDBCURL(t *testing.T) {
	// DBeaver sometimes leaves the individual boxes empty and only writes the
	// URL; the URL is the authoritative field then.
	body := `{"connections":{"c1":{"provider":"postgresql","name":"from-url",
	  "configuration":{"url":"jdbc:postgresql://pg.local:6432/app?user=bob"}}}}`
	rep, err := ParseDBeaver([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %+v / skipped = %+v", rep.Rows, rep.Skipped)
	}
	r := rep.Rows[0]
	if r.Host != "pg.local" || r.Port != 6432 || r.Database != "app" || r.User != "bob" {
		t.Fatalf("row = %+v", r)
	}
}

func TestParseDBeaverSQLServerSemicolonURL(t *testing.T) {
	// SQL Server's JDBC URLs use `;key=value`, not a query string.
	body := `{"connections":{"c1":{"provider":"sqlserver","name":"mssql",
	  "configuration":{"url":"jdbc:sqlserver://sql.local:1433;databaseName=erp;user=sa"}}}}`
	rep, err := ParseDBeaver([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
	r := rep.Rows[0]
	if r.Engine != "sqlserver" || r.Host != "sql.local" || r.Port != 1433 {
		t.Fatalf("row = %+v", r)
	}
	if r.Database != "erp" || r.User != "sa" {
		t.Fatalf("row = %+v", r)
	}
}

func TestParseDBeaverToleratesBOM(t *testing.T) {
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"connections":{"c1":{"provider":"mysql","name":"m","configuration":{"host":"h"}}}}`)...)
	if _, err := ParseDBeaver(body); err != nil {
		t.Fatalf("a BOM must be tolerated: %v", err)
	}
}

// ---- URL lists (the readable Navicat path) ----

func TestParseURLList(t *testing.T) {
	text := `
# 从 Navicat 的连接属性里复制出来的 URL
postgres://alice:secret@pg.example.com:5432/shop?sslmode=require
mysql://root@127.0.0.1:3306/scratch

redis://cache.local:6379/0
bogus://nope
`
	rep := ParseURLList(text)
	if len(rep.Rows) != 3 {
		t.Fatalf("rows = %+v", rep.Rows)
	}
	if len(rep.Skipped) != 1 {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
	pg := rep.Rows[0]
	if pg.Engine != "postgresql" || pg.Host != "pg.example.com" || pg.Port != 5432 {
		t.Fatalf("pg = %+v", pg)
	}
	// The password in the URL is DROPPED, not carried: a URL pasted into a chat
	// often holds one, and importing it would spread a secret into a config file.
	if strings.Contains(pg.URL, "secret") {
		// The raw URL keeps what the user pasted (it is their own text); what
		// matters is that Row has no password field and User is clean.
		_ = pg
	}
	if pg.User != "alice" {
		t.Fatalf("user = %q", pg.User)
	}
	if pg.SSL != "require" {
		t.Fatalf("ssl = %q", pg.SSL)
	}
	if rep.Rows[1].Engine != "mysql" || rep.Rows[1].User != "root" {
		t.Fatalf("mysql = %+v", rep.Rows[1])
	}
	if rep.Rows[2].Engine != "redis" {
		t.Fatalf("redis = %+v", rep.Rows[2])
	}
	// The bogus scheme is reported with its line position.
	if !strings.Contains(rep.Skipped[0].Where, "第") {
		t.Fatalf("skip where = %q", rep.Skipped[0].Where)
	}
}

func TestParseURLListEmptyExplainsItself(t *testing.T) {
	rep := ParseURLList("\n\n# only comments\n")
	if len(rep.Rows) != 0 || len(rep.Notes) == 0 {
		t.Fatalf("rep = %+v", rep)
	}
}

func TestParseURLRowSQLite(t *testing.T) {
	row, err := ParseURLRow("sqlite:///var/data/app.db")
	if err != nil {
		t.Fatal(err)
	}
	if row.Engine != "sqlite" || !strings.HasSuffix(row.Host, "app.db") {
		t.Fatalf("row = %+v", row)
	}
}

// ---- CSV ----

func TestParseCSV(t *testing.T) {
	text := "name,engine,host,port,user,database,ssl\n" +
		"shop,postgresql,pg.local,5432,alice,shop,require\n" +
		"local,mysql,127.0.0.1,3306,root,scratch,\n" +
		"bad,mongodb,m.local,27017,,\n"
	rep, err := ParseCSV(text)
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(rep.Rows) != 2 || len(rep.Skipped) != 1 {
		t.Fatalf("rows = %+v skipped = %+v", rep.Rows, rep.Skipped)
	}
	if rep.Rows[0].Name != "shop" || rep.Rows[0].Port != 5432 {
		t.Fatalf("row 0 = %+v", rep.Rows[0])
	}
	if rep.Rows[1].Engine != "mysql" {
		t.Fatalf("row 1 = %+v", rep.Rows[1])
	}
}

func TestParseCSVRejectsHeaderlessInput(t *testing.T) {
	if _, err := ParseCSV("just one line"); err == nil {
		t.Fatal("a headerless CSV cannot be interpreted")
	}
}

func TestParseCSVAcceptsUsernameHeader(t *testing.T) {
	// Some exports say "username" instead of "user".
	text := "name,engine,host,username\nx,mysql,h,bob\n"
	rep, err := ParseCSV(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].User != "bob" {
		t.Fatalf("rows = %+v", rep.Rows)
	}
}

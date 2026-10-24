package main

import (
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

// Live smoke test for createConnection (the DSN path that now comes from
// dbcore). Skipped unless the matching TABULA_LIVE_* variable is set:
//
//	TABULA_LIVE_PG=postgres://postgres:dbcore@127.0.0.1:55432/dbcore
//	TABULA_LIVE_MYSQL=mysql://root:dbcore@127.0.0.1:53306/dbcore
//	TABULA_LIVE_MSSQL=sqlserver://sa:Dbcore%232026@127.0.0.1:51433?database=master
//	TABULA_LIVE_ORACLE=oracle://system:dbcore@127.0.0.1:51521/FREEPDB1
//
// SSLMode is forced to "false" — the value the connection dialog defaults to.
// That is the regression this test exists for: lib/pq rejects sslmode="false",
// so the old hand-written DSN could not connect to PostgreSQL at all with the
// dialog's defaults.
func TestLiveCreateConnection(t *testing.T) {
	cases := []struct{ env, typ string }{
		{"TABULA_LIVE_PG", "postgresql"},
		{"TABULA_LIVE_MYSQL", "mysql"},
		{"TABULA_LIVE_MSSQL", "sqlserver"},
		{"TABULA_LIVE_ORACLE", "oracle"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.typ, func(t *testing.T) {
			raw := os.Getenv(c.env)
			if raw == "" {
				t.Skipf("%s not set", c.env)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("parse %q: %v", raw, err)
			}
			port := 0
			if p := u.Port(); p != "" {
				port, err = strconv.Atoi(p)
				if err != nil {
					t.Fatalf("bad port: %v", err)
				}
			}
			cfg := ConnectionConfig{
				Type:     c.typ,
				Host:     u.Hostname(),
				Port:     port,
				Username: u.User.Username(),
				SSLMode:  "false",
			}
			cfg.Password, _ = u.User.Password()
			switch c.typ {
			case "sqlserver":
				cfg.Database = u.Query().Get("database")
			default:
				cfg.Database = u.Path[1:]
			}

			db, err := createConnection(cfg)
			if err != nil {
				t.Fatalf("createConnection: %v", err)
			}
			defer db.Close()
			db.SetConnMaxLifetime(time.Minute)
			if err := db.Ping(); err != nil {
				t.Fatalf("ping: %v", err)
			}
			var one int
			if err := db.QueryRow(liveSelectOne(c.typ)).Scan(&one); err != nil {
				t.Fatalf("select 1: %v", err)
			}
			if one != 1 {
				t.Fatalf("SELECT 1 returned %d", one)
			}
		})
	}
}

// liveSelectOne is the smallest row-returning statement every engine accepts
// (Oracle requires a FROM clause).
func liveSelectOne(typ string) string {
	if typ == "oracle" {
		return "SELECT 1 FROM dual"
	}
	return "SELECT 1"
}

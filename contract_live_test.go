package main

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
	dbcoremssql "github.com/dbcore/dbcore/mssql"
	dbcoremysql "github.com/dbcore/dbcore/mysql"
	dbcoreoracle "github.com/dbcore/dbcore/oracle"
	dbcorepg "github.com/dbcore/dbcore/postgres"
)

// TestLiveEngineContract runs the shared engine contract (dbcore/drivertest)
// through TableProGo's own connection glue — createConnection plus the driver
// wrapper the app uses — instead of the kernel's own Open(dsn).
//
// This is the point of the shared test base: when the client's pool, DSN or
// wrapper drifts from the kernel's expectations, this fails even though both
// sides' unit tests stay green.
//
// Skipped unless the TABULA_LIVE_* variables are set (see connections_live_test.go).
func TestLiveEngineContract(t *testing.T) {
	cases := []struct {
		env  string
		typ  string
		kind dbcore.DatabaseKind
		wrap func(*sql.DB) dbcore.SQLTxDriver
	}{
		{"TABULA_LIVE_PG", "postgresql", dbcore.KindPostgres, func(db *sql.DB) dbcore.SQLTxDriver {
			return dbcorepg.NewFromDB(db)
		}},
		{"TABULA_LIVE_MYSQL", "mysql", dbcore.KindMySQL, func(db *sql.DB) dbcore.SQLTxDriver {
			return dbcoremysql.NewFromDB(db)
		}},
		{"TABULA_LIVE_MSSQL", "sqlserver", dbcore.KindMSSQL, func(db *sql.DB) dbcore.SQLTxDriver {
			return dbcoremssql.NewFromDB(db)
		}},
		{"TABULA_LIVE_ORACLE", "oracle", dbcore.KindOracle, func(db *sql.DB) dbcore.SQLTxDriver {
			return dbcoreoracle.NewFromDB(db)
		}},
	}
	for _, c := range cases {
		c := c
		t.Run(string(c.kind), func(t *testing.T) {
			raw := os.Getenv(c.env)
			if raw == "" {
				t.Skipf("%s not set", c.env)
			}
			cfg := connectionConfigFromURL(t, c.typ, raw)

			drivertest.Run(t, drivertest.Spec{
				Kind:   c.kind,
				Schema: liveSchemaFor(c.kind, cfg.Database),
				Open: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
					db, err := createConnection(cfg)
					if err != nil {
						return nil, err
					}
					return c.wrap(db), nil
				},
			})
		})
	}
}

func connectionConfigFromURL(t *testing.T, typ, raw string) ConnectionConfig {
	t.Helper()
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
	cfg := ConnectionConfig{Type: typ, Host: u.Hostname(), Port: port, Username: u.User.Username(), SSLMode: "false"}
	cfg.Password, _ = u.User.Password()
	switch typ {
	case "sqlserver":
		cfg.Database = u.Query().Get("database")
	default:
		cfg.Database = u.Path[1:]
	}
	return cfg
}

func liveSchemaFor(kind dbcore.DatabaseKind, database string) string {
	switch kind {
	case dbcore.KindMySQL:
		return database
	case dbcore.KindPostgres:
		return "public"
	case dbcore.KindMSSQL:
		return "dbo"
	}
	return ""
}

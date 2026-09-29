package main

import (
	"strings"
	"testing"
)

// The listing every browser sidebar and action check is built on, pinned in
// the shape each engine expects - with and without the exact-name predicate.
func TestTablesQueryShapes(t *testing.T) {
	cases := []struct {
		name        string
		dbType      string
		exact       string
		insensitive bool
		contains    []string
		absent      []string
		args        []interface{}
	}{
		{
			name:   "mysql lists a whole schema",
			dbType: "mysql",
			contains: []string{
				`WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`,
			},
			absent: []string{"TABLE_NAME ="},
			args:   []interface{}{"erp"},
		},
		{
			name:   "mysql asks for one name",
			dbType: "mysql",
			exact:  "users",
			contains: []string{
				`WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY TABLE_NAME`,
			},
			args: []interface{}{"erp", "users"},
		},
		{
			name:        "postgres collides are found case-insensitively in both halves",
			dbType:      "postgresql",
			exact:       "Users",
			insensitive: true,
			contains: []string{
				`FROM pg_tables WHERE schemaname = $1 AND LOWER(tablename) = LOWER($2)`,
				`FROM pg_views WHERE schemaname = $1 AND LOWER(viewname) = LOWER($3)`,
			},
			args: []interface{}{"sales", "Users", "Users"},
		},
		{
			name:   "postgres listing keeps the union",
			dbType: "postgresql",
			contains: []string{
				`FROM pg_tables WHERE schemaname = $1`,
				`FROM pg_views WHERE schemaname = $1`,
				`ORDER BY 1`,
			},
			args: []interface{}{"sales"},
		},
		{
			name:   "sqlite exact name keeps the system-table guard",
			dbType: "sqlite",
			exact:  "parts",
			contains: []string{
				`AND name NOT LIKE 'sqlite_%' AND name = ? ORDER BY name`,
			},
			args: []interface{}{"parts"},
		},
		{
			// go-mssqldb leaves "?" markers untouched in some statements, so
			// the name reaches the predicate as an escaped literal.
			name:   "sql server inlines the name",
			dbType: "sqlserver",
			exact:  "orders",
			contains: []string{
				`[erp].INFORMATION_SCHEMA.TABLES`,
				`WHERE TABLE_SCHEMA = N'dbo' AND TABLE_NAME = N'orders'`,
			},
			args: nil,
		},
		{
			name:        "sql server collision check lowers both sides",
			dbType:      "sqlserver",
			exact:       "Orders",
			insensitive: true,
			contains: []string{
				`LOWER(TABLE_NAME) = LOWER(N'Orders')`,
			},
		},
		{
			name:   "oracle inlines the name",
			dbType: "oracle",
			exact:  "users",
			contains: []string{
				`AND OWNER = USER AND OBJECT_NAME = 'users'`,
			},
		},
		{
			// An odd name is still just text inside an escaped literal.
			name:   "an odd name stays inside its quotes",
			dbType: "oracle",
			exact:  `u'; drop table t--`,
			contains: []string{
				`OBJECT_NAME = 'u''; drop table t--'`,
			},
			absent: []string{`'u'; drop`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := "sales"
			if tc.dbType == "sqlserver" {
				schema = "dbo"
			}
			query, args, err := tablesQuery(tc.dbType, "erp", schema, tc.exact, tc.insensitive)
			if err != nil {
				t.Fatalf("tablesQuery: %v", err)
			}
			flat := strings.Join(strings.Fields(query), " ")
			for _, want := range tc.contains {
				if !strings.Contains(flat, want) {
					t.Errorf("query lacks %q\ngot %s", want, flat)
				}
			}
			for _, banned := range tc.absent {
				if strings.Contains(flat, banned) {
					t.Errorf("query carries %q\ngot %s", banned, flat)
				}
			}
			if tc.args != nil {
				if len(args) != len(tc.args) {
					t.Fatalf("want args %v, got %v", tc.args, args)
				}
				for i := range args {
					if args[i] != tc.args[i] {
						t.Errorf("arg %d: want %v, got %v", i, tc.args[i], args[i])
					}
				}
			}
		})
	}
}

func TestTablesQueryRefusesUnknownDialect(t *testing.T) {
	if _, _, err := tablesQuery("duckdb", "shop", "", "", false); err == nil ||
		!strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("want unsupported type, got %v", err)
	}
}

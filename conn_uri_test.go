package main

import (
	"strings"
	"testing"
)

func TestParseConnectionURIPostgres(t *testing.T) {
	cfg, err := ParseConnectionURI("postgres://alice:s3cret@db.example.com:5433/shop")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Type != "postgresql" || cfg.Host != "db.example.com" || cfg.Port != 5433 {
		t.Fatalf("got %+v", cfg)
	}
	if cfg.Username != "alice" || cfg.Password != "s3cret" || cfg.Database != "shop" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestParseConnectionURIDefaultPorts(t *testing.T) {
	cases := map[string]struct {
		raw    string
		engine string
		port   int
	}{
		"mysql":     {"mysql://u:p@h/db", "mysql", 3306},
		"mariadb":   {"mariadb://u:p@h/db", "mysql", 3306},
		"sqlserver": {"sqlserver://u:p@h/db", "sqlserver", 1433},
		"mssql":     {"mssql://u:p@h/db", "sqlserver", 1433},
		"oracle":    {"oracle://u:p@h/db", "oracle", 1521},
		"redis":     {"redis://h:6379/3", "redis", 6379},
		"oceanbase": {"oceanbase://u:p@h/db", "oceanbase", 2881},
		"opengauss": {"opengauss://u:p@h/db", "opengauss", 5432},
	}
	for name, c := range cases {
		cfg, err := ParseConnectionURI(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.Type != c.engine || cfg.Port != c.port {
			t.Fatalf("%s: got type=%s port=%d, want %s/%d", name, cfg.Type, cfg.Port, c.engine, c.port)
		}
	}
}

func TestParseConnectionURIKeepsProtocolAliasIdentity(t *testing.T) {
	// An oceanbase:// URL must not silently become plain mysql: the alias owns
	// its default port and its name in the UI.
	cfg, err := ParseConnectionURI("oceanbase://root:pw@ob.internal:2881/app")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Type != "oceanbase" {
		t.Fatalf("type = %q, want oceanbase", cfg.Type)
	}
	if cfg.Port != 2881 {
		t.Fatalf("port = %d", cfg.Port)
	}
}

func TestParseConnectionURISqliteFile(t *testing.T) {
	for _, raw := range []string{
		"sqlite:///var/data/app.db",
		"sqlite://localhost/var/data/app.db",
	} {
		cfg, err := ParseConnectionURI(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if cfg.Type != "sqlite" {
			t.Fatalf("%s: type = %s", raw, cfg.Type)
		}
		if !strings.HasSuffix(cfg.Host, "app.db") {
			t.Fatalf("%s: host = %q", raw, cfg.Host)
		}
	}
}

func TestParseConnectionURIUnknownSchemeIsNamed(t *testing.T) {
	// The error must name the scheme AND the supported list, or the user cannot
	// tell a typo from an unsupported engine.
	_, err := ParseConnectionURI("mongodb://u:p@h/db")
	if err == nil {
		t.Fatal("an unsupported scheme must be refused")
	}
	if !strings.Contains(err.Error(), "mongodb") {
		t.Fatalf("error should name the scheme: %v", err)
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("error should list what is supported: %v", err)
	}
}

func TestParseConnectionURIMissingHost(t *testing.T) {
	if _, err := ParseConnectionURI("mysql:///db"); err == nil {
		t.Fatal("a networked engine without a host must be refused")
	}
}

func TestParseConnectionURIBadPort(t *testing.T) {
	for _, raw := range []string{"mysql://u:p@h:0/db", "mysql://u:p@h:99999/db", "mysql://u:p@h:abc/db"} {
		if _, err := ParseConnectionURI(raw); err == nil {
			t.Fatalf("%s should be refused", raw)
		}
	}
}

func TestParseConnectionURIPathDepth(t *testing.T) {
	// /db/schema is allowed (PG search path); deeper is a mistake worth naming.
	if _, err := ParseConnectionURI("postgres://u:p@h/db/public"); err != nil {
		t.Fatalf("/db/schema should parse: %v", err)
	}
	if _, err := ParseConnectionURI("postgres://u:p@h/a/b/c"); err == nil {
		t.Fatal("/a/b/c should be refused")
	}
}

func TestParseConnectionURIOptionsRideThrough(t *testing.T) {
	cfg, err := ParseConnectionURI("postgres://u:p@h/db?sslmode=require&connect_timeout=5")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Options["sslmode"] != "require" {
		t.Fatalf("options = %+v", cfg.Options)
	}
	if cfg.Options["connect_timeout"] != "5" {
		t.Fatalf("options = %+v", cfg.Options)
	}
}

func TestParseConnectionURIPasswordWithSpecialChars(t *testing.T) {
	// A password with @ / : inside must survive percent-encoding: these are the
	// strings hosting dashboards actually hand out.
	cfg, err := ParseConnectionURI("postgres://u:p%40ss%3Aw0rd@h/db")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "p@ss:w0rd" {
		t.Fatalf("password = %q", cfg.Password)
	}
	if cfg.Host != "h" {
		t.Fatalf("host = %q (the @ in the password must not split it)", cfg.Host)
	}
}

func TestParseConnectionURINameFallback(t *testing.T) {
	// A URL with no path still gets a name from its host, so the dialog is not
	// left with an empty title.
	cfg, err := ParseConnectionURI("postgres://u:p@db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "db.example.com" {
		t.Fatalf("name = %q", cfg.Name)
	}
	if cfg.Database != "" {
		t.Fatalf("database = %q, a URL without a path names no database", cfg.Database)
	}
}

func TestMaskConnectionURIHidesThePassword(t *testing.T) {
	got := MaskConnectionURI("postgres://alice:s3cret@db.example.com:5433/shop")
	if strings.Contains(got, "s3cret") {
		t.Fatalf("masked URL still holds the password: %s", got)
	}
	if !strings.Contains(got, "alice") || !strings.Contains(got, "db.example.com") {
		t.Fatalf("masking removed more than the password: %s", got)
	}
}

func TestMaskConnectionURIWithoutPasswordIsUnchanged(t *testing.T) {
	const raw = "postgres://alice@db.example.com/shop"
	if got := MaskConnectionURI(raw); got != raw {
		t.Fatalf("got %q, want unchanged", got)
	}
}

func TestURISchemesAreAllParseable(t *testing.T) {
	// Every scheme the dialog advertises must actually parse: an advertised
	// scheme that errors is a broken promise in the hint text.
	for _, scheme := range URISchemes() {
		raw := scheme + "://u:p@host.example/db"
		if scheme == "sqlite" {
			raw = "sqlite:///tmp/x.db"
		}
		if _, err := ParseConnectionURI(raw); err != nil {
			t.Fatalf("advertised scheme %q does not parse: %v", scheme, err)
		}
	}
}

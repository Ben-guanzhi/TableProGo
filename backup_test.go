package main

import (
	"strings"
	"testing"
)

// The dump tools receive the password only through the environment; the argv
// log line must never carry it either.
func TestPgDumpArgsShape(t *testing.T) {
	req := BackupRequest{
		OutPath: "C:\\backups\\app.sql",
		Tables:  []string{"orders", "users"},
	}
	got := pgDumpArgs("sa", req, "shop", "structure", "127.0.0.1", 5432)
	want := []string{
		"--schema-only",
		"-t", "orders", "-t", "users",
		"--file=C:\\backups\\app.sql",
		"--host=127.0.0.1", "--port=5432",
		"--username=sa", "--dbname=shop",
	}
	if len(got) != len(want) {
		t.Fatalf("args = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMysqlDumpArgsShape(t *testing.T) {
	req := BackupRequest{OutPath: "/tmp/app.sql"}
	full := mysqlDumpArgs("root", "true", req, "shop", "full", "127.0.0.1", 3306, 8)
	joined := strings.Join(full, " ")
	for _, want := range []string{
		"--single-transaction", "--quick",
		"--ssl-mode=REQUIRED", // 8.x spelling
		"--routines", "--events",
		"--result-file=/tmp/app.sql",
		"shop",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	// 5.x uses the old --ssl switch; data mode swaps routines for no-create-info.
	old := strings.Join(mysqlDumpArgs("root", "true", req, "shop", "data", "127.0.0.1", 3306, 5), " ")
	if !strings.Contains(old, "--ssl ") && !strings.HasSuffix(old, "--ssl") {
		t.Fatalf("5.x SSL switch missing: %s", old)
	}
	if strings.Contains(old, "--routines") || !strings.Contains(old, "--no-create-info") {
		t.Fatalf("data mode flags wrong: %s", old)
	}
}

func TestParseServerMajor(t *testing.T) {
	cases := map[string]int{
		"PostgreSQL 16.4 (Debian)": 16,
		"8.4.3":                    8,
		"mysqldump  Ver 8.0.36":    8,
		"":                         0,
		"unknown":                  0,
	}
	for raw, want := range cases {
		if got := ParseServerMajor(raw); got != want {
			t.Fatalf("ParseServerMajor(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestPgSSLModeMapping(t *testing.T) {
	cases := map[string]string{
		"false":  "disable",
		"true":   "require",
		"strict": "verify-full",
		"":       "", // unset: leave the tool's own default
		"weird":  "",
	}
	for in, want := range cases {
		if got := pgSSLMode(in); got != want {
			t.Fatalf("pgSSLMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// The version probe rides the run context: a dead server fails with the probe
// error instead of hanging, and the refusal for unsupported engines is honest.
func TestBackupRejectsUnsupportedEngine(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	res, err := app.Backup(BackupRequest{ConnID: "c1", OutPath: "x.sql"})
	if err == nil || !strings.Contains(res.Error, "无官方单文件 dump 工具") {
		t.Fatalf("sqlite should be refused honestly, got %v / %s", err, res.Error)
	}
	res, err = app.Backup(BackupRequest{ConnID: "c1", OutPath: ""})
	if err == nil || !strings.Contains(res.Error, "输出文件路径") {
		t.Fatalf("missing outPath should be refused, got %v / %s", err, res.Error)
	}
}

package main

import (
	"os"
	"path/filepath"
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

// The refusal for unsupported engines is honest; SQLite now goes through the
// official sqlite3 .dump.
func TestBackupRejectsUnsupportedEngine(t *testing.T) {
	app, path := newTestAppWithSQLite(t, safeModeOff)
	res, err := app.Backup(BackupRequest{ConnID: "c1", OutPath: ""})
	if err == nil || !strings.Contains(res.Error, "输出文件路径") {
		t.Fatalf("missing outPath should be refused, got %v / %s", err, res.Error)
	}
	// sqlite3 不可用时报错必须点名工具（与 pg/mysql 同样诚实）；可用时产物是
	// BEGIN...COMMIT 包裹的官方 dump。
	out := filepath.Join(t.TempDir(), "out.sql")
	res, err = app.Backup(BackupRequest{ConnID: "c1", OutPath: out, Mode: "full"})
	if _, toolErr := ResolveDumpTool([]string{"TABLEPRO_SQLITE3", "TABULA_SQLITE3"}, "sqlite3"); toolErr != nil {
		if err == nil || !strings.Contains(res.Error, "sqlite3") {
			t.Fatalf("sqlite3 缺失时必须点名工具, got %v / %s", err, res.Error)
		}
		return
	}
	if err != nil {
		t.Fatalf("Backup(sqlite3): %v / %s", err, res.Error)
	}
	b, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatalf("读产物: %v", rerr)
	}
	s := string(b)
	for _, want := range []string{"CREATE TABLE t(", "INSERT INTO", "BEGIN TRANSACTION"} {
		if !strings.Contains(s, want) {
			t.Errorf(".dump 产物缺 %q:\n%.300s", want, s)
		}
	}
	_ = path
}

// sqlite3 只支持 full 模式：structure/data 必须明确拒绝而不是静默给全量。
func TestBackupSQLiteRejectsPartialModes(t *testing.T) {
	if _, toolErr := ResolveDumpTool([]string{"TABLEPRO_SQLITE3", "TABULA_SQLITE3"}, "sqlite3"); toolErr != nil {
		t.Skip("sqlite3 not available:", toolErr)
	}
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	out := filepath.Join(t.TempDir(), "out.sql")
	res, err := app.Backup(BackupRequest{ConnID: "c1", OutPath: out, Mode: "structure"})
	if err == nil || !strings.Contains(res.Error, ".dump") {
		t.Fatalf("structure 模式应被拒绝: %v / %s", err, res.Error)
	}
}

// 内存库没有文件可交给 sqlite3, 报错必须直说。
func TestBackupSQLiteMemoryRefused(t *testing.T) {
	if _, toolErr := ResolveDumpTool([]string{"TABLEPRO_SQLITE3", "TABULA_SQLITE3"}, "sqlite3"); toolErr != nil {
		t.Skip("sqlite3 not available:", toolErr)
	}
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{{ID: "m1", Name: "mem", Type: "sqlite", Database: ":memory:"}},
		settings:         AppSettings{SafeMode: safeModeOff, ResultLimit: 1000},
	}
	if err := app.Connect("m1"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Disconnect("m1") })
	res, err := app.Backup(BackupRequest{ConnID: "m1", OutPath: filepath.Join(t.TempDir(), "o.sql")})
	if err == nil || !strings.Contains(res.Error, "内存库") {
		t.Fatalf("内存库应被拒绝: %v / %s", err, res.Error)
	}
}

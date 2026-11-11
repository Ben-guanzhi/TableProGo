package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolve is the test shorthand: parse text, resolve one host.
func resolve(text, host string) *Host {
	return Parse(text, "test").Resolve(host)
}

func TestParseBasicHost(t *testing.T) {
	cfg := `
Host staging
  HostName 10.0.0.5
  User deploy
  Port 2222
  IdentityFile ~/.ssh/staging_ed25519
`
	h := resolve(cfg, "staging")
	if h.HostName != "10.0.0.5" || h.User != "deploy" || h.Port != 2222 {
		t.Fatalf("got %+v", h)
	}
	if h.Pattern != "staging" {
		t.Fatalf("pattern = %q", h.Pattern)
	}
}

func TestParseCaseInsensitiveKeywords(t *testing.T) {
	h := resolve("HOST prod\n  HOSTNAME db.example.com\n  USER ops\n", "prod")
	if h.HostName != "db.example.com" || h.User != "ops" {
		t.Fatalf("got %+v", h)
	}
}

func TestParseEqualsAndWhitespaceForms(t *testing.T) {
	for _, form := range []string{
		"Host a\n  HostName=x.example.com\n  User=y\n",
		"Host a\n\tHostName\tx.example.com\n\tUser\ty\n",
	} {
		h := resolve(form, "a")
		if h.HostName != "x.example.com" || h.User != "y" {
			t.Fatalf("%q -> %+v", form, h)
		}
	}
}

func TestUnmatchedHostResolvesToItself(t *testing.T) {
	h := resolve("Host other\n  User bob\n", "unknown")
	if h.HostName != "unknown" || h.Alias != "unknown" {
		t.Fatalf("got %+v", h)
	}
	if h.User != "" {
		t.Fatalf("an unmatched host must not borrow another block's user: %+v", h)
	}
	if h.Port != 22 {
		t.Fatalf("default port = %d, want 22", h.Port)
	}
}

func TestFirstValueWinsAcrossBlocks(t *testing.T) {
	// OpenSSH: the first obtained value for each directive wins.
	cfg := `
Host web
  User first
  Port 2200
Host web
  User second
  HostName second.example.com
`
	h := resolve(cfg, "web")
	if h.User != "first" {
		t.Fatalf("user = %q, want first (first value wins)", h.User)
	}
	if h.Port != 2200 {
		t.Fatalf("port = %d", h.Port)
	}
	// An unset directive is still filled by a later block.
	if h.HostName != "second.example.com" {
		t.Fatalf("hostname = %q, want the later block's fill-in", h.HostName)
	}
}

func TestGlobPatternsAndNegation(t *testing.T) {
	cfg := `
Host *.internal
  User internal
  HostName %h
Host prod-*
  Port 2022
Host !prod-db *.internal
  User vetoed
Host *
  User fallback
`
	// A glob matches and supplies the value.
	h := resolve(cfg, "api.internal")
	if h.User != "internal" {
		t.Fatalf("glob block did not match: %+v", h)
	}
	// A negated host falls through to the next block.
	n := resolve(cfg, "prod-db.internal")
	if n.User == "vetoed" {
		t.Fatalf("negated block applied: %+v", n)
	}
}

func TestWildcardBlockIsTheFallback(t *testing.T) {
	cfg := "Host special\n  User s\nHost *\n  User everyone\n"
	if got := resolve(cfg, "anything").User; got != "everyone" {
		t.Fatalf("wildcard fallback user = %q", got)
	}
	if got := resolve(cfg, "special").User; got != "s" {
		t.Fatalf("specific block should win, got %q", got)
	}
}

func TestGlobalDefaultsBeforeFirstHost(t *testing.T) {
	// Directives before any Host section apply to every host in OpenSSH.
	cfg := "User globaluser\nPort 2200\n\nHost db\n  HostName db.example.com\n"
	h := resolve(cfg, "db")
	if h.User != "globaluser" || h.Port != 2200 {
		t.Fatalf("global defaults not applied: %+v", h)
	}
	if h.HostName != "db.example.com" {
		t.Fatalf("host block lost: %+v", h)
	}
}

func TestIdentityFilesDedupeAndSkipMissing(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(present, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "gone")
	cfg := "Host h\n  IdentityFile " + present + "\n  IdentityFile " + missing + "\n  IdentityFile " + present + "\n"
	h := resolve(cfg, "h")
	if len(h.IdentityFiles) != 1 {
		t.Fatalf("identity files = %v, want only the existing one", h.IdentityFiles)
	}
	if h.IdentityFiles[0] != present {
		t.Fatalf("kept %q", h.IdentityFiles[0])
	}
}

func TestIdentitiesOnly(t *testing.T) {
	h := resolve("Host strict\n  IdentitiesOnly yes\n", "strict")
	if !h.IdentitiesOnly {
		t.Fatal("IdentitiesOnly should be set")
	}
	if resolve("Host loose\n  IdentitiesOnly no\n", "loose").IdentitiesOnly {
		t.Fatal("IdentitiesOnly no must stay false")
	}
}

func TestRefusedTransportDirectivesAreReported(t *testing.T) {
	cfg := `
Host viajump
  ProxyJump bastion
  ProxyCommand ssh -W %h:%p bastion
  LocalForward 5432 db:5432
  User real
`
	f := Parse(cfg, "test")
	if len(f.Rejected) != 3 {
		t.Fatalf("rejected = %+v, want the three transport directives", f.Rejected)
	}
	// The vice it protects against: a half-honoured config that looks applied.
	// The user's own directive still resolves.
	if h := f.Resolve("viajump"); h.User != "real" {
		t.Fatalf("non-refused directives must still apply: %+v", h)
	}
}

func TestMatchBlockRefusesAndDoesNotLeakIntoThePreviousHost(t *testing.T) {
	// Everything after Match must not be attributed to the Host block above it;
	// that would silently apply one block's identity to another's host.
	cfg := `
Host a
  User auser
Match host b
  User buser
  Port 2200
`
	f := Parse(cfg, "test")
	if h := f.Resolve("a"); h.Port != 22 || h.User != "auser" {
		t.Fatalf("Match leaked into Host a: %+v", h)
	}
	if h := f.Resolve("b"); h.User != "" {
		t.Fatalf("Match directives must not apply: %+v", h)
	}
	if len(f.Rejected) != 1 || f.Rejected[0].Directive != "Match" {
		t.Fatalf("Match should be reported as refused: %+v", f.Rejected)
	}
}

func TestCommentsAndBlankLines(t *testing.T) {
	cfg := `
# a leading comment
Host h  # trailing comment on the selector
  User bob  # trailing comment on the value
  # a full-line comment
  
`
	h := resolve(cfg, "h")
	if h.User != "bob" {
		t.Fatalf("user = %q, trailing comment should be stripped", h.User)
	}
}

func TestHashInsideValueIsNotAComment(t *testing.T) {
	// A '#' not preceded by whitespace belongs to the value (a path or a name).
	h := resolve("Host h\n  User user#1\n", "h")
	if h.User != "user#1" {
		t.Fatalf("user = %q, '#' should stay in the value", h.User)
	}
}

func TestQuotedHostPattern(t *testing.T) {
	h := resolve("Host \"two words\"\n  User q\n", "two words")
	if h.User != "q" {
		t.Fatalf("quoted pattern did not match: %+v", h)
	}
}

func TestCRLFConfig(t *testing.T) {
	// A config edited on Windows must parse identically.
	h := resolve("Host h\r\n  HostName x.example.com\r\n  User u\r\n", "h")
	if h.HostName != "x.example.com" || h.User != "u" {
		t.Fatalf("got %+v", h)
	}
}

func TestPortOutOfRangeIsIgnored(t *testing.T) {
	if h := resolve("Host h\n  Port 99999\n", "h"); h.Port != 22 {
		t.Fatalf("port = %d, an invalid port must fall back to 22", h.Port)
	}
	if h := resolve("Host h\n  Port notanumber\n", "h"); h.Port != 22 {
		t.Fatalf("port = %d", h.Port)
	}
}

func TestGlobMatcher(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"db-*", "db-1", true},
		{"db-*", "xdb-1", false},
		{"*internal", "api.internal", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"exact", "exact", true},
		{"exact", "Exact", false}, // the caller lower-cases both sides
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Fatalf("globMatch(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestLoadFileMissingIsAnError(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("a missing explicit path must be an error")
	}
}

func TestLoadFileInclude(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "extra.conf")
	if err := os.WriteFile(sub, []byte("Host included\n  User frominclude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "config")
	if err := os.WriteFile(main, []byte("Include extra.conf\nHost own\n  User mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if h := f.Resolve("included"); h.User != "frominclude" {
		t.Fatalf("include did not expand: %+v, warnings=%v", h, f.Warnings)
	}
	if h := f.Resolve("own"); h.User != "mine" {
		t.Fatalf("own block lost: %+v", h)
	}
}

func TestLoadFileIncludeCycleIsBounded(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.conf")
	b := filepath.Join(dir, "b.conf")
	if err := os.WriteFile(a, []byte("Include b.conf\nHost a\n  User ua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("Include a.conf\nHost b\n  User ub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(a)
	if err != nil {
		t.Fatalf("a cycle must not fail the load: %v", err)
	}
	if !hasWarning(f.Warnings, "循环") {
		t.Fatalf("warnings = %v, want a cycle warning", f.Warnings)
	}
	// Both own blocks still resolved; only the loop was cut.
	if h := f.Resolve("a"); h.User != "ua" {
		t.Fatalf("got %+v", h)
	}
}

func TestLoadFileMissingIncludeWarns(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config")
	if err := os.WriteFile(main, []byte("Include nothere/*.conf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(f.Warnings, "未匹配") {
		t.Fatalf("warnings = %v", f.Warnings)
	}
}

func TestLoadDefaultOnEmptyHomeIsNotAnError(t *testing.T) {
	// A user who never wrote a config gets an empty result, not a failure.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	f, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if len(f.Hosts) != 0 {
		t.Fatalf("hosts = %+v, want none", f.Hosts)
	}
}

func TestRejectedCarriesLineNumbers(t *testing.T) {
	f := Parse("Host a\n\n  ProxyJump x\n", "test")
	if len(f.Rejected) != 1 {
		t.Fatalf("rejected = %+v", f.Rejected)
	}
	if f.Rejected[0].Line != 3 {
		t.Fatalf("line = %d, want 3", f.Rejected[0].Line)
	}
	if f.Rejected[0].Reason == "" {
		t.Fatal("a refusal without a reason is not actionable")
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

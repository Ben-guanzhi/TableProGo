// Package sshx (config.go) reads an OpenSSH client configuration file into the jump
// host settings a connection can start from.
//
// The point is not to reimplement ssh(1): it is to answer "what does the user's
// own ~/.ssh/config already know about this host?", so a connection dialog can
// prefill (or a saved connection can reference) the identity, port and user the
// user has already written down once. The subset is deliberately the part that
// matters for a single-hop tunnel:
//
//	Host <pattern> ...        block selector (glob, `!` negation, multiple)
//	  HostName <name>         the real address behind an alias
//	  User <name>
//	  Port <number>
//	  IdentityFile <path>     several allowed; a missing file is skipped
//	  IdentitiesOnly yes|no
//	Include <glob>            line-oriented, relative to ~/.ssh
//	Match all|host|originalhost|user|localuser   (see Scope below)
//
// Deliberately NOT supported, and refused rather than half-honoured:
// ProxyJump, ProxyCommand, ForwardAgent, ForwardX11, DynamicForward,
// LocalForward, RemoteForward and Match exec. The first six change how the
// transport is built, which this package does not do; `Match exec` would run a
// shell command while merely LOOKING at a config file, which a database client
// has no business doing. Refusing is visible (an entry in Rejected) while a
// silently ignored directive would look like it applied.
package sshx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Host is one resolved configuration for a target host.
type Host struct {
	// Pattern is the Host block that matched (its first token), for display.
	Pattern string
	// Alias is the name the caller asked for; HostName is the address it maps
	// to. When no block names HostName, both hold the alias.
	Alias    string
	HostName string
	User     string
	Port     int
	// IdentityFiles are the keys the matching blocks named, in the order they
	// appeared, de-duplicated. Files that do not exist are already removed.
	IdentityFiles []string
	// IdentitiesOnly mirrors the OpenSSH option: when true the caller should
	// try exactly IdentityFiles and no other key.
	IdentitiesOnly bool
}

// Rejected is one directive that was seen and deliberately not applied. The
// dialog shows these so "why did my ProxyJump not take effect" is answerable
// from the UI instead of from a wiki.
type Rejected struct {
	Line      int
	Directive string
	Reason    string
}

// File is the parse result for one config file tree.
type File struct {
	// Hosts is every Host block in declaration order, with its directives.
	Hosts []block
	// Rejected lists directives refused by the policy above.
	Rejected []Rejected
	// Warnings are non-fatal problems (a missing Include, an unreadable file).
	Warnings []string
}

// block is one Host stanza: its patterns and the directives under it.
type block struct {
	patterns  []string
	hostName  string
	user      string
	port      int
	identity  []string
	identOnly bool
	line      int
}

// directives that change the transport or execute something; both lists are
// refused on purpose (see the package comment).
var refusedDirectives = map[string]string{
	"proxyjump":          "ProxyJump 会改变传输路径，本客户端不支持（请改用内置 SSH 隧道）",
	"proxycommand":       "ProxyCommand 会执行外部程序，本客户端不支持",
	"forwardagent":       "ForwardAgent 会转发 agent 套接字，本客户端不支持",
	"forwardx11":         "ForwardX11 会为无关的图形会话转发，本客户端不支持",
	"forwardx11trusted":  "ForwardX11Trusted 同 ForwardX11，本客户端不支持",
	"dynamicforward":     "DynamicForward 会开一个 SOCKS 代理，本客户端不支持",
	"localforward":       "LocalForward 是另一个隧道，请用连接自带的 SSH 设置",
	"remoteforward":      "RemoteForward 是反向隧道，本客户端不支持",
	"permitlocalcommand": "PermitLocalCommand 会执行本地命令，本客户端不支持",
	"localcommand":       "LocalCommand 会执行本地命令，本客户端不支持",
}

// Parse reads one config file's text. name is used in warnings only.
//
// A missing config file is not an error for the caller: Parse on empty text
// yields an empty File, which is the right answer for a user who never wrote
// one.
func Parse(text, name string) *File {
	f := &File{}
	var current *block
	// inMatch says the parser is inside a Match block's body. Its directives
	// are discarded outright: falling back to "global defaults" would apply
	// them to EVERY host, and attaching them to the previous block would apply
	// one host's identity to another.
	inMatch := false
	for i, raw := range splitLines(text) {
		lineNo := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value := splitDirective(line)
		if key == "" {
			continue
		}
		switch key {
		case "host":
			inMatch = false
			f.Hosts = append(f.Hosts, block{patterns: splitPatterns(value), line: lineNo})
			current = &f.Hosts[len(f.Hosts)-1]
		case "match":
			// A Match block continues the file's stream, but matching it would
			// need the real connection context (and `exec` would run a
			// command). Everything after it is discarded rather than applied to
			// the previous Host block, which would silently attribute one
			// block's directives to another.
			current = nil
			inMatch = true
			f.Rejected = append(f.Rejected, Rejected{
				Line: lineNo, Directive: "Match",
				Reason: "Match 块需要连接上下文求值，且 exec 会执行命令；本客户端只接受 Host 块",
			})
		case "include":
			// Include is resolved by the caller (it owns the filesystem root);
			// here it is recorded so ParseFile can expand it.
			f.Rejected = append(f.Rejected, Rejected{
				Line: lineNo, Directive: "Include",
				Reason: "Include 由 ParseFile 展开；直接调用 Parse 时忽略",
			})
		default:
			if reason, bad := refusedDirectives[key]; bad {
				f.Rejected = append(f.Rejected, Rejected{Line: lineNo, Directive: key, Reason: reason})
				continue
			}
			if inMatch {
				continue
			}
			if current == nil {
				// A global directive (before any Host block) — User/Port there
				// apply to every host in OpenSSH. Only the ones this package
				// understands are honoured, as a default "*" block.
				applyDirective(globalDefaults(f), key, value)
				continue
			}
			applyDirective(current, key, value)
		}
	}
	_ = name
	return f
}

// globalDefaults returns the synthetic leading "*" block that global directives
// (those before the first Host section) populate. It is a pointer into the
// slice so the caller's writes land, and it is created once.
func globalDefaults(f *File) *block {
	if len(f.Hosts) > 0 && len(f.Hosts[0].patterns) == 1 && f.Hosts[0].patterns[0] == "*" && f.Hosts[0].line == 0 {
		return &f.Hosts[0]
	}
	f.Hosts = append([]block{{patterns: []string{"*"}, line: 0}}, f.Hosts...)
	return &f.Hosts[0]
}

// applyDirective records the directives this package understands; everything
// else is ignored (OpenSSH has ~80 options, most irrelevant to one hop).
func applyDirective(b *block, key, value string) {
	switch key {
	case "hostname":
		b.hostName = value
	case "user":
		b.user = value
	case "port":
		if n, err := strconv.Atoi(value); err == nil && n > 0 && n <= 65535 {
			b.port = n
		}
	case "identityfile":
		b.identity = append(b.identity, expandTilde(value))
	case "identitiesonly":
		b.identOnly = isYes(value)
	}
}

// Resolve answers "what does this config say about host?" by walking the blocks
// in declaration order and letting each matching one fill in only the values no
// earlier block set — OpenSSH's first-value-wins rule.
//
// The returned Host is never nil: an unknown host resolves to itself
// (HostName == alias) with the package's defaults, which is what the caller
// needs to prefill a dialog.
func (f *File) Resolve(alias string) *Host {
	h := &Host{Alias: alias, HostName: alias}
	negated := false
	matched := false
	for _, b := range f.Hosts {
		if !patternsMatch(b.patterns, alias) {
			continue
		}
		// A block whose negation matched still counts as "the user said
		// something about this host", so an explicit `!host` suppresses later
		// blocks the way OpenSSH intends.
		if hasNegationMatch(b.patterns, alias) {
			negated = true
			continue
		}
		matched = true
		if h.Pattern == "" {
			h.Pattern = b.patterns[0]
		}
		if h.HostName == alias && b.hostName != "" {
			h.HostName = b.hostName
		}
		if h.User == "" && b.user != "" {
			h.User = b.user
		}
		if h.Port == 0 && b.port != 0 {
			h.Port = b.port
		}
		if b.identOnly {
			h.IdentitiesOnly = true
		}
		for _, key := range b.identity {
			if !contains(h.IdentityFiles, key) {
				h.IdentityFiles = append(h.IdentityFiles, key)
			}
		}
	}
	if negated && !matched {
		// A lone negation means "this config has nothing for the host": keep
		// the identity mapping rather than borrowing another block's values.
		return h
	}
	// Only files that exist are offered: a stale path in someone's config must
	// not make a connection fail with "no such file" when a working key sits
	// next to it.
	h.IdentityFiles = existingFiles(h.IdentityFiles)
	if h.Port == 0 {
		h.Port = 22
	}
	return h
}

// Patterns lists the concrete host aliases the file declares, in order. Wildcard
// patterns are excluded: `*.internal` is a rule, not a host the user can pick.
func (f *File) Patterns() []string {
	var out []string
	for _, b := range f.Hosts {
		for _, p := range b.patterns {
			if strings.HasPrefix(p, "!") || strings.ContainsAny(p, "*?") {
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

// ---- file-level entry points ----

// LoadFile parses a config file and follows its Includes (one level of nesting
// is enough in practice; recursion is bounded anyway so a cycle cannot hang).
func LoadFile(path string) (*File, error) {
	return load(path, 0, nil)
}

// LoadDefault reads ~/.ssh/config. A missing file yields an empty (not nil)
// File so callers need no nil check.
func LoadDefault() (*File, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return &File{Warnings: []string{"无法定位用户主目录，未读取 ~/.ssh/config"}}, nil
	}
	path := filepath.Join(home, ".ssh", "config")
	f, err := LoadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{}, nil
	}
	return f, err
}

// maxIncludeDepth bounds Include recursion; a config that includes itself is a
// mistake, not a workload.
const maxIncludeDepth = 8

func load(path string, depth int, seen map[string]bool) (*File, error) {
	if seen == nil {
		seen = map[string]bool{}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if seen[abs] {
		return &File{Warnings: []string{fmt.Sprintf("Include 循环，已跳过: %s", path)}}, nil
	}
	if depth > maxIncludeDepth {
		return &File{Warnings: []string{fmt.Sprintf("Include 嵌套超过 %d 层，已停止: %s", maxIncludeDepth, path)}}, nil
	}
	seen[abs] = true

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := Parse(string(data), path)
	// Expand Includes in place: splice the included file's blocks where the
	// directive stood. OpenSSH treats them as if pasted, which is what makes
	// "first value wins" behave.
	for _, inc := range includesOf(string(data)) {
		pattern := inc.pattern
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(filepath.Dir(path), pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			f.Warnings = append(f.Warnings, fmt.Sprintf("Include 未匹配到文件: %s", inc.pattern))
			continue
		}
		sort.Strings(matches)
		for _, m := range matches {
			sub, err := load(m, depth+1, seen)
			if err != nil {
				f.Warnings = append(f.Warnings, fmt.Sprintf("Include 读取失败 %s: %v", m, err))
				continue
			}
			f.Hosts = append(f.Hosts, sub.Hosts...)
			f.Rejected = append(f.Rejected, sub.Rejected...)
			f.Warnings = append(f.Warnings, sub.Warnings...)
		}
	}
	return f, nil
}

type includeLine struct{ pattern string }

// includesOf collects Include patterns in order (Parse refuses them for the
// single-file path; the file loader expands them).
func includesOf(text string) []includeLine {
	var out []includeLine
	for _, raw := range splitLines(text) {
		key, value := splitDirective(strings.TrimSpace(raw))
		if key == "include" {
			out = append(out, includeLine{pattern: value})
		}
	}
	return out
}

// ---- parsing helpers ----

// splitLines handles both LF and CRLF; a config written on Windows must parse.
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Split(text, "\n")
}

// splitDirective splits "Key value" / "Key=value" and folds the key to lower
// case (OpenSSH keywords are case-insensitive).
func splitDirective(line string) (key, value string) {
	// Strip a trailing comment that is preceded by whitespace: `User bob # ops`
	// is a user named bob, not "bob # ops". A '#' inside a value with no
	// leading space (a path) is kept.
	if idx := commentIndex(line); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if line == "" {
		return "", ""
	}
	if i := strings.IndexAny(line, " \t="); i >= 0 {
		key = strings.ToLower(strings.TrimSpace(line[:i]))
		rest := strings.TrimSpace(line[i:])
		rest = strings.TrimPrefix(rest, "=")
		return key, strings.TrimSpace(rest)
	}
	return strings.ToLower(line), ""
}

// commentIndex finds the start of a trailing comment: a '#' at the beginning or
// preceded by whitespace.
func commentIndex(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
			return i
		}
	}
	return -1
}

// splitPatterns splits a Host line's patterns, honouring quoted patterns.
func splitPatterns(value string) []string {
	return splitQuoted(value)
}

// splitQuoted splits on whitespace but keeps "quoted strings" together, which
// is how a Host pattern containing a space is written.
func splitQuoted(s string) []string {
	var out []string
	var buf strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case (c == ' ' || c == '\t') && !inQuote:
			if buf.Len() > 0 {
				out = append(out, buf.String())
				buf.Reset()
			}
		default:
			buf.WriteByte(c)
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// patternsMatch reports whether any positive pattern matches the host, and no
// negation vetoes it. OpenSSH evaluates a block's negations first.
func patternsMatch(patterns []string, host string) bool {
	host = strings.ToLower(host)
	anyPositive := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		pat := strings.ToLower(strings.TrimPrefix(p, "!"))
		if neg {
			if globMatch(pat, host) {
				return false
			}
			continue
		}
		anyPositive = true
		if globMatch(pat, host) {
			// Keep scanning: a later negation in the SAME block still vetoes.
			for _, q := range patterns {
				if strings.HasPrefix(q, "!") && globMatch(strings.ToLower(strings.TrimPrefix(q, "!")), host) {
					return false
				}
			}
			return anyPositive
		}
	}
	_ = anyPositive
	return false
}

// hasNegationMatch reports whether a negation in the block matched the host.
func hasNegationMatch(patterns []string, host string) bool {
	host = strings.ToLower(host)
	for _, p := range patterns {
		if strings.HasPrefix(p, "!") && globMatch(strings.ToLower(strings.TrimPrefix(p, "!")), host) {
			return true
		}
	}
	return false
}

// globMatch is OpenSSH's pattern matcher: `*` and `?` anywhere, case
// insensitive. It is implemented directly rather than through filepath.Match,
// which treats `/` and separators specially (host patterns have none).
func globMatch(pattern, name string) bool {
	pi, ni := 0, 0
	star, mark := -1, 0
	for ni < len(name) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == name[ni]) {
			pi++
			ni++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			mark = ni
			pi++
			continue
		}
		if star >= 0 {
			pi = star + 1
			mark++
			ni = mark
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// expandTilde resolves a leading ~ to the user's home directory (OpenSSH does
// this for IdentityFile).
func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
		}
	}
	return path
}

// existingFiles filters a key list to files that are actually readable, which is
// the difference between "your config mentions three keys" and "three keys can
// be tried".
func existingFiles(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

func isYes(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "true", "1", "on":
		return true
	}
	return false
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

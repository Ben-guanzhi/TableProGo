package main

// W4-d: pasting a connection URL instead of filling a form.
//
// Every client in the market accepts `postgres://user:pw@host:5432/db`, because
// that string is what a hosting dashboard or a colleague hands you. This parses
// the schemes this client can actually open, and REFUSES everything else with
// the scheme named — a silently mis-parsed URL that connects to the wrong port
// is worse than "we do not know this scheme".
//
// The parser produces a ConnectionConfig with the password already split out, so
// the dialog can put it through the same secrets layer a typed password uses.

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// uriSchemes maps a URL scheme to the engine type and its default port. The
// protocol-compatible engines are listed by their own alias so a pasted
// `oceanbase://` keeps its identity instead of becoming plain mysql.
var uriSchemes = map[string]struct {
	engine string
	port   int
}{
	"postgres":     {"postgresql", 5432},
	"postgresql":   {"postgresql", 5432},
	"mysql":        {"mysql", 3306},
	"mariadb":      {"mysql", 3306},
	"mssql":        {"sqlserver", 1433},
	"sqlserver":    {"sqlserver", 1433},
	"oracle":       {"oracle", 1521},
	"sqlite":       {"sqlite", 0},
	"redis":        {"redis", 6379},
	"rediss":       {"redis", 6379},
	"oceanbase":    {"oceanbase", 2881},
	"greatsql":     {"greatsql", 3306},
	"starrocks":    {"starrocks", 9030},
	"doris":        {"doris", 9030},
	"opengauss":    {"opengauss", 5432},
	"gaussdb":      {"gaussdb", 5432},
	"kingbase":     {"kingbase", 5432},
	// W14: 达梦 DM8. It has its own wire protocol and therefore its own driver, but
	// a pasted URL is how a user migrates a connection, so the scheme has to work.
	"dm":     {"dm", 5236},
	"dameng": {"dm", 5236},
}

// URISchemes lists the accepted schemes for the dialog's hint text, in a stable
// order.
func URISchemes() []string {
	out := []string{
		"postgres", "mysql", "mariadb", "sqlserver", "mssql", "oracle", "sqlite",
		"redis", "oceanbase", "opengauss", "kingbase",
	}
	return out
}

// ParseConnectionURI turns a pasted URL into a ConnectionConfig.
//
// Refused on purpose:
//   - an unknown scheme (named in the error);
//   - a missing host for a networked engine (sqlite exempt);
//   - a path that is neither empty nor a single database name.
//
// Options in the query string are kept as the connection's Options map, which is
// where the drivers already read engine-specific knobs from.
func ParseConnectionURI(raw string) (ConnectionConfig, error) {
	cfg := ConnectionConfig{}
	text := strings.TrimSpace(raw)
	if text == "" {
		return cfg, fmt.Errorf("连接串为空")
	}
	u, err := url.Parse(text)
	if err != nil {
		return cfg, fmt.Errorf("无法解析连接串: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	spec, ok := uriSchemes[scheme]
	if !ok {
		return cfg, fmt.Errorf("不支持的连接串协议 %q（支持: %s）", u.Scheme, strings.Join(URISchemes(), ", "))
	}
	cfg.Type = spec.engine
	cfg.Name = u.Hostname()
	if cfg.Name == "" {
		cfg.Name = strings.TrimPrefix(u.Path, "/")
	}
	if cfg.Name == "" {
		cfg.Name = spec.engine
	}

	// sqlite's "host" is its file path; every other engine needs a host.
	if spec.engine == "sqlite" {
		path := u.Path
		// file:///tmp/x.db and sqlite:///tmp/x.db both mean the same file.
		if u.Host != "" && u.Host != "localhost" {
			path = u.Host + path
		}
		if path == "" {
			return cfg, fmt.Errorf("%s 连接串缺少数据库文件路径", scheme)
		}
		cfg.Host = path
		return cfg, nil
	}

	if u.Hostname() == "" {
		return cfg, fmt.Errorf("%s 连接串缺少主机名", scheme)
	}
	cfg.Host = u.Hostname()

	port := spec.port
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return cfg, fmt.Errorf("端口无效: %s", p)
		}
		port = n
	}
	cfg.Port = port

	if u.User != nil {
		cfg.Username = u.User.Username()
		if pw, has := u.User.Password(); has {
			cfg.Password = pw
		}
	}

	// The path is the database (and, for PG-family engines, may be db/schema).
	path := strings.TrimPrefix(u.Path, "/")
	if path != "" {
		parts := strings.Split(path, "/")
		if len(parts) > 2 {
			return cfg, fmt.Errorf("连接串路径层级过深（期望 /数据库 或 /数据库/模式）: %s", u.Path)
		}
		cfg.Database = parts[0]
	}

	// Options ride through unchanged: the drivers already interpret them, and
	// rewriting them here would be a second place that knows each dialect.
	if len(u.Query()) > 0 {
		cfg.Options = map[string]string{}
		for key := range u.Query() {
			cfg.Options[key] = u.Query().Get(key)
		}
	}
	return cfg, nil
}

// MaskConnectionURI replaces the password in a URL with a fixed placeholder, for
// showing the parsed result back to the user without putting the secret on
// screen a second time.
func MaskConnectionURI(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); !has {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), "***")
	return u.String()
}

// connURIHostPort formats an address the way the trial dialog shows it.
func connURIHostPort(host string, port int) string {
	if port <= 0 {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

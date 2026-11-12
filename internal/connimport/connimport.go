// Package connimport reads other database clients' connection definitions.
//
// The point is migration, not compatibility: a user who has forty connections in
// DBeaver will not retype them, and the fastest way to lose that user is to make
// them. So this package reads what the other tools actually write, converts the
// parts that are portable, and says plainly what it could not carry.
//
// Two rules decide the shape:
//
//  1. CREDENTIALS ARE NEVER READ. DBeaver stores passwords encrypted per
//     workspace; Navicat's .ncx is encrypted wholesale. Attempting to decrypt
//     either would mean shipping an attack on a competitor's credential store,
//     and a migrated connection with a blank password costs the user one prompt
//     while a wrong guess costs them trust. Every imported row therefore carries
//     no password, and the import result says so.
//
//  2. A MALFORMED ENTRY IS SKIPPED AND REPORTED. One broken connection in a
//     forty-connection file must not fail the import; it must appear in the
//     report with the reason, like an import wizard's per-row error column.
//
// What each source can actually give:
//
//	DBeaver   ~/.dbeaver/data-sources.json (or a workspace copy) — provider,
//	          host, port, database, user, SSL flag, and a JDBC URL. This is
//	          plain JSON and is read in full.
//	Navicat   .ncx is encrypted (refused, with the readable path named). The
//	          readable form is a connection URL or a hand-written list, which
//	          the generic reader accepts.
//	Generic   a list of URLs, or a simple JSON/CSV table of host/port/user/db,
//	          which is what every other client's "export" ends up being.
package connimport

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Row is one imported connection, in a neutral shape.
//
// It deliberately has no password field: see the package comment. A caller that
// wants to prompt for credentials has everything else it needs here.
type Row struct {
	// Name is the display name the source used; "" means the caller should
	// derive one from the host/database.
	Name string
	// Engine is the engine type spelling this app uses ("mysql", "postgresql", …).
	Engine string
	Host   string
	Port   int
	// Database may be a database or a schema depending on the engine; the
	// caller maps it the way its own connection dialog does.
	Database string
	User     string
	// SSL is the source's SSL flag, already folded onto this app's SSLMode.
	SSL      string
	// Source names where the row came from, for the report.
	Source string
	// URL is the raw connection URL when the row came from one (generic import).
	URL string
}

// Report is the outcome, including every row that was skipped.
type Report struct {
	// Rows are the connections that were understood.
	Rows []Row
	// Skipped are the entries that were not, with why. An import that silently
	// dropped half the file would look like a success.
	Skipped []Skip
	// Notes are facts about the whole file: "credentials are not imported",
	// "N connections had no host", …
	Notes []string
}

// Skip is one entry that could not be imported.
type Skip struct {
	// Where identifies the entry as the source spelled it (a connection id, a
	// URL, a CSV row number).
	Where  string
	Reason string
}

// ---- DBeaver ----

// dbeaverFile mirrors data-sources.json. Only the fields this package carries
// are declared; DBeaver's file holds far more (driver properties, folders,
// connection history) and none of it is portable here.
type dbeaverFile struct {
	Connections map[string]dbeaverConnection `json:"connections"`
	// Folders is DBeaver's grouping; imported as a note only, because this
	// app's groups are a flat label.
	Folders map[string]dbeaverFolder `json:"folders"`
}

type dbeaverFolder struct {
	Name string `json:"name"`
}

type dbeaverConnection struct {
	Provider string `json:"provider"`
	Driver   string `json:"driver"`
	Name     string `json:"name"`
	// SavePassword says a password exists in DBeaver's store; it is read only to
	// explain the omission, never to obtain the secret.
	SavePassword  bool                `json:"save-password"`
	Configuration dbeaverConfig       `json:"configuration"`
}

type dbeaverConfig struct {
	Host     string `json:"host"`
	Port     any    `json:"port"` // string in most files, number in some
	Database string `json:"database"`
	// Schema is PostgreSQL's search_path in some exports.
	Schema string `json:"schema"`
	User   string `json:"user"`
	// URL is the JDBC URL, the most reliable field: host/port/database can be
	// derived from it even when the individual fields are empty.
	URL     string `json:"url"`
	SSL     any    `json:"ssl"` // bool, most files
	SSLMode string `json:"sslmode"`
}

// ParseDBeaver reads a data-sources.json body.
func ParseDBeaver(data []byte) (Report, error) {
	data = trimBOM(data)
	var doc dbeaverFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return Report{}, fmt.Errorf("解析 DBeaver 配置: %w", err)
	}
	if len(doc.Connections) == 0 {
		return Report{}, fmt.Errorf("该文件里没有任何连接（是不是选错了文件？DBeaver 的连接在 data-sources.json）")
	}
	var rep Report
	// Stable order: a report whose rows shuffle between runs is hard to compare.
	keys := make([]string, 0, len(doc.Connections))
	for id := range doc.Connections {
		keys = append(keys, id)
	}
	sortStrings(keys)
	credentialsSeen := 0
	for _, id := range keys {
		c := doc.Connections[id]
		if c.SavePassword {
			credentialsSeen++
		}
		row, skip := dbeaverRow(id, c)
		if skip != nil {
			rep.Skipped = append(rep.Skipped, *skip)
			continue
		}
		rep.Rows = append(rep.Rows, row)
	}
	if credentialsSeen > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf(
			"%d 个连接在 DBeaver 里保存了口令，口令不会被读取：导入后首次连接时填写一次（这是有意的边界，不去解密别人的凭据库）",
			credentialsSeen))
	}
	if len(doc.Folders) > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf(
			"DBeaver 的分组（%d 个文件夹）不会导入：本程序的分组是一个扁平标签", len(doc.Folders)))
	}
	return rep, nil
}

// dbeaverRow converts one DBeaver connection, or explains why it cannot be.
func dbeaverRow(id string, c dbeaverConnection) (Row, *Skip) {
	engine, ok := dbeaverEngine(c.Provider, c.Driver)
	if !ok {
		return Row{}, &Skip{
			Where:  pick(c.Name, id),
			Reason: fmt.Sprintf("不认识引擎 provider=%q driver=%q（本程序支持 MySQL/MariaDB、PostgreSQL、SQLite、SQL Server、Oracle、Redis）", c.Provider, c.Driver),
		}
	}
	cfg := c.Configuration
	host := strings.TrimSpace(cfg.Host)
	port := toInt(cfg.Port, 0)
	database := strings.TrimSpace(cfg.Database)
	user := strings.TrimSpace(cfg.User)

	// A JDBC URL is the most complete field, and DBeaver writes it even when the
	// individual boxes are empty: parse it as the base and let the explicit
	// fields win, in that order of reliability.
	var urlSSL string
	if raw := strings.TrimSpace(cfg.URL); raw != "" {
		if u, err := parseJDBC(raw); err == nil {
			if host == "" {
				host = u.host
			}
			if port == 0 {
				port = u.port
			}
			if database == "" {
				database = u.database
			}
			if user == "" {
				user = u.user
			}
			urlSSL = u.ssl
		}
	}
	if host == "" && engine != "sqlite" {
		return Row{}, &Skip{
			Where:  pick(c.Name, id),
			Reason: "没有主机名（host 与 url 都是空的）",
		}
	}
	if engine == "sqlite" {
		// SQLite's "host" is its file path, which DBeaver puts in the URL.
		path := strings.TrimSpace(cfg.Database)
		if path == "" {
			path = host
		}
		if path == "" {
			return Row{}, &Skip{Where: pick(c.Name, id), Reason: "SQLite 连接没有数据库文件路径"}
		}
		return Row{
			Name: pick(c.Name, id), Engine: "sqlite", Host: path, Source: "dbeaver",
		}, nil
	}
	ssl := dbeaverSSL(cfg)
	if ssl == "" {
		ssl = urlSSL
	}
	return Row{
		Name: pick(c.Name, id), Engine: engine,
		Host: host, Port: port, Database: database, User: user,
		SSL:    ssl,
		Source: "dbeaver",
	}, nil
}

// dbeaverEngine maps DBeaver's provider/driver onto this app's engine spelling.
//
// DBeaver's provider is already close ("postgresql", "mysql", "sqlserver",
// "oracle", "sqlite", "redis"), but its *driver* id carries the version
// ("postgres-jdbc-1708"), so provider is preferred and driver is the fallback.
func dbeaverEngine(provider, driver string) (string, bool) {
	probe := strings.ToLower(strings.TrimSpace(provider))
	if probe == "" {
		probe = strings.ToLower(strings.TrimSpace(driver))
	}
	switch {
	case strings.Contains(probe, "postgres"), probe == "pg":
		return "postgresql", true
	case strings.Contains(probe, "mariadb"):
		return "mysql", true
	case strings.Contains(probe, "mysql"):
		return "mysql", true
	case strings.Contains(probe, "sqlserver"), strings.Contains(probe, "mssql"), strings.Contains(probe, "sql-server"):
		return "sqlserver", true
	case strings.Contains(probe, "oracle"):
		return "oracle", true
	case strings.Contains(probe, "sqlite"):
		return "sqlite", true
	case strings.Contains(probe, "redis"):
		return "redis", true
	}
	return "", false
}

// dbeaverSSL folds DBeaver's SSL settings onto this app's SSLMode. DBeaver's
// `ssl` is a boolean; `sslmode` is PostgreSQL's spelling and passes through when
// present.
func dbeaverSSL(cfg dbeaverConfig) string {
	if mode := strings.TrimSpace(cfg.SSLMode); mode != "" {
		return mode
	}
	if b, ok := cfg.SSL.(bool); ok {
		if b {
			return "require"
		}
		return "false"
	}
	return ""
}

// ---- JDBC URL ----

type jdbcURL struct {
	engine   string
	host     string
	port     int
	database string
	user     string
	// ssl is the URL's own sslmode when it names one: it is the more specific
	// spelling the user chose, so it wins over a boolean box.
	ssl string
}

// parseJDBC reads `jdbc:<engine>://host:port/db?…`. It normalizes the engine to
// this app's spelling but does NOT decide the connection's engine (the caller's
// provider mapping does): a URL is evidence, not the authority.
func parseJDBC(raw string) (jdbcURL, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(trimmed), "jdbc:") {
		return jdbcURL{}, fmt.Errorf("不是 JDBC URL")
	}
	rest := trimmed[len("jdbc:"):]
	// `jdbc:sqlserver://host;databaseName=db` uses semicolons, not a path. Split
	// the scheme first, then treat the remainder as a URL with ";" separators
	// folded into query parameters.
	colon := strings.Index(rest, "://")
	if colon < 0 {
		return jdbcURL{}, fmt.Errorf("JDBC URL 缺少 ://")
	}
	scheme := strings.ToLower(rest[:colon])
	tail := rest[colon+3:]
	// Semicolon style (SQL Server): host:port;k=v;…
	query := url.Values{}
	if i := strings.Index(tail, ";"); i >= 0 {
		for _, part := range strings.Split(tail[i+1:], ";") {
			if k, v, ok := strings.Cut(part, "="); ok {
				query.Set(strings.ToLower(strings.TrimSpace(k)), v)
			}
		}
		tail = tail[:i]
	}
	// Path style: host:port/db?k=v
	if q := strings.Index(tail, "?"); q >= 0 {
		if parsed, err := url.ParseQuery(tail[q+1:]); err == nil {
			for k, vs := range parsed {
				if len(vs) > 0 {
					query.Set(k, vs[0])
				}
			}
		}
		tail = tail[:q]
	}
	hostPort := tail
	dbName := ""
	if slash := strings.Index(tail, "/"); slash >= 0 {
		hostPort = tail[:slash]
		dbName = tail[slash+1:]
	}
	out := jdbcURL{engine: scheme, database: dbName, ssl: query.Get("sslmode")}
	// SQL Server's JDBC properties spell the database `databaseName`; the other
	// engines use the path. Accept both so a URL-only connection still lands in
	// the right database. Keys were folded to lower case when collected, so the
	// lookups must be too.
	if out.database == "" {
		out.database = query.Get("databasename")
	}
	if out.database == "" {
		out.database = query.Get("database")
	}
	// SQL Server spells SSL `encrypt`; MySQL uses `useSSL`.
	if out.ssl == "" {
		if enc := strings.ToLower(query.Get("encrypt")); enc == "true" || enc == "yes" {
			out.ssl = "require"
		}
	}
	if out.ssl == "" {
		if use := strings.ToLower(query.Get("usessl")); use == "true" || use == "yes" {
			out.ssl = "require"
		}
	}
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		out.host = hostPort[:i]
		if n, err := strconv.Atoi(hostPort[i+1:]); err == nil {
			out.port = n
		}
	} else {
		out.host = hostPort
	}
	out.user = query.Get("user")
	return out, nil
}

// ---- generic lists ----

// ParseURLList reads one connection URL per line. Blank lines and lines starting
// with '#' are skipped, so a hand-maintained list works.
//
// This is the readable path for Navicat: its .ncx export is encrypted, but the
// connection URL it shows in the properties dialog is exactly this shape.
func ParseURLList(text string) Report {
	var rep Report
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		row, err := ParseURLRow(trimmed)
		if err != nil {
			rep.Skipped = append(rep.Skipped, Skip{
				Where:  fmt.Sprintf("第 %d 行: %s", i+1, shorten(trimmed)),
				Reason: err.Error(),
			})
			continue
		}
		row.Source = "url"
		rep.Rows = append(rep.Rows, row)
	}
	if len(rep.Rows) == 0 && len(rep.Skipped) == 0 {
		rep.Notes = append(rep.Notes, "没有读到任何连接行（每行一个 URL，空行与 # 开头的行会被跳过）")
	}
	return rep
}

// ParseURLRow converts one connection URL into a Row. The scheme carries the
// engine, the way every client's "copy connection URL" does.
func ParseURLRow(raw string) (Row, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Row{}, fmt.Errorf("空行")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return Row{}, fmt.Errorf("无法解析 URL: %w", err)
	}
	engine, ok := urlSchemeEngine(strings.ToLower(u.Scheme))
	if !ok {
		return Row{}, fmt.Errorf("不支持的协议 %q（postgres/mysql/mariadb/sqlserver/mssql/oracle/redis/sqlite）", u.Scheme)
	}
	row := Row{Engine: engine, URL: trimmed}
	if u.User != nil {
		row.User = u.User.Username()
		// The password, if present, is deliberately dropped: see the package
		// comment. A URL in a chat message often carries one, and importing it
		// would spread a secret into a config file.
	}
	if engine == "sqlite" {
		row.Host = strings.TrimPrefix(u.Path, "/")
		if row.Host == "" {
			return Row{}, fmt.Errorf("SQLite URL 缺少文件路径")
		}
		return row, nil
	}
	row.Host = u.Hostname()
	if row.Host == "" {
		return Row{}, fmt.Errorf("URL 缺少主机名")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			row.Port = n
		}
	}
	row.Database = strings.TrimPrefix(u.Path, "/")
	if mode := u.Query().Get("sslmode"); mode != "" {
		row.SSL = mode
	}
	if row.SSL == "" && strings.EqualFold(u.Scheme, "rediss") {
		row.SSL = "require"
	}
	return row, nil
}

// urlSchemeEngine maps a URL scheme onto this app's engine spelling.
func urlSchemeEngine(scheme string) (string, bool) {
	switch scheme {
	case "postgres", "postgresql", "pg":
		return "postgresql", true
	case "mysql", "mariadb":
		return "mysql", true
	case "sqlserver", "mssql":
		return "sqlserver", true
	case "oracle", "oracledb":
		return "oracle", true
	case "redis", "rediss":
		return "redis", true
	case "sqlite", "file":
		return "sqlite", true
	}
	return "", false
}

// ParseCSV reads a table of connections: a header row naming the columns, then
// one row per connection. Accepted headers (case-insensitive): name/host/port/
// user/username/database/engine/ssl. Anything else is ignored.
//
// This is what a user produces when their client exports "connection list to
// spreadsheet", and it costs almost nothing to support.
func ParseCSV(text string) (Report, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return Report{}, fmt.Errorf("解析 CSV: %w", err)
	}
	if len(records) < 2 {
		return Report{}, fmt.Errorf("CSV 至少需要表头与一行数据")
	}
	col := map[string]int{}
	for i, name := range records[0] {
		col[strings.ToLower(strings.TrimSpace(name))] = i
	}
	need := func(row []string, key string) string {
		if i, ok := col[key]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	var rep Report
	for n, row := range records[1:] {
		if len(row) == 0 || (len(row) == 1 && strings.TrimSpace(row[0]) == "") {
			continue
		}
		engineRaw := need(row, "engine")
		if engineRaw == "" {
			engineRaw = need(row, "type")
		}
		engine, ok := dbeaverEngine(engineRaw, "")
		if !ok {
			rep.Skipped = append(rep.Skipped, Skip{
				Where:  fmt.Sprintf("第 %d 行", n+2),
				Reason: fmt.Sprintf("不认识引擎 %q", engineRaw),
			})
			continue
		}
		host := need(row, "host")
		if host == "" && engine != "sqlite" {
			rep.Skipped = append(rep.Skipped, Skip{
				Where:  fmt.Sprintf("第 %d 行", n+2),
				Reason: "缺少 host",
			})
			continue
		}
		port := 0
		if p := need(row, "port"); p != "" {
			port, _ = strconv.Atoi(p)
		}
		rep.Rows = append(rep.Rows, Row{
			Name: pick(need(row, "name"), host), Engine: engine,
			Host: host, Port: port,
			Database: need(row, "database"),
			User:     pick(need(row, "user"), need(row, "username")),
			SSL:      need(row, "ssl"),
			Source:   "csv",
		})
	}
	return rep, nil
}

// ---- helpers ----

func pick(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func toInt(v any, fallback int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	}
	return fallback
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

func shorten(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:57] + "…"
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

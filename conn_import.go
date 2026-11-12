package main

// W12: importing connections from another client.
//
// The parsing lives in internal/connimport (pure, tested against fixtures shaped
// like the real files). This file is the app-side half: it decides which reader a
// file needs, refuses the encrypted one with an explanation instead of pretending,
// and applies the imported rows to the saved-connection list without overwriting
// anything the user already has.

import (
	"fmt"
	"os"
	"strings"

	"tablepro-go/internal/connimport"
	"tablepro-go/internal/pluginreg"
)

// ConnImportRequest names the file to read and whether to apply it.
type ConnImportRequest struct {
	// Path is the file: a DBeaver data-sources.json, a .ncx (refused with the
	// reason), a URL list, or a CSV.
	Path string `json:"path"`
	// Format overrides the reader selected by the file's shape. Empty means
	// detect; the values are "dbeaver", "url", "csv".
	Format string `json:"format"`
	// Apply saves the rows. Without it the call only reports what WOULD import —
	// the same preview-then-apply discipline the transfer and sync paths use.
	Apply bool `json:"apply"`
	// Group labels every imported connection's sidebar section.
	Group string `json:"group"`
}

// ConnImportRow is one importable connection, as the dialog shows it.
type ConnImportRow struct {
	Name     string `json:"name"`
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	SSL      string `json:"ssl,omitempty"`
	Source   string `json:"source"`
}

// ConnImportResult is the report.
type ConnImportResult struct {
	// Rows are what was read (and, with Apply, saved).
	Rows []ConnImportRow `json:"rows"`
	// Skipped are the entries that could not be imported, with why.
	Skipped []ConnImportSkip `json:"skipped"`
	// Notes explain the whole file: credentials, grouping, format guesses.
	Notes []string `json:"notes"`
	// Imported counts the rows actually saved (0 without Apply).
	Imported int `json:"imported"`
	// Renamed lists rows saved under a different name because one was taken.
	Renamed []string `json:"renamed"`
}

// ConnImportSkip is one unimportable entry.
type ConnImportSkip struct {
	Where  string `json:"where"`
	Reason string `json:"reason"`
}

// ImportConnections reads a file and (with Apply) saves what it understood.
func (a *App) ImportConnections(req ConnImportRequest) (ConnImportResult, error) {
	path := strings.TrimSpace(req.Path)
	if path == "" {
		return ConnImportResult{}, fmt.Errorf("请选择要导入的文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ConnImportResult{}, fmt.Errorf("读取文件: %w", err)
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = detectImportFormat(path, data)
	}

	var report connimport.Report
	switch format {
	case "dbeaver":
		report, err = connimport.ParseDBeaver(data)
		if err != nil {
			return ConnImportResult{}, err
		}
	case "url":
		report = connimport.ParseURLList(string(data))
	case "csv":
		report, err = connimport.ParseCSV(string(data))
		if err != nil {
			return ConnImportResult{}, err
		}
	case "navicat":
		// Navicat's .ncx is encrypted. Refusing with the readable path named is
		// the honest answer: decrypting it would mean attacking their credential
		// store, and a half-parsed blob would look like data corruption.
		return ConnImportResult{}, fmt.Errorf(
			"Navicat 的 .ncx 导出是加密的，本程序不尝试解密（那等于破解别人的凭据库）。" +
				"可改用：在 Navicat 连接属性里复制连接 URL，一行一个粘进 .txt 按 URL 导入；或导出 CSV")
	default:
		return ConnImportResult{}, fmt.Errorf("无法识别文件格式（支持 DBeaver 的 data-sources.json、一行一个 URL 的文本、CSV）")
	}

	result := ConnImportResult{
		Skipped: make([]ConnImportSkip, 0, len(report.Skipped)),
		Notes:   append([]string(nil), report.Notes...),
	}
	// Credentials never arrive, so say it once at the top: a user who expected
	// passwords to come along should learn that here, not at first connect.
	result.Notes = append(result.Notes, "导入的连接不带口令：首次连接时填写一次")
	for _, row := range report.Rows {
		result.Rows = append(result.Rows, ConnImportRow{
			Name: row.Name, Engine: row.Engine, Host: row.Host, Port: row.Port,
			Database: row.Database, User: row.User, SSL: row.SSL, Source: row.Source,
		})
	}
	for _, s := range report.Skipped {
		result.Skipped = append(result.Skipped, ConnImportSkip{Where: s.Where, Reason: s.Reason})
	}

	if !req.Apply {
		return result, nil
	}

	// Apply: append, never overwrite. A user importing into a profile that
	// already has "shop-prod" must keep both, with the newcomer renamed, or the
	// import would silently replace a working connection.
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()
	existing := map[string]bool{}
	for _, c := range a.savedConnections {
		existing[strings.ToLower(strings.TrimSpace(c.Name))] = true
	}
	for i, row := range result.Rows {
		name := strings.TrimSpace(row.Name)
		if name == "" {
			name = strings.TrimSpace(row.Host)
		}
		if name == "" {
			name = row.Engine
		}
		unique := name
		for n := 2; existing[strings.ToLower(unique)]; n++ {
			unique = fmt.Sprintf("%s (%d)", name, n)
		}
		if unique != name {
			result.Renamed = append(result.Renamed, fmt.Sprintf("%s → %s", name, unique))
		}
		existing[strings.ToLower(unique)] = true

		id, idErr := newSecretID()
		if idErr != nil {
			return ConnImportResult{}, fmt.Errorf("生成连接 ID: %w", idErr)
		}
		cfg := ConnectionConfig{
			ID:       id,
			Name:     unique,
			Type:     row.Engine,
			Host:     row.Host,
			Port:     row.Port,
			Username: row.User,
			// No password: see the package comment in internal/connimport.
			Password: "",
			Database: row.Database,
			SSLMode:  importerSSLMode(row.SSL),
			Group:    strings.TrimSpace(req.Group),
		}
		if cfg.Type == "sqlite" {
			// A SQLite "host" is the file path; the dialog stores it in Database.
			cfg.Database = row.Host
			cfg.Host = ""
		}
		if cfg.Port == 0 {
			cfg.Port = defaultPortForEngine(cfg.Type)
		}
		result.Rows[i].Name = unique
		a.savedConnections = append(a.savedConnections, cfg)
		result.Imported++
	}
	if result.Imported > 0 {
		a.saveToFile()
	}
	return result, nil
}

// detectImportFormat guesses the reader from the file's shape rather than its
// extension: people rename downloads, and the content is what decides.
func detectImportFormat(path string, data []byte) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".ncx"):
		return "navicat"
	case strings.HasSuffix(lower, ".csv"):
		return "csv"
	case strings.HasSuffix(lower, ".json"):
		// DBeaver's file is a JSON object with a "connections" key.
		return "dbeaver"
	}
	trimmed := strings.TrimSpace(string(pluginreg.TrimBOM(data)))
	switch {
	case strings.HasPrefix(trimmed, "{"):
		return "dbeaver"
	case strings.HasPrefix(trimmed, "<?xml"):
		// Navicat's other export shape; also encrypted, same refusal.
		return "navicat"
	}
	// Everything else is treated as a URL list: one connection per line is the
	// format a human writes by hand.
	return "url"
}

// importerSSLMode folds a source's SSL setting onto this app's SSLMode.
func importerSSLMode(ssl string) string {
	mode := strings.ToLower(strings.TrimSpace(ssl))
	switch mode {
	case "", "false", "disable", "off", "0":
		return "false"
	case "true", "require", "on", "1", "prefer":
		return "require"
	case "verify-ca", "verify-full":
		return mode
	}
	return "false"
}

// defaultPortForEngine is the port a connection gets when the source had none.
func defaultPortForEngine(engine string) int {
	switch engine {
	case "mysql":
		return 3306
	case "postgresql":
		return 5432
	case "sqlserver":
		return 1433
	case "oracle":
		return 1521
	case "redis":
		return 6379
	}
	return 0
}

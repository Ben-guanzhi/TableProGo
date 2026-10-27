package main

// Redis support in TableProGo is a read path first: connect, browse the
// logical databases and their keys, open one key read-only, and run commands
// from the query tab. Every call goes through dbcore.KVDriver, so the client
// never speaks the wire protocol itself.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dbcore/dbcore"
	dbredis "github.com/dbcore/dbcore/redis"
)

const (
	// Redis ships with 16 logical databases; a server configuration can change
	// that, but there is no command that lists them, so the navigator offers
	// the default range and lets a key that lives elsewhere fail visibly.
	redisDatabases = 16
	// One SCAN pass is not guaranteed to see the whole keyspace, so the walk
	// keeps following the cursor. The cap keeps a million-key database from
	// turning a sidebar expand into a multi-second stall.
	redisKeyScanCount = 200
	redisKeyLimit     = 1000
	// The navigator walks SCAN one page per request so a large keyspace opens
	// without waiting for every key. COUNT is a hint; the server may return more.
	redisKeyPageCount = 1000
	redisKeyPageMax   = 5000
	// A collection key is read whole and then cut to what the grid can render.
	kvValueRowLimit = 1000
)

// KVValue is one key opened from the navigator: its identity, remaining
// lifetime, and the rows that describe its value.
//
// Next/Done come from the paged read (P3-3): Next is the opaque cursor for the
// following window — echo it to /api/kv-value-page — and Done says there is no
// further window. Both are empty/true for a whole read or an unpaged driver.
type KVValue struct {
	Key    string      `json:"key"`
	Type   string      `json:"type"`
	TTL    string      `json:"ttl"`
	Result QueryResult `json:"result"`
	Next   string      `json:"next,omitempty"`
	Done   bool        `json:"done"`
}

// openKV builds the redis:// URL from the same connection fields the SQL
// engines use. A blank database means logical database 0.
func openKV(config ConnectionConfig) (dbcore.KVDriver, error) {
	idx, err := redisDBIndex(config.Database)
	if err != nil {
		return nil, err
	}
	scheme := "redis"
	if config.SSLMode == "true" || config.SSLMode == "require" {
		scheme = "rediss"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(config.Host, strconv.Itoa(config.Port)),
		Path:   "/" + strconv.Itoa(idx),
	}
	if config.Username != "" || config.Password != "" {
		u.User = url.UserPassword(config.Username, config.Password)
	}
	return dbredis.Open(u.String())
}

func redisDBIndex(database string) (int, error) {
	database = strings.TrimSpace(database)
	if database == "" {
		return 0, nil
	}
	idx, err := strconv.Atoi(database)
	if err != nil || idx < 0 {
		return 0, fmt.Errorf("redis: database %q is not a logical database index", database)
	}
	return idx, nil
}

func (c *Connection) closeKV() {
	if c.KV != nil {
		_ = c.KV.Close()
		c.KV = nil
	}
}

// withKV runs fn on the requested logical database. SELECT is session state on
// the shared client, so the switch and the command it serves happen under one
// lock: two panels reading different databases can never interleave.
func (a *App) withKV(conn *Connection, database string, fn func(context.Context, dbcore.KVDriver) error) error {
	idx, err := redisDBIndex(database)
	if err != nil {
		return err
	}
	if database == "" {
		idx = conn.kvDefault
	}

	conn.kvMu.Lock()
	defer conn.kvMu.Unlock()
	if !conn.kvDBSet || conn.kvDB != idx {
		if err := conn.KV.SelectDB(context.Background(), idx); err != nil {
			return err
		}
		conn.kvDB, conn.kvDBSet = idx, true
	}
	return fn(context.Background(), conn.KV)
}

// redisKeys lists the keys of one logical database. SCAN may repeat a key and
// may split the keyspace over pages, so the result is de-duplicated and sorted
// before it reaches the tree.
func (a *App) redisKeys(conn *Connection, database string) ([]TableInfo, error) {
	var keys []string
	err := a.withKV(conn, database, func(ctx context.Context, drv dbcore.KVDriver) error {
		seen := make(map[string]bool)
		var cursor uint64
		for {
			page, err := drv.ScanKeys(ctx, cursor, "*", redisKeyScanCount)
			if err != nil {
				return err
			}
			for _, key := range page.Keys {
				if !seen[key] {
					seen[key] = true
					keys = append(keys, key)
				}
			}
			cursor = page.Cursor
			if cursor == 0 || len(keys) >= redisKeyLimit {
				return nil
			}
		}
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	out := make([]TableInfo, len(keys))
	for i, key := range keys {
		out[i] = TableInfo{Name: key, Type: "table"}
	}
	return out, nil
}

// GetKVValue reads one key's value and lifetime. Streams come back as their
// rendered entries; every other collection keeps its natural row shape.
//
// A driver with the kernel's KeyPageReader capability (dbcore P3-3) is read in
// a window instead of whole: a million-member hash used to cost one giant
// HGETALL reply and one giant allocation. When the window covers the whole
// collection the shape is identical to the old full read; otherwise the result
// is Truncated and Next names the window to continue from — echo it through
// GetKVValuePage. A driver without the capability gets today's single read.
func (a *App) GetKVValue(connID, database, key string) (KVValue, error) {
	return a.GetKVValuePage(connID, database, key, "")
}

// GetKVValuePage reads one window of a key's value. cursor is the opaque
// position the previous window returned ("" starts over); Done=true says there
// is no further window — the value was read completely, or the driver cannot
// page and what came back is all there is.
func (a *App) GetKVValuePage(connID, database, key, cursor string) (KVValue, error) {
	conn, err := a.kvConnection(connID)
	if err != nil {
		return KVValue{}, err
	}

	var (
		value     dbcore.RedisValue
		meta      dbcore.KeyMeta
		next      string
		done      = true
		window    int64 // rank the first row of a list/zset window really has
		truncated bool  // the bounded read cut the value (no paged window follows)
	)
	err = a.withKV(conn, database, func(ctx context.Context, drv dbcore.KVDriver) error {
		paged, canPage := dbcore.AsKeyPageReader(drv)
		bounded, canBound := dbcore.AsBoundedValueReader(drv)
		kind, err := drv.KeyType(ctx, key)
		if err != nil {
			return err
		}
		if kind == dbcore.RedisNone {
			// Missing key: let GetValue produce its canonical "not found".
			_, err := drv.GetValue(ctx, key)
			return err
		}
		if canPage && kind != dbcore.RedisStr {
			v, cont, err := kvWindow(ctx, paged, key, kind, cursor, kvValueRowLimit)
			if err != nil {
				return err
			}
			value, next, done = v, cont, cont == ""
			if kind == dbcore.RedisList {
				// The list's index column must carry the position the item
				// really has: the grid edits list items by index (LSET), and a
				// continuation window renumbered from zero would point an edit
				// at the wrong item. The cursor IS that first rank.
				window, _ = parseRankCursor(cursor)
			}
			return nil
		}
		// A string is one cell, and an unpaged driver only ever had the whole
		// read — but neither ever needs the unbounded read again: the bounded
		// capability caps a half-gigabyte string at its first 64 KiB and a
		// whole collection at its first kvValueRowLimit entries. Drivers
		// without the capability keep today's single full read.
		if canBound {
			bv, err := bounded.GetValueBounded(ctx, key, dbcore.RedisValueLimit{MaxEntries: kvValueRowLimit})
			if err != nil {
				return err
			}
			value = bv.Value
			truncated = bv.Truncated
		} else {
			v, err := drv.GetValue(ctx, key)
			if err != nil {
				return err
			}
			value = v
		}
		// KeyMeta is optional in the contract; a driver without it still knows
		// the type from the value, it just cannot say when the key expires.
		if admin, ok := drv.(dbcore.KeyAdmin); ok {
			m, err := admin.KeyMeta(ctx, key)
			if err != nil {
				return err
			}
			meta = m
			return nil
		}
		meta.Kind = value.Type
		return nil
	})
	if err != nil {
		return KVValue{}, err
	}

	kind := value.Type
	if meta.Kind != "" {
		kind = meta.Kind
	}
	result := kvResultWindow(value, window)
	if !done {
		result.Truncated = true // rows exist beyond this window
	}
	if truncated {
		// The bounded read cut the value (a long string, or an unpaged driver's
		// collection): same signal the row cap uses, without a Next window.
		result.Truncated = true
	}
	return KVValue{
		Key:    key,
		Type:   string(kind),
		TTL:    formatTTL(meta),
		Result: result,
		Next:   next,
		Done:   done,
	}, nil
}

// kvWindow reads one window of a collection key through the KeyPageReader
// capabilities (dbcore P3-3). The cursor is per-type and opaque to the client:
// a rank for list/zset, the last entry ID for stream, a SCAN cursor for
// hash/set ("" = start). hash/set windows loop until filled because a SCAN
// count is a hint; the collected members are de-duplicated and sorted, so a
// key that fits one window renders exactly like the old full read.
func kvWindow(ctx context.Context, p dbcore.KeyPageReader, key string,
	kind dbcore.RedisKeyType, cursor string, limit int) (dbcore.RedisValue, string, error) {
	switch kind {
	case dbcore.RedisStr:
		return dbcore.RedisValue{}, "", fmt.Errorf("redis: strings are read whole")
	case dbcore.RedisList:
		start, err := parseRankCursor(cursor)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		items, length, err := p.ListRange(ctx, key, start, start+int64(limit)-1)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		next := ""
		if end := start + int64(len(items)); end < length {
			next = strconv.FormatInt(end, 10)
		}
		return dbcore.RedisValue{Type: kind, Items: items}, next, nil
	case dbcore.RedisZSet:
		start, err := parseRankCursor(cursor)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		members, scores, length, err := p.ZSetRange(ctx, key, start, start+int64(limit)-1)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		next := ""
		if end := start + int64(len(members)); end < length {
			next = strconv.FormatInt(end, 10)
		}
		return dbcore.RedisValue{Type: kind, Items: members, Scores: scores}, next, nil
	case dbcore.RedisStream:
		// StreamRange reads strictly after the ID, so the cursor is the last
		// entry of the previous window ("" = from the start). A full window is
		// the only signal more may follow — a short one ends the walk.
		entries, _, err := p.StreamRange(ctx, key, cursor, int64(limit))
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		v := dbcore.RedisValue{Type: kind, Stream: entries}
		for _, e := range entries {
			v.Items = append(v.Items, renderStreamEntry(e))
		}
		next := ""
		if len(entries) == limit {
			next = entries[len(entries)-1].ID
		}
		return v, next, nil
	case dbcore.RedisSet:
		seen := map[string]bool{}
		var members []string
		scan, err := parseScanCursor(cursor)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		for round := 0; round < kvScanMaxRounds; round++ {
			page, cont, err := p.SetPage(ctx, key, scan, int64(limit))
			if err != nil {
				return dbcore.RedisValue{}, "", err
			}
			for _, m := range page {
				if !seen[m] {
					seen[m] = true
					members = append(members, m)
				}
			}
			scan = cont
			if scan == 0 || len(members) >= limit {
				break
			}
		}
		sort.Strings(members)
		return dbcore.RedisValue{Type: kind, Items: members}, formatCont(scan), nil
	case dbcore.RedisHash:
		fields := map[string]string{}
		scan, err := parseScanCursor(cursor)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		for round := 0; round < kvScanMaxRounds; round++ {
			page, cont, err := p.HashPage(ctx, key, scan, int64(limit))
			if err != nil {
				return dbcore.RedisValue{}, "", err
			}
			for _, f := range page {
				fields[f.Name] = f.Value
			}
			scan = cont
			if scan == 0 || len(fields) >= limit {
				break
			}
		}
		return dbcore.RedisValue{Type: kind, Hash: fields}, formatCont(scan), nil
	default:
		return dbcore.RedisValue{}, "", fmt.Errorf("redis: cannot page %q keys", kind)
	}
}

// kvScanMaxRounds bounds the fill loop the same way the kernel bounds its key
// scan: a cursor that never advances must not hang the request.
const kvScanMaxRounds = 100

func parseRankCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("redis: invalid window cursor %q", cursor)
	}
	return n, nil
}

func parseScanCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("redis: invalid scan cursor %q", cursor)
	}
	return n, nil
}

// formatCont spells a SCAN continuation: cursor 0 means the walk is over.
func formatCont(cursor uint64) string {
	if cursor == 0 {
		return ""
	}
	return strconv.FormatUint(cursor, 10)
}

// renderStreamEntry flattens one entry into the grid's single "entry" column
// exactly as the whole-key read renders it: the ID, then the fields sorted by
// name as "k=v", space separated.
func renderStreamEntry(e dbcore.RedisStreamEntry) string {
	fields := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		fields = append(fields, f.Name+"="+f.Value)
	}
	sort.Strings(fields)
	return e.ID + " " + strings.Join(fields, " ")
}

func formatTTL(meta dbcore.KeyMeta) string {
	if !meta.HasTTL {
		return "永不过期"
	}
	if meta.TTL < time.Second {
		return meta.TTL.Round(time.Millisecond).String()
	}
	return meta.TTL.Round(time.Second).String()
}

// KVKeyInfo is a key's identity without its value: enough for the navigator to
// show what a right-click is about before offering TTL or rename.
type KVKeyInfo struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTL  string `json:"ttl"`
}

// KVKeyPage is one SCAN step. Cursor stays a string because Redis cursors are
// uint64 and a JSON number would round past 2^53. Done means the walk reached
// cursor 0; the keys in that same page are still valid.
type KVKeyPage struct {
	Cursor string   `json:"cursor"`
	Keys   []string `json:"keys"`
	Done   bool     `json:"done"`
}

// GetKVKeyInfo reports type and remaining lifetime without reading the value.
func (a *App) GetKVKeyInfo(connID, database, key string) (KVKeyInfo, error) {
	if key == "" {
		return KVKeyInfo{}, fmt.Errorf("redis: empty key")
	}
	conn, err := a.kvConnection(connID)
	if err != nil {
		return KVKeyInfo{}, err
	}
	var meta dbcore.KeyMeta
	err = a.withKV(conn, database, func(ctx context.Context, drv dbcore.KVDriver) error {
		admin, ok := drv.(dbcore.KeyAdmin)
		if !ok {
			return fmt.Errorf("redis: key info is not supported by this driver")
		}
		m, err := admin.KeyMeta(ctx, key)
		if err != nil {
			return err
		}
		meta = m
		return nil
	})
	if err != nil {
		return KVKeyInfo{}, err
	}
	return KVKeyInfo{Key: key, Type: string(meta.Kind), TTL: formatTTL(meta)}, nil
}

// GetKVKeyPage walks one SCAN step. Empty cursor starts the walk; the returned
// cursor asks for the next page. Keys may repeat across pages — SCAN is allowed
// to return an element more than once — so callers append with de-duplication.
func (a *App) GetKVKeyPage(connID, database, cursor string, count int) (KVKeyPage, error) {
	conn, err := a.kvConnection(connID)
	if err != nil {
		return KVKeyPage{}, err
	}
	var from uint64
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		from, err = strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			return KVKeyPage{}, fmt.Errorf("redis: invalid scan cursor %q", cursor)
		}
	}
	if count <= 0 {
		count = redisKeyPageCount
	}
	if count > redisKeyPageMax {
		count = redisKeyPageMax
	}

	var page *dbcore.RedisScanPage
	err = a.withKV(conn, database, func(ctx context.Context, drv dbcore.KVDriver) error {
		p, err := drv.ScanKeys(ctx, from, "*", count)
		if err != nil {
			return err
		}
		page = p
		return nil
	})
	if err != nil {
		return KVKeyPage{}, err
	}
	keys := append([]string(nil), page.Keys...)
	sort.Strings(keys)
	return KVKeyPage{
		Cursor: strconv.FormatUint(page.Cursor, 10),
		Keys:   keys,
		Done:   page.Cursor == 0,
	}, nil
}

// withKVAdmin runs fn against the optional key-administration capability.
func (a *App) withKVAdmin(connID, database string, fn func(context.Context, dbcore.KeyAdmin) error) error {
	conn, err := a.kvConnection(connID)
	if err != nil {
		return err
	}
	return a.withKV(conn, database, func(ctx context.Context, drv dbcore.KVDriver) error {
		admin, ok := drv.(dbcore.KeyAdmin)
		if !ok {
			return fmt.Errorf("redis: key administration is not supported by this driver")
		}
		return fn(ctx, admin)
	})
}

// SetKVKeyTTL sets a remaining lifetime in seconds; 0 removes the expiry.
func (a *App) SetKVKeyTTL(connID, database, key string, seconds int64) error {
	if key == "" {
		return fmt.Errorf("redis: empty key")
	}
	if seconds < 0 {
		return fmt.Errorf("redis: TTL seconds must not be negative")
	}
	return a.withKVAdmin(connID, database, func(ctx context.Context, admin dbcore.KeyAdmin) error {
		return admin.SetTTL(ctx, key, time.Duration(seconds)*time.Second)
	})
}

// RenameKVKey renames without overwriting an existing target, so a typo cannot
// destroy another key.
func (a *App) RenameKVKey(connID, database, key, newKey string) error {
	newKey = strings.TrimSpace(newKey)
	if key == "" || newKey == "" {
		return fmt.Errorf("redis: key names must not be empty")
	}
	if key == newKey {
		return nil
	}
	return a.withKVAdmin(connID, database, func(ctx context.Context, admin dbcore.KeyAdmin) error {
		return admin.RenameKey(ctx, key, newKey)
	})
}

// DeleteKVKey removes one key; a missing key is a no-op, like DEL.
func (a *App) DeleteKVKey(connID, database, key string) error {
	if key == "" {
		return fmt.Errorf("redis: empty key")
	}
	return a.withKVAdmin(connID, database, func(ctx context.Context, admin dbcore.KeyAdmin) error {
		return admin.DeleteKeys(ctx, key)
	})
}

// SetKVElement overwrites one element in place, keeping the key's TTL. The
// driver decides which identities and columns are writable.
func (a *App) SetKVElement(connID, database string, elem dbcore.RedisElement) error {
	if elem.Key == "" {
		return fmt.Errorf("redis: empty key")
	}
	conn, err := a.kvConnection(connID)
	if err != nil {
		return err
	}
	return a.withKV(conn, database, func(ctx context.Context, drv dbcore.KVDriver) error {
		writer, ok := drv.(dbcore.ElementWriter)
		if !ok {
			return fmt.Errorf("redis: element writes are not supported by this driver")
		}
		return writer.SetElement(ctx, elem)
	})
}

// AddKVRow appends one row to a set (SADD) or a stream (XADD). Those keys have
// no writable value column, so the write adds an identity instead of replacing
// an element. The returned string is the member, or the stream entry id the
// server assigned.
func (a *App) AddKVRow(connID, database string, row dbcore.RedisRow) (string, error) {
	if row.Key == "" {
		return "", fmt.Errorf("redis: empty key")
	}
	if row.Kind != dbcore.RedisSet && row.Kind != dbcore.RedisStream {
		return "", fmt.Errorf("redis: %q keys do not accept appended rows", row.Kind)
	}
	conn, err := a.kvConnection(connID)
	if err != nil {
		return "", err
	}
	var id string
	err = a.withKV(conn, database, func(ctx context.Context, drv dbcore.KVDriver) error {
		appender, ok := drv.(dbcore.RowAppender)
		if !ok {
			return fmt.Errorf("redis: row appends are not supported by this driver")
		}
		got, err := appender.AppendRow(ctx, row)
		if err != nil {
			return err
		}
		id = got
		return nil
	})
	return id, err
}

func kvResult(v dbcore.RedisValue) QueryResult {
	return kvResultAt(v, 0, true)
}

// kvResultWindow renders one continuation window. Only a list has a positional
// identity column — its index must be the item's real rank, not the position
// inside the window, or the grid's LSET would rewrite a different item. A
// window is never cut to the row limit: a SCAN count is a hint, so one call
// can legitimately return more members than asked, and dropping the overflow
// would lose rows with no cursor left to resume from (cursor 0 = done).
func kvResultWindow(v dbcore.RedisValue, start int64) QueryResult {
	return kvResultAt(v, start, false)
}

// kvResultAt renders the value's rows; list indices start at startRank, and
// capRows bounds a whole-key read to what the grid can render (the pre-paging
// behaviour; paged reads are bounded by their own window).
func kvResultAt(v dbcore.RedisValue, startRank int64, capRows bool) QueryResult {
	result := QueryResult{}
	add := func(columns ...Column) {
		result.Columns = append(result.Columns, columns...)
	}
	switch v.Type {
	case dbcore.RedisStr:
		add(Column{Name: "value", Type: "TEXT"})
		result.Rows = append(result.Rows, []interface{}{v.Str})
	case dbcore.RedisList:
		add(Column{Name: "index", Type: "BIGINT"}, Column{Name: "value", Type: "TEXT"})
		for i, item := range v.Items {
			result.Rows = append(result.Rows, []interface{}{startRank + int64(i), item})
		}
	case dbcore.RedisSet:
		add(Column{Name: "member", Type: "TEXT"})
		for _, item := range v.Items {
			result.Rows = append(result.Rows, []interface{}{item})
		}
	case dbcore.RedisZSet:
		add(Column{Name: "member", Type: "TEXT"}, Column{Name: "score", Type: "DOUBLE"})
		for i, item := range v.Items {
			var score float64
			if i < len(v.Scores) {
				score = v.Scores[i]
			}
			result.Rows = append(result.Rows, []interface{}{item, score})
		}
	case dbcore.RedisHash:
		add(Column{Name: "field", Type: "TEXT"}, Column{Name: "value", Type: "TEXT"})
		fields := make([]string, 0, len(v.Hash))
		for field := range v.Hash {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			result.Rows = append(result.Rows, []interface{}{field, v.Hash[field]})
		}
	case dbcore.RedisStream:
		add(Column{Name: "entry", Type: "TEXT"})
		for _, entry := range v.Items {
			result.Rows = append(result.Rows, []interface{}{entry})
		}
	}
	if capRows && len(result.Rows) > kvValueRowLimit {
		result.Rows = result.Rows[:kvValueRowLimit]
		result.Truncated = true
	}
	result.RowCount = len(result.Rows)
	return result
}

// runKVCommand executes one Redis command typed into a query tab. The reply is
// rendered as text by the driver, one row per line, because a Redis reply has
// no column metadata to map onto a grid.
func (a *App) runKVCommand(conn *Connection, database, command string) QueryResult {
	start := time.Now()
	args, err := splitRedisCommand(command)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	if len(args) == 0 {
		return QueryResult{Error: "redis: empty command"}
	}

	var reply dbcore.RedisReply
	err = a.withKV(conn, database, func(ctx context.Context, drv dbcore.KVDriver) error {
		r, err := drv.ExecCommand(ctx, args)
		if err != nil {
			return err
		}
		reply = r
		// A SELECT on the shared client moves it to another logical database;
		// the next request must re-select the one it asked for.
		if strings.EqualFold(args[0], "SELECT") {
			conn.kvDBSet = false
		}
		return nil
	})
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	result := QueryResult{
		Columns:       []Column{{Name: "result", Type: "TEXT"}},
		ExecutionTime: time.Since(start).Seconds(),
	}
	raw := strings.TrimRight(reply.Raw, "\r\n")
	if raw == "" {
		return result
	}
	for _, line := range strings.Split(raw, "\n") {
		result.Rows = append(result.Rows, []interface{}{strings.TrimSuffix(line, "\r")})
	}
	result.RowCount = len(result.Rows)
	return result
}

// splitRedisCommand tokenizes one command, honoring single and double quotes
// and backslash escapes so values with spaces survive.
func splitRedisCommand(line string) ([]string, error) {
	var (
		out     []string
		token   strings.Builder
		quote   rune
		escaped bool
		inToken bool
	)
	flush := func() {
		if inToken {
			out = append(out, token.String())
			token.Reset()
			inToken = false
		}
	}
	for _, r := range line {
		switch {
		case escaped:
			token.WriteRune(r)
			escaped = false
			inToken = true
		case r == '\\':
			escaped = true
			inToken = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			inToken = true
		case r == '\'' || r == '"':
			quote = r
			inToken = true
		case unicode.IsSpace(r):
			flush()
		default:
			token.WriteRune(r)
			inToken = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("redis: trailing backslash")
	}
	if quote != 0 {
		return nil, fmt.Errorf("redis: unterminated quote")
	}
	flush()
	return out, nil
}

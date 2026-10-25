package main

import (
	"context"
	"fmt"
	"time"

	"github.com/dbcore/dbcore/schemadiff"
)

// This file is the client's side of schema comparison and synchronization. The
// rules live in dbcore/schemadiff — what counts as a difference, in which order
// changes have to run, and how each statement is spelled. What lives here is
// everything that needs a live connection: reading both sides, keeping the plan
// between the two round-trips, and running the statements the user picked.
//
// The plan is the important design point. Applying a sync means executing DDL,
// so the client never sends SQL to be executed: it sends the plan id and the
// entry ids it selected, and the statements come back out of the plan this
// process computed. A stale or forged plan id finds nothing and nothing runs.

// CompareTarget names one side of a comparison: a connection plus the database
// and schema within it.
type CompareTarget struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
}

// CompareRequest names both sides and how strictly to compare them.
type CompareRequest struct {
	Source  CompareTarget  `json:"source"`
	Target  CompareTarget  `json:"target"`
	Options CompareOptions `json:"options"`
}

// CompareOptions narrows what counts as a difference.
type CompareOptions struct {
	IgnoreDefaults    bool `json:"ignoreDefaults"`
	IgnoreNullability bool `json:"ignoreNullability"`
	IgnoreIndexes     bool `json:"ignoreIndexes"`
	IgnoreForeignKeys bool `json:"ignoreForeignKeys"`
}

// CompareEntry is one change for the UI to show and the user to tick.
type CompareEntry struct {
	ID          string `json:"id"`
	Op          string `json:"op"`
	Table       string `json:"table"`
	Object      string `json:"object"`
	From        string `json:"from"`
	To          string `json:"to"`
	Phase       int    `json:"phase"`
	Reason      string `json:"reason"`
	Destructive bool   `json:"destructive"`
	// Selected is the initial tick. Additive changes start ticked; destructive
	// ones start unticked so running a sync takes a deliberate act.
	Selected bool `json:"selected"`
}

// CompareStatement is one line of the rendered plan.
type CompareStatement struct {
	EntryID string `json:"entryId"`
	SQL     string `json:"sql"`
}

// CompareResult is what the compare tab renders.
type CompareResult struct {
	PlanID      string             `json:"planId"`
	SourceLabel string             `json:"sourceLabel"`
	TargetLabel string             `json:"targetLabel"`
	Kind        string             `json:"kind"`
	Schema      string             `json:"schema"`
	Entries     []CompareEntry     `json:"entries"`
	Statements  []CompareStatement `json:"statements"`
	Warnings    []string           `json:"warnings"`
}

// ApplySyncRequest asks for a stored plan's selected entries to be executed.
type ApplySyncRequest struct {
	PlanID   string   `json:"planId"`
	EntryIDs []string `json:"entryIds"`
	// Confirm is the user's explicit yes. It is part of the request rather than
	// a UI-only step so the route cannot be driven without it.
	Confirm bool `json:"confirm"`
}

// ApplyFailure is one statement that did not run.
type ApplyFailure struct {
	EntryID string `json:"entryId"`
	SQL     string `json:"sql"`
	Error   string `json:"error"`
}

// ApplySyncResult reports how far the run got. A sync is not atomic: MySQL
// commits DDL implicitly, so pretending otherwise would be worse than telling
// the user which statement stopped it.
type ApplySyncResult struct {
	Applied  int            `json:"applied"`
	Total    int            `json:"total"`
	Failures []ApplyFailure `json:"failures"`
}

// comparePlan is a computed plan held between the compare and the apply call.
type comparePlan struct {
	target     CompareTarget
	kind       string
	schema     string
	statements []schemadiff.Statement
	createdAt  time.Time
}

// maxComparePlans caps how many plans one process keeps. A plan is a few
// hundred bytes, but an app left open for a day must not accumulate them.
const maxComparePlans = 16

// CompareSchemas reads both sides and returns the changes that would make the
// target look like the source, with the statements that carry them out.
func (a *App) CompareSchemas(source, target CompareTarget, opts CompareOptions) (CompareResult, error) {
	ctx := context.Background()

	sourceConn, err := a.liveConnection(source.ConnID)
	if err != nil {
		return CompareResult{}, err
	}
	targetConn, err := a.liveConnection(target.ConnID)
	if err != nil {
		return CompareResult{}, err
	}
	for _, conn := range []*Connection{sourceConn, targetConn} {
		if conn.Type == "redis" {
			return CompareResult{}, fmt.Errorf("结构对比不支持 Redis 连接")
		}
		if conn.DB == nil && !pluginConnection(conn) {
			return CompareResult{}, fmt.Errorf("not connected")
		}
	}

	sourceDrv, err := a.sqlDriver(sourceConn, source.Database)
	if err != nil {
		return CompareResult{}, err
	}
	targetDrv, err := a.sqlDriver(targetConn, target.Database)
	if err != nil {
		return CompareResult{}, err
	}

	sourceSchema := compareSchema(sourceConn, source.Database, source.Schema)
	targetSchema := compareSchema(targetConn, target.Database, target.Schema)

	sourceSnap, err := schemadiff.Collect(ctx, sourceDrv, sourceSchema)
	if err != nil {
		return CompareResult{}, err
	}
	targetSnap, err := schemadiff.Collect(ctx, targetDrv, targetSchema)
	if err != nil {
		return CompareResult{}, err
	}

	entries := schemadiff.Diff(sourceSnap, targetSnap, schemadiff.Options{
		IgnoreDefaults:    opts.IgnoreDefaults,
		IgnoreNullability: opts.IgnoreNullability,
		IgnoreIndexes:     opts.IgnoreIndexes,
		IgnoreForeignKeys: opts.IgnoreForeignKeys,
	})
	script := schemadiff.Script(sourceSnap, targetSnap, entries)

	result := CompareResult{
		PlanID:      a.storeComparePlan(target, targetSnap.Kind, targetSchema, script.Statements),
		SourceLabel: compareLabel(sourceConn, source.Database, sourceSchema),
		TargetLabel: compareLabel(targetConn, target.Database, targetSchema),
		Kind:        targetSnap.Kind,
		Schema:      targetSchema,
		Entries:     make([]CompareEntry, 0, len(entries)),
		Statements:  make([]CompareStatement, 0, len(script.Statements)),
		Warnings:    script.Warnings,
	}
	for _, e := range entries {
		result.Entries = append(result.Entries, CompareEntry{
			ID:          e.ID,
			Op:          string(e.Op),
			Table:       e.Table,
			Object:      e.Object,
			From:        e.From,
			To:          e.To,
			Phase:       e.Phase,
			Reason:      e.Reason,
			Destructive: e.Destructive,
			Selected:    !e.Destructive,
		})
	}
	for _, s := range script.Statements {
		result.Statements = append(result.Statements, CompareStatement{EntryID: s.EntryID, SQL: s.SQL})
	}
	return result, nil
}

// ApplySync executes the selected statements of a stored plan against the
// target. It stops at the first failure and reports how many statements landed.
func (a *App) ApplySync(req ApplySyncRequest) (ApplySyncResult, error) {
	if !req.Confirm {
		return ApplySyncResult{}, fmt.Errorf("同步需要显式确认")
	}
	plan, ok := a.loadComparePlan(req.PlanID)
	if !ok {
		return ApplySyncResult{}, fmt.Errorf("对比计划已失效，请重新对比后再执行")
	}
	selected := make(map[string]bool, len(req.EntryIDs))
	for _, id := range req.EntryIDs {
		selected[id] = true
	}

	statements := make([]schemadiff.Statement, 0, len(plan.statements))
	for _, s := range plan.statements {
		if selected[s.EntryID] {
			statements = append(statements, s)
		}
	}
	result := ApplySyncResult{Total: len(statements), Failures: []ApplyFailure{}}
	if len(statements) == 0 {
		return result, nil
	}

	conn, err := a.liveConnection(plan.target.ConnID)
	if err != nil {
		return ApplySyncResult{}, err
	}
	drv, err := a.sqlDriver(conn, plan.target.Database)
	if err != nil {
		return ApplySyncResult{}, err
	}

	ctx := context.Background()
	for _, s := range statements {
		if verdict := guardWrite(a.GetSettings().SafeMode, s.SQL); verdict.Blocked {
			result.Failures = append(result.Failures, ApplyFailure{
				EntryID: s.EntryID, SQL: s.SQL,
				Error: nonEmpty(verdict.Reason, "Safe Mode 已阻止该语句"),
			})
			break
		}
		if _, err := drv.Execute(ctx, s.SQL); err != nil {
			result.Failures = append(result.Failures, ApplyFailure{
				EntryID: s.EntryID, SQL: s.SQL, Error: err.Error(),
			})
			break
		}
		result.Applied++
	}
	return result, nil
}

// storeComparePlan files a plan under a fresh id so the apply step can refer to
// it without the client carrying SQL.
func (a *App) storeComparePlan(target CompareTarget, kind, schema string, statements []schemadiff.Statement) string {
	a.compareMu.Lock()
	defer a.compareMu.Unlock()
	if a.comparePlans == nil {
		a.comparePlans = map[string]*comparePlan{}
	}
	if len(a.comparePlans) >= maxComparePlans {
		oldest := ""
		var oldestAt time.Time
		for id, p := range a.comparePlans {
			if oldest == "" || p.createdAt.Before(oldestAt) {
				oldest, oldestAt = id, p.createdAt
			}
		}
		delete(a.comparePlans, oldest)
	}
	id := fmt.Sprintf("plan-%d", time.Now().UnixNano())
	a.comparePlans[id] = &comparePlan{
		target: target, kind: kind, schema: schema, statements: statements, createdAt: time.Now(),
	}
	return id
}

func (a *App) loadComparePlan(id string) (*comparePlan, bool) {
	a.compareMu.Lock()
	defer a.compareMu.Unlock()
	p, ok := a.comparePlans[id]
	return p, ok
}

// compareSchema resolves the catalog namespace the way the rest of the navigator
// does: MySQL's schema is its database, and every engine has a default.
func compareSchema(conn *Connection, database, schema string) string {
	resolved := defaultSchema(conn.Type, schema)
	if resolved == "" && conn.Type == "mysql" {
		return database
	}
	return resolved
}

func compareLabel(conn *Connection, database, schema string) string {
	label := conn.Name
	if label == "" {
		label = conn.ID
	}
	if database != "" {
		label += "/" + database
	}
	if schema != "" && schema != database {
		label += "." + schema
	}
	return label
}

func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

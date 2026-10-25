package main

// Execution plans. The per-engine shapes live in the kernel's sqltext package
// (sqltext.ExplainQuery) like every other statement shape; this file only picks
// the session the plan runs in.

import (
	"context"
	"time"

	"github.com/dbcore/dbcore"

	tableprosql "github.com/dbcore/dbcore/sqltext"
)

// ExplainQuery returns the execution plan of query against database, rendered
// as a result grid. Nothing in a plan executes the statement — EXPLAIN DELETE
// does not delete — so Safe Mode has no say here, and the plan does not join a
// tab's transaction.
func (a *App) ExplainQuery(connID string, query string, database string) QueryResult {
	conn, err := a.connection(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	plan, err := tableprosql.ExplainQuery(conn.Type, query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	ctx, done := a.beginRun(connID)
	defer done()
	start := time.Now()

	// Engines without session state send the plan statement through the normal
	// statement path — which also covers plugin-backed connections, whose child
	// process classifies it as a read on its own.
	if plan.Prepare == "" {
		drv, err := a.execDriver(conn, database)
		if err != nil {
			return QueryResult{Error: err.Error()}
		}
		return runDriverStatement(ctx, drv, plan.Query, start)
	}

	// SQL Server's SHOWPLAN_XML and Oracle's PLAN_TABLE are session state: the
	// prepare, the plan query and the cleanup must land on one and the same
	// connection, so the session is taken from the pool for the whole recipe.
	// A plugin child serves statements one at a time over its protocol and
	// cannot hold that state open, so it is refused rather than mis-routed.
	if pluginConnection(conn) {
		return QueryResult{Error: "explain on " + conn.Type + " needs a dedicated session and is not available for plugin-backed connections"}
	}
	db, err := a.queryPool(conn, database)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	session, err := db.Conn(ctx)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer session.Close()
	if _, err := session.ExecContext(ctx, plan.Prepare); err != nil {
		return QueryResult{Error: err.Error()}
	}
	// The cleanup must run even when the plan query was cancelled, or a pooled
	// connection would come back with SHOWPLAN_XML still on and answer every
	// later statement with a plan instead of running it.
	defer func() {
		_, _ = session.ExecContext(context.WithoutCancel(ctx), plan.Cleanup)
	}()
	res, err := dbcore.RunQuery(ctx, session, plan.Query, nil)
	result := queryResultFrom(res, err)
	result.ExecutionTime = time.Since(start).Seconds()
	return result
}

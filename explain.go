package main

// Execution plans. The per-engine shapes live in the kernel's sqltext package
// (sqltext.ExplainQuery) like every other statement shape; this file only picks
// the session the plan runs in.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dbcore/dbcore"

	tableprosql "github.com/dbcore/dbcore/sqltext"
	"tablepro-go/internal/explainplan"
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

// ExplainPlanTree returns the same plan as a TREE plus its raw payload, for the
// visual plan view.
//
// The difference from ExplainQuery is the statement it sends: this asks for the
// engine's machine-readable format (JSON/XML/rows) so a parser can walk it,
// where ExplainQuery asks for the human-readable text a grid shows. Both shapes
// come from the kernel (sqltext) because they are statement text, not render
// details.
//
// Oracle has no structured form, so it answers with Format "text" and the caller
// keeps the grid: pretending to draw a plan we could only read as prose would be
// worse than showing the prose.
func (a *App) ExplainPlanTree(connID string, query string, database string) (explainplan.Plan, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return explainplan.Plan{}, err
	}
	recipe, format, ok, err := tableprosql.ExplainPlanStructured(conn.Type, query)
	if err != nil {
		return explainplan.Plan{}, err
	}
	if !ok {
		text := a.ExplainQuery(connID, query, database)
		if text.Error != "" {
			return explainplan.Plan{}, fmt.Errorf("%s", text.Error)
		}
		return *explainplan.TextPlan(resultToText(text)), nil
	}

	ctx, done := a.beginRun(connID)
	defer done()
	start := time.Now()

	var result QueryResult
	if recipe.Prepare == "" {
		drv, err := a.execDriver(conn, database)
		if err != nil {
			return explainplan.Plan{}, err
		}
		result = runDriverStatement(ctx, drv, recipe.Query, start)
	} else {
		// Session-state engines (SQL Server): prepare, plan and cleanup must land
		// on one connection. A plugin child cannot hold that state open.
		if pluginConnection(conn) {
			return explainplan.Plan{}, fmt.Errorf("该引擎的结构化计划需要专用会话，插件连接不支持")
		}
		db, err := a.queryPool(conn, database)
		if err != nil {
			return explainplan.Plan{}, err
		}
		session, err := db.Conn(ctx)
		if err != nil {
			return explainplan.Plan{}, err
		}
		defer session.Close()
		if _, err := session.ExecContext(ctx, recipe.Prepare); err != nil {
			return explainplan.Plan{}, err
		}
		defer func() {
			_, _ = session.ExecContext(context.WithoutCancel(ctx), recipe.Cleanup)
		}()
		res, qerr := dbcore.RunQuery(ctx, session, recipe.Query, nil)
		result = queryResultFrom(res, qerr)
		result.ExecutionTime = time.Since(start).Seconds()
	}

	if result.Error != "" {
		return explainplan.Plan{}, fmt.Errorf("%s", result.Error)
	}
	payload := resultToText(result)
	switch format {
	case tableprosql.FormatJSON:
		return derefPlan(explainplan.ParseJSON(payload))
	case tableprosql.FormatRows:
		columns := make([]string, len(result.Columns))
		for i, c := range result.Columns {
			columns[i] = c.Name
		}
		rows := make([][]string, 0, len(result.Rows))
		for _, r := range result.Rows {
			row := make([]string, len(r))
			for i, cell := range r {
				row[i] = cellString(cell)
			}
			rows = append(rows, row)
		}
		return derefPlan(explainplan.ParseRows(columns, rows))
	case tableprosql.FormatXML:
		return derefPlan(explainplan.ParseXML(payload))
	}
	return explainplan.Plan{}, fmt.Errorf("未知的计划格式 %q", format)
}

// derefPlan turns (Plan, error) into the signature the caller wants.
func derefPlan(p *explainplan.Plan, err error) (explainplan.Plan, error) {
	if err != nil {
		return explainplan.Plan{}, err
	}
	if p == nil {
		return explainplan.Plan{}, fmt.Errorf("计划解析返回空")
	}
	return *p, nil
}

// resultToText flattens a result into the payload string a plan parser reads. A
// plan is either one document in one cell (JSON, XML) or a table (SQLite's rows),
// so both shapes are covered.
func resultToText(result QueryResult) string {
	if len(result.Columns) == 1 && len(result.Rows) == 1 && len(result.Rows[0]) == 1 {
		return cellString(result.Rows[0][0])
	}
	var b strings.Builder
	b.WriteString(strings.Join(columnNames(result.Columns), " | "))
	b.WriteString("\n")
	for _, row := range result.Rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = cellString(c)
		}
		b.WriteString(strings.Join(cells, " | "))
		b.WriteString("\n")
	}
	return b.String()
}

func columnNames(columns []Column) []string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = c.Name
	}
	return out
}

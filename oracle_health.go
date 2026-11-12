package main

// W9: Oracle object health — the invalid-object list, one-click recompile, and the
// dependency view.
//
// The three pieces are what an Oracle developer actually does when a deployment
// leaves something broken: list what is INVALID, recompile it, and — when the
// recompile does not fix it — read the compiler's errors.
//
// The non-obvious part, found on a live instance: Oracle ACCEPTS the ALTER …
// COMPILE statement even when the object cannot be compiled. It records the
// diagnostics in ALL_ERRORS and leaves the object INVALID. So "compile succeeded"
// and "the object is fixed" are different facts, and this file never conflates
// them: after every recompile it re-reads the invalid list and the errors.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dbcore/dbcore"
	tableprosql "github.com/dbcore/dbcore/sqltext"
)

// OracleObject is one INVALID object in the list.
type OracleObject struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// OracleObjectHealth is the panel's data.
type OracleObjectHealth struct {
	// Supported is false for a non-Oracle connection: the panel hides rather than
	// showing an empty list that looks like "everything is fine".
	Supported bool           `json:"supported"`
	Objects   []OracleObject `json:"objects"`
	// Note explains why the list is empty when something went wrong reading it.
	Note string `json:"note,omitempty"`
}

// OracleRecompileRequest names one object to recompile.
type OracleRecompileRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	// Confirm is required: recompiling writes to the data dictionary.
	Confirm bool `json:"confirm"`
}

// OracleRecompileResult reports what happened AND what state the object is in now.
type OracleRecompileResult struct {
	// Compiled is whether the statement itself ran.
	Compiled bool `json:"compiled"`
	// StillInvalid is the real answer to "did that fix it".
	StillInvalid bool `json:"stillInvalid"`
	Errors       []OracleCompileError `json:"errors"`
	// Error is a failure of the request itself (no connection, refused, …).
	Error string `json:"error,omitempty"`
}

// OracleCompileError is one diagnostic.
type OracleCompileError struct {
	Line     int    `json:"line"`
	Position int    `json:"position"`
	Text     string `json:"text"`
}

// OracleDependency is one edge of the dependency graph.
type OracleDependency struct {
	Direction string `json:"direction"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Owner     string `json:"owner"`
}

// oracleHealth resolves the connection's driver and reports whether it can answer
// object-health questions at all.
func (a *App) oracleHealth(connID string, database string) (dbcore.ObjectHealthReader, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}
	// The engine check uses the normalized spelling so an Oracle protocol alias
	// resolves the same way every other Oracle path does.
	if tableprosql.Normalize(conn.Type) != tableprosql.Oracle {
		return nil, fmt.Errorf("该连接不是 Oracle")
	}
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return nil, err
	}
	health, ok := dbcore.AsObjectHealth(drv)
	if !ok {
		return nil, fmt.Errorf("该驱动不支持对象健康检查")
	}
	return health, nil
}

// ListInvalidObjects lists the schema's INVALID objects.
func (a *App) ListInvalidObjects(connID, database, schema string) OracleObjectHealth {
	health, err := a.oracleHealth(connID, database)
	if err != nil {
		// A non-Oracle connection is not an error the user should see: the panel
		// simply does not apply, and Supported=false says so.
		if strings.Contains(err.Error(), "不是 Oracle") {
			return OracleObjectHealth{Supported: false, Note: "仅 Oracle 支持无效对象检查"}
		}
		return OracleObjectHealth{Supported: true, Note: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	objects, err := health.InvalidObjects(ctx, schema)
	if err != nil {
		return OracleObjectHealth{Supported: true, Note: err.Error()}
	}
	out := OracleObjectHealth{Supported: true, Objects: make([]OracleObject, 0, len(objects))}
	for _, o := range objects {
		out.Objects = append(out.Objects, OracleObject{Name: o.Name, Type: o.Type})
	}
	return out
}

// RecompileObject recompiles one object and reports its resulting state.
//
// The statement goes through guardWrite like every other write: recompiling is a
// data-dictionary change, and the same Safe Mode that gates a user's DDL gates
// this. A blocked statement is reported, not run.
func (a *App) RecompileObject(req OracleRecompileRequest) OracleRecompileResult {
	if !req.Confirm {
		return OracleRecompileResult{Error: "重编译需要显式确认"}
	}
	health, err := a.oracleHealth(req.ConnID, req.Database)
	if err != nil {
		return OracleRecompileResult{Error: err.Error()}
	}
	conn, err := a.connection(req.ConnID)
	if err != nil {
		return OracleRecompileResult{Error: err.Error()}
	}
	drv, err := a.sqlDriver(conn, req.Database)
	if err != nil {
		return OracleRecompileResult{Error: err.Error()}
	}
	stmt, err := tableprosql.CompileStatement(conn.Type, req.Schema, req.Type, req.Name)
	if err != nil {
		return OracleRecompileResult{Error: err.Error()}
	}
	// Safe Mode: a recompile is DDL. The strictest level blocks it like any other
	// write; that is the point of routing it here rather than around the guard.
	if verdict := guardWrite(a.GetSettings().SafeMode, stmt); verdict.Blocked {
		return OracleRecompileResult{Error: verdict.Reason}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := drv.Execute(ctx, stmt); err != nil {
		// A statement-level failure (bad name, permission) is the request's
		// problem, and there is no new state to report.
		return OracleRecompileResult{Error: err.Error()}
	}

	// The statement ran. Now the only question that matters: is the object valid?
	result := OracleRecompileResult{Compiled: true}
	objects, listErr := health.InvalidObjects(ctx, req.Schema)
	if listErr == nil {
		for _, o := range objects {
			if strings.EqualFold(o.Name, req.Name) {
				result.StillInvalid = true
			}
		}
	}
	if errs, err := health.CompileErrors(ctx, req.Schema, req.Name); err == nil {
		for _, e := range errs {
			result.Errors = append(result.Errors, OracleCompileError{
				Line: e.Line, Position: e.Position, Text: e.Text,
			})
		}
	}
	return result
}

// ListObjectDependencies lists one object's edges in both directions.
func (a *App) ListObjectDependencies(connID, database, schema, object string) ([]OracleDependency, error) {
	health, err := a.oracleHealth(connID, database)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	deps, err := health.ObjectDependencies(ctx, schema, object)
	if err != nil {
		return nil, err
	}
	out := make([]OracleDependency, 0, len(deps))
	for _, d := range deps {
		out = append(out, OracleDependency{
			Direction: d.Direction, Name: d.Name, Type: d.Type, Owner: d.Owner,
		})
	}
	return out, nil
}

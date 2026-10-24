package main

// Executing a .sql script (dump) as an import. The statement splitter lives in
// internal/sqlscript; this file runs the statements through the kernel driver.

import (
	"context"
	"fmt"
	"os"

	"github.com/dbcore/dbcore"

	"tablepro-go/internal/sqlscript"
)

// ImportSQLScript reads path and executes every statement, in one transaction
// unless SkipErrors is set. It reports statements run rather than rows written,
// because the script decides what it touches.
func (a *App) ImportSQLScript(req ImportRequest) (ImportResult, error) {
	res := ImportResult{}
	data, err := os.ReadFile(req.Path)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	statements := sqlscript.Split(string(data))
	if len(statements) == 0 {
		err := fmt.Errorf("import: no statements found in %s", req.Path)
		res.Error = err.Error()
		return res, err
	}

	conn, err := a.connection(req.ConnID)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	drv, err := a.execDriver(conn, req.Database)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	ctx := context.Background()

	if req.Options.SkipErrors {
		for _, stmt := range statements {
			if _, err := drv.Execute(ctx, stmt); err != nil {
				res.Skipped++
				continue
			}
			res.Statements++
		}
		return res, nil
	}

	txDrv, ok := drv.(dbcore.SQLTxDriver)
	if !ok {
		for _, stmt := range statements {
			if _, err := drv.Execute(ctx, stmt); err != nil {
				res.Error = err.Error()
				return res, err
			}
			res.Statements++
		}
		return res, nil
	}
	tx, err := txDrv.Begin(ctx, dbcore.TxMode{})
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	for _, stmt := range statements {
		if _, err := txDrv.ExecuteIn(tx, ctx, stmt); err != nil {
			_ = tx.Rollback()
			res.Error = err.Error()
			return res, err
		}
		res.Statements++
	}
	if err := tx.Commit(); err != nil {
		res.Error = err.Error()
		return res, err
	}
	return res, nil
}

package main

// Executing a .sql script (dump) as an import. The statement splitter lives in
// internal/sqlscript; this file runs the statements through the kernel driver.

import (
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
	ctx, done := a.beginRun(req.ConnID)
	defer done()

	if req.Options.SkipErrors {
		for i, stmt := range statements {
			if _, err := drv.Execute(ctx, stmt); err != nil {
				if res.FirstError == "" {
					res.FirstError = fmt.Sprintf("语句 %d: %s", i+1, err.Error())
				}
				res.Skipped++
				continue
			}
			res.Statements++
		}
		return res, nil
	}

	txDrv, ok := drv.(dbcore.SQLTxDriver)
	if !ok {
		for i, stmt := range statements {
			if _, err := drv.Execute(ctx, stmt); err != nil {
				res.Error = fmt.Sprintf("语句 %d: %s", i+1, err.Error())
				return res, fmt.Errorf("语句 %d: %w", i+1, err)
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
	for i, stmt := range statements {
		if _, err := txDrv.ExecuteIn(tx, ctx, stmt); err != nil {
			_ = tx.Rollback()
			res.Error = fmt.Sprintf("语句 %d: %s", i+1, err.Error())
			return res, fmt.Errorf("语句 %d: %w", i+1, err)
		}
		res.Statements++
	}
	if err := tx.Commit(); err != nil {
		res.Error = err.Error()
		return res, err
	}
	return res, nil
}

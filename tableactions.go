package main

import (
	"errors"
	"fmt"
	"strings"

	tableprosql "github.com/dbcore/dbcore/sqltext"
)

// What the sidebar's object menu can do to a table or a view. Each one is
// rendered here from the catalog's own name parts: the menu picks an action and
// a name, it cannot send a statement of its own.
const (
	actionDrop     = "drop"
	actionTruncate = "truncate"
	actionRename   = "rename"
)

// objectNames qualifies both sides of an action: the object as it is now, and -
// for a rename - as it will be.
type objectNames struct {
	dbType     string
	objectType string
	database   string
	schema     string
	table      string
	newName    string
}

func (o objectNames) current() (string, error) {
	return tableName(o.dbType, o.database, o.schema, o.table)
}

func (o objectNames) next() (string, error) {
	return tableName(o.dbType, o.database, o.schema, o.newName)
}

// tableActionStatement renders one action against one object. schema is already
// the default filled in, and objectType is "table" or "view" as the catalog
// describes it - which is why the caller verifies the name before asking.
func tableActionStatement(o objectNames, action string) (string, error) {
	current, err := o.current()
	if err != nil {
		return "", err
	}
	view := o.objectType == "view"

	switch action {
	case actionDrop:
		if view {
			return "DROP VIEW " + current, nil
		}
		return "DROP TABLE " + current, nil

	case actionTruncate:
		if view {
			return "", fmt.Errorf("视图没有数据可清空")
		}
		return tableprosql.TruncateStatement(o.dbType, current)

	case actionRename:
		if !isIdentifierLike(o.newName) {
			return "", fmt.Errorf("%q is not a valid new name", o.newName)
		}
		if o.newName == o.table {
			return "", fmt.Errorf("新名字和原来一样")
		}
		next := ""
		if o.dbType == "mysql" {
			// MySQL's rename names both sides in full, so the table cannot
			// drift into another database on the way.
			var err error
			next, err = o.next()
			if err != nil {
				return "", err
			}
		}
		return tableprosql.RenameStatement(o.dbType, current, next, o.schema, o.table, o.newName)
	}
	return "", fmt.Errorf("unknown table action %q", action)
}

// needsConfirmationError says Safe Mode paused the action: nothing ran. The
// HTTP layer turns it into a structured pause the UI can confirm and resend,
// instead of the caller mistaking it for an execution failure.
type needsConfirmationError struct{ reason string }

func (e needsConfirmationError) Error() string { return e.reason }

func isNeedsConfirmation(err error) (string, bool) {
	var e needsConfirmationError
	if errors.As(err, &e) {
		return e.reason, true
	}
	return "", false
}

// TableAction runs one of the menu's actions and returns the statement it ran,
// so the caller can say out loud what happened.
//
// The object is looked up in the catalog first: the name has to be one the
// database actually has, and whether it is a table or a view follows the
// catalog rather than whatever the request claimed. That is what lets a name
// with odd characters through - the quoting is then the only thing standing
// between it and a statement, and it was never the client's to write.
func (a *App) TableAction(connID, database, schema, table, action, newName string) (string, error) {
	return a.runTableAction(connID, database, schema, table, action, newName, false)
}

// TableActionConfirmed is TableAction after the user acknowledged the Safe Mode
// warning. A distinct entry point, like ExecuteQueryConfirmed, so skipping the
// prompt has to be explicit in the caller's code.
func (a *App) TableActionConfirmed(connID, database, schema, table, action, newName string) (string, error) {
	return a.runTableAction(connID, database, schema, table, action, newName, true)
}

func (a *App) runTableAction(connID, database, schema, table, action, newName string, force bool) (string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	if database == "" {
		database = conn.Database
	}
	schema = defaultSchema(conn.Type, schema)

	object, err := a.lookupObject(connID, database, schema, table, false)
	if err != nil {
		return "", err
	}
	if object == nil {
		return "", fmt.Errorf("%s 不在这个库里，也许已经被改掉，刷新侧栏再看一次", table)
	}
	objectType := object.Type
	newName = strings.TrimSpace(newName)
	if action == actionRename && newName != "" {
		next, err := a.lookupObject(connID, database, schema, newName, true)
		if err != nil {
			return "", err
		}
		if next != nil {
			return "", fmt.Errorf("%s 已经存在", newName)
		}
	}

	statement, err := tableActionStatement(objectNames{
		dbType:     conn.Type,
		objectType: objectType,
		database:   database,
		schema:     schema,
		table:      table,
		newName:    newName,
	}, action)
	if err != nil {
		return "", err
	}
	// The guard runs here, not by accident through ExecuteQuery: a pause has to
	// come back as a confirmation request, not as a generic error the UI would
	// show as "the drop failed".
	if guard := guardWrite(a.GetSettings().SafeMode, statement); guard.Blocked {
		return statement, fmt.Errorf("%s", guard.Reason)
	} else if guard.NeedsConfirmation && !force {
		return statement, needsConfirmationError{reason: guard.Reason}
	}
	// The same path a typed statement takes, so the action lands in the
	// history with its error if it fails.
	result := a.ExecuteQueryConfirmed(connID, statement, database)
	if result.Error != "" {
		return statement, fmt.Errorf("%s", result.Error)
	}
	return statement, nil
}

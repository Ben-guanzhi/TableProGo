package main

// 表触发器：列表进结构页，定义点开即看。方言配方在内核
// sqltext.TriggersQuery/TriggerDefinitionQuery，这里只做接线：归一 schema、
// 执行、映射行。读失败不拖垮结构页——记日志返回空列表。

import (
	"fmt"
	"log"

	"github.com/dbcore/dbcore"
	tableprosql "github.com/dbcore/dbcore/sqltext"

	"tablepro-go/internal/catalogmodel"
)

// resolveTriggerSchema 归一目录命名空间：MySQL 用库当 schema，PG 缺省 public。
func resolveTriggerSchema(conn *Connection, database, schema string) string {
	if conn.Type == "mysql" {
		if schema == "" {
			return database
		}
		return schema
	}
	return defaultSchema(conn.Type, schema)
}

func (a *App) tableTriggers(conn *Connection, database, schema, table string) []catalogmodel.TriggerInfo {
	query, args, err := tableprosql.TriggersQuery(conn.Type, resolveTriggerSchema(conn, database, schema), table)
	if err != nil {
		log.Printf("triggers: %v", err)
		return nil
	}
	ctx, done := a.beginRun(conn.ID)
	defer done()
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		log.Printf("triggers: %v", err)
		return nil
	}
	bound := make([]dbcore.Value, 0, len(args))
	for _, arg := range args {
		bound = append(bound, dbcore.ValueOf(arg))
	}
	res, err := drv.Execute(ctx, query, bound...)
	if err != nil {
		log.Printf("triggers: %v", err)
		return nil
	}
	out := make([]catalogmodel.TriggerInfo, 0, len(res.Rows))
	for _, row := range res.Rows {
		t := catalogmodel.TriggerInfo{}
		if len(row) > 0 {
			t.Name = cellString(row[0])
		}
		if len(row) > 1 {
			t.Timing = cellString(row[1])
		}
		if len(row) > 2 {
			t.Event = cellString(row[2])
		}
		if len(row) > 3 {
			t.Enabled = cellString(row[3])
		}
		out = append(out, t)
	}
	return out
}

// cellString flattens a driver cell into text (nil → empty).
func cellString(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// GetTriggerDefinition returns one trigger's definition text.
func (a *App) GetTriggerDefinition(connID, database, schema, trigger string) (string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	if conn.Type == "redis" {
		return "", fmt.Errorf("Redis 没有触发器")
	}
	query, args, err := tableprosql.TriggerDefinitionQuery(conn.Type, resolveTriggerSchema(conn, database, schema), trigger)
	if err != nil {
		return "", err
	}
	ctx, done := a.beginRun(connID)
	defer done()
	drv, err := a.sqlDriver(conn, database)
	if err != nil {
		return "", err
	}
	bound := make([]dbcore.Value, 0, len(args))
	for _, arg := range args {
		bound = append(bound, dbcore.ValueOf(arg))
	}
	res, err := drv.Execute(ctx, query, bound...)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("触发器 %s 不存在或已被删除", trigger)
	}
	return cellString(res.Rows[0][0]), nil
}

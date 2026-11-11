package explainplan

import (
	"strings"
	"testing"
)

// pgSample is a real PostgreSQL FORMAT JSON plan (a hash join over an index scan
// and a seq scan), trimmed to the fields the tree consumes.
const pgSample = `[
  {
    "Plan": {
      "Node Type": "Hash Join",
      "Join Type": "Inner",
      "Startup Cost": 1.15,
      "Total Cost": 35.50,
      "Plan Rows": 2550,
      "Hash Cond": "(o.customer_id = c.id)",
      "Plans": [
        {
          "Node Type": "Seq Scan",
          "Relation Name": "orders",
          "Alias": "o",
          "Startup Cost": 0.00,
          "Total Cost": 25.50,
          "Plan Rows": 1000,
          "Filter": "(amount > 10)"
        },
        {
          "Node Type": "Index Scan",
          "Relation Name": "customers",
          "Alias": "c",
          "Index Name": "customers_pkey",
          "Startup Cost": 0.15,
          "Total Cost": 1.15,
          "Plan Rows": 10
        }
      ]
    }
  }
]`

func TestParseJSONPostgres(t *testing.T) {
	plan, err := ParseJSON(pgSample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.Format != "json" || plan.Root == nil {
		t.Fatalf("plan = %+v", plan)
	}
	root := plan.Root
	if !strings.Contains(root.Label, "Hash Join") || !strings.Contains(root.Label, "Inner") {
		t.Fatalf("root label = %q", root.Label)
	}
	if root.Rows != 2550 {
		t.Fatalf("root rows = %d", root.Rows)
	}
	if len(root.Children) != 2 {
		t.Fatalf("children = %d", len(root.Children))
	}
	// The child labels name the relation and the index.
	if !strings.Contains(root.Children[0].Label, "orders") {
		t.Fatalf("child 0 = %q", root.Children[0].Label)
	}
	if !strings.Contains(root.Children[1].Label, "customers_pkey") {
		t.Fatalf("child 1 = %q", root.Children[1].Label)
	}
	// Depth is assigned by the parser so the renderer does not recompute it.
	if root.Depth != 0 || root.Children[0].Depth != 1 {
		t.Fatalf("depths = %d/%d", root.Depth, root.Children[0].Depth)
	}
	// A sequential scan is flagged: that is the one thing a plan viewer must point at.
	if root.Children[0].Warn != "全表扫描" {
		t.Fatalf("seq scan warn = %q", root.Children[0].Warn)
	}
	if plan.Nodes != 3 {
		t.Fatalf("nodes = %d", plan.Nodes)
	}
}

const mysqlSample = `{
  "query_block": {
    "select_id": 1,
    "cost_info": {"query_cost": "12.35"},
    "nested_loop": [
      {
        "table": {
          "table_name": "orders",
          "access_type": "ALL",
          "rows_examined_per_scan": 1000,
          "rows_produced_per_join": 100,
          "filtered": "10.00",
          "cost_info": {"read_cost": "1.00", "eval_cost": "10.00", "prefix_cost": "11.00"}
        }
      },
      {
        "table": {
          "table_name": "customers",
          "access_type": "eq_ref",
          "possible_keys": ["PRIMARY"],
          "key": "PRIMARY",
          "rows_examined_per_scan": 1,
          "filtered": "100.00",
          "attached_condition": "(shop.customers.id = shop.orders.customer_id)"
        }
      }
    ]
  }
}`

func TestParseJSONMySQL(t *testing.T) {
	plan, err := ParseJSON(mysqlSample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.Root == nil {
		t.Fatalf("no root: %+v", plan)
	}
	// The nested_loop array becomes a node with two table children.
	if len(plan.Root.Children) != 2 {
		t.Fatalf("children = %d (%q)", len(plan.Root.Children), plan.Root.Label)
	}
	first := plan.Root.Children[0]
	if !strings.Contains(first.Label, "orders") || !strings.Contains(first.Label, "ALL") {
		t.Fatalf("child 0 = %q", first.Label)
	}
	if first.Warn != "全表扫描" {
		t.Fatalf("access_type=ALL should be flagged, warn = %q", first.Warn)
	}
	if first.Rows != 1000 {
		t.Fatalf("rows = %d", first.Rows)
	}
	second := plan.Root.Children[1]
	if !strings.Contains(second.Label, "eq_ref") {
		t.Fatalf("child 1 = %q", second.Label)
	}
}

func TestParseJSONMySQLSingleTable(t *testing.T) {
	// A single-table plan has no nested_loop: query_block→table directly.
	const body = `{"query_block":{"select_id":1,"table":{"table_name":"t","access_type":"index","rows_examined_per_scan":5}}}`
	plan, err := ParseJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Root == nil || !strings.Contains(plan.Root.Label, "t (index)") {
		t.Fatalf("root = %+v", plan.Root)
	}
	if len(plan.Root.Children) != 0 {
		t.Fatalf("children = %d", len(plan.Root.Children))
	}
}

func TestParseJSONRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "not json", `{"unterminated": `} {
		if _, err := ParseJSON(bad); err == nil {
			t.Fatalf("%q should not parse", bad)
		}
	}
}

func TestParseJSONMySQLWithoutQueryBlockKeepsRaw(t *testing.T) {
	// A MySQL error document is not a plan; the raw text must still reach the
	// user rather than an empty tree.
	plan, err := ParseJSON(`{"error":"table not found"}`)
	if err != nil {
		t.Fatalf("an unexpected shape should not error: %v", err)
	}
	if plan.Root != nil {
		t.Fatalf("root = %+v, want none", plan.Root)
	}
	if plan.Note == "" || !strings.Contains(plan.Raw, "error") {
		t.Fatalf("plan = %+v", plan)
	}
}

// sqliteRows mirrors EXPLAIN QUERY PLAN's four columns.
func sqliteRows() ([]string, [][]string) {
	return []string{"id", "parent", "notused", "detail"}, [][]string{
		{"2", "0", "0", "SCAN orders"},
		{"8", "0", "0", "SEARCH customers USING INTEGER PRIMARY KEY (rowid=?)"},
		{"0", "0", "0", "SEARCH orders USING INDEX idx_customer (customer_id=?)"},
	}
}

func TestParseRowsSQLite(t *testing.T) {
	cols, rows := sqliteRows()
	plan, err := ParseRows(cols, rows)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.Root == nil {
		t.Fatalf("no root: %+v", plan)
	}
	if plan.Root.Rows != -1 {
		t.Fatalf("rows = %d, want -1 (unknown)", plan.Root.Rows)
	}
	// id 0 is the root (rowid lookup); the other two are its children.
	if !strings.Contains(plan.Root.Label, "SEARCH orders") {
		t.Fatalf("root label = %q", plan.Root.Label)
	}
	if len(plan.Root.Children) != 2 {
		t.Fatalf("children = %d", len(plan.Root.Children))
	}
	if plan.Nodes != 3 {
		t.Fatalf("nodes = %d", plan.Nodes)
	}
	// SCAN is a full scan and must be flagged.
	var scan *Node
	for i := range plan.Root.Children {
		if strings.HasPrefix(plan.Root.Children[i].Label, "SCAN") {
			scan = &plan.Root.Children[i]
		}
	}
	if scan == nil || scan.Warn != "全表扫描" {
		t.Fatalf("scan node = %+v", scan)
	}
}

func TestParseRowsKeepsOrphans(t *testing.T) {
	// A row whose parent is missing must become a root rather than vanish: a plan
	// viewer that silently drops a step is worse than one that shows it
	// unparented.
	cols := []string{"id", "parent", "notused", "detail"}
	rows := [][]string{
		{"1", "0", "0", "SCAN a"},
		{"2", "99", "0", "SCAN orphan"},
	}
	plan, err := ParseRows(cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Root == nil {
		t.Fatal("no root")
	}
	// Two roots → a synthetic parent so both survive.
	if plan.Nodes != 3 {
		t.Fatalf("nodes = %d, want 3 (two roots plus the wrapper)", plan.Nodes)
	}
}

func TestParseRowsWithoutDetailColumnKeepsRaw(t *testing.T) {
	plan, err := ParseRows([]string{"a", "b"}, [][]string{{"1", "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Root != nil || plan.Note == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if !strings.Contains(plan.Raw, "a | b") {
		t.Fatalf("raw = %q", plan.Raw)
	}
}

// mssqlSample is a SHOWPLAN_XML plan: a nested loop over a clustered index scan
// and an index seek.
const mssqlSample = `<?xml version="1.0" encoding="utf-16"?>
<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.2">
  <BatchSequence>
    <Batch>
      <Statements>
        <StmtSimple StatementText="SELECT * FROM orders o JOIN customers c ON ..." StatementId="1">
          <QueryPlan CachedPlanSize="32" CompileTime="1" CompileCPU="1">
            <RelOp NodeId="0" PhysicalOp="Nested Loops" LogicalOp="Inner Join" EstimateRows="100" EstimatedTotalSubtreeCost="0.35">
              <NestedLoops>
                <OuterReferences><ColumnReference Table="[shop].[dbo].[orders]" Column="customer_id"/></OuterReferences>
                <RelOp NodeId="1" PhysicalOp="Clustered Index Scan" LogicalOp="Clustered Index Scan" EstimateRows="1000" EstimatedTotalSubtreeCost="0.12" EstimateCPU="0.01">
                  <OutputList><ColumnReference Table="[shop].[dbo].[orders]" Column="id"/></OutputList>
                  <IndexScan Ordered="0" ForcedIndex="0">
                    <Object Database="[shop]" Schema="[dbo]" Table="[orders]" Index="[PK_orders]" Alias="[o]"/>
                  </IndexScan>
                </RelOp>
                <RelOp NodeId="2" PhysicalOp="Clustered Index Seek" LogicalOp="Clustered Index Seek" EstimateRows="1" EstimatedTotalSubtreeCost="0.003" EstimateCPU="0.0001">
                  <OutputList><ColumnReference Table="[shop].[dbo].[customers]" Column="name"/></OutputList>
                  <IndexScan Ordered="1" ForcedIndex="0">
                    <Object Database="[shop]" Schema="[dbo]" Table="[customers]" Index="[PK_customers]" Alias="[c]"/>
                  </IndexScan>
                </RelOp>
              </NestedLoops>
            </RelOp>
          </QueryPlan>
        </StmtSimple>
      </Statements>
    </Batch>
  </BatchSequence>
</ShowPlanXML>`

func TestParseXMLSQLServer(t *testing.T) {
	plan, err := ParseXML(mssqlSample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.Root == nil {
		t.Fatalf("no root: %+v", plan)
	}
	root := plan.Root
	if root.Label != "Nested Loops" {
		t.Fatalf("root = %q", root.Label)
	}
	if root.Rows != 100 {
		t.Fatalf("rows = %d", root.Rows)
	}
	if root.Cost <= 0 {
		t.Fatalf("cost = %v", root.Cost)
	}
	if len(root.Children) != 2 {
		t.Fatalf("children = %d (%+v)", len(root.Children), root.Children)
	}
	if root.Children[0].Label != "Clustered Index Scan" {
		t.Fatalf("child 0 = %q", root.Children[0].Label)
	}
	// A clustered index scan is a full scan.
	if root.Children[0].Warn != "全表扫描" {
		t.Fatalf("scan warn = %q", root.Children[0].Warn)
	}
	// Object names reach the detail line.
	if !strings.Contains(root.Children[0].Detail, "[orders]") {
		t.Fatalf("detail = %q", root.Children[0].Detail)
	}
	if plan.Nodes != 3 {
		t.Fatalf("nodes = %d", plan.Nodes)
	}
}

func TestParseXMLWithoutRelOpKeepsRaw(t *testing.T) {
	plan, err := ParseXML(`<ShowPlanXML><Nothing/></ShowPlanXML>`)
	if err != nil {
		t.Fatalf("an empty plan should not error: %v", err)
	}
	if plan.Root != nil || plan.Note == "" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestParseXMLRejectsMalformed(t *testing.T) {
	if _, err := ParseXML(`<ShowPlanXML><unclosed>`); err == nil {
		t.Fatal("malformed XML must error, not yield an empty tree")
	}
}

func TestTextPlanIsMarkedNotDrawable(t *testing.T) {
	// Oracle answers with DBMS_XPLAN prose; the caller keeps the grid, and the
	// note is what tells the UI not to draw anything.
	p := TextPlan("Plan hash value: 123\n| Id | Operation |")
	if p.Format != "text" || p.Root != nil || p.Note == "" {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(p.Raw, "Plan hash value") {
		t.Fatalf("raw = %q", p.Raw)
	}
}

// Package explainplan turns a machine-readable execution plan into a tree a
// renderer can draw.
//
// Every engine says the same thing in a different envelope, and the workspace's
// rule is that each envelope is parsed once, here, rather than in a client's
// render code:
//
//	PostgreSQL  EXPLAIN (FORMAT JSON)        -> [{"Plan": {...}}]
//	MySQL       EXPLAIN FORMAT=JSON          -> {"query_block": {...}}
//	SQLite      EXPLAIN QUERY PLAN           -> rows (id, parent, detail)
//	SQL Server  SET SHOWPLAN_XML ON          -> <ShowPlanXML><RelOp>…
//	Oracle      DBMS_XPLAN.DISPLAY           -> text only (no tree)
//
// The parsers are tolerant in one specific way and strict in another: an unknown
// field is skipped (engines add fields between versions, and a plan is a
// diagnostic, not a contract), but a payload that cannot be understood at all
// returns an error rather than an empty tree — "no plan" and "a plan I could not
// read" must not look the same to the user.
package explainplan

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Node is one step of a plan.
type Node struct {
	// Label is the operation, e.g. "Seq Scan on orders".
	Label string `json:"label"`
	// Detail is the engine's own numbers on one line, so the tree carries the
	// facts without the renderer formatting per engine.
	Detail string `json:"detail,omitempty"`
	// Cost is the estimated total cost when the engine reports one; 0 = unknown.
	Cost float64 `json:"cost,omitempty"`
	// Rows is the estimated row count; -1 = unknown.
	Rows int64 `json:"rows"`
	// Children are the operations feeding this one, in the engine's order.
	Children []Node `json:"children,omitempty"`
	// Depth is the node's level in the tree (filled by the parser). The renderer
	// uses it for layout, so a client never recomputes the tree's shape.
	Depth int `json:"depth"`
	// Warn is a one-line hint the renderer highlights (a full scan, a filesort).
	Warn string `json:"warn,omitempty"`
}

// Plan is one statement's parsed plan.
type Plan struct {
	// Root is the top node; nil when the payload had no tree.
	Root *Node `json:"root,omitempty"`
	// Format echoes the payload shape that was parsed.
	Format string `json:"format"`
	// Raw is the payload as text, for the "raw" view and for a plan the parser
	// could not structure.
	Raw string `json:"raw"`
	// Note explains why there is no tree, when there is none.
	Note string `json:"note,omitempty"`
	// Nodes counts the tree's nodes (0 when there is no tree).
	Nodes int `json:"nodes"`
}

// nodeGap is how a table scan is flagged: the single most useful thing a plan
// viewer can point out.
func scanWarn(label string) string {
	lower := strings.ToLower(label)
	switch {
	// SQLite says "SCAN t" (and "SCAN TABLE t"); PostgreSQL "Seq Scan on t";
	// MySQL marks access_type=ALL ("t (ALL)"); SQL Server "Clustered Index Scan".
	case strings.Contains(lower, "seq scan"), strings.Contains(lower, "table scan"),
		strings.Contains(lower, "index scan"), strings.Contains(lower, "full scan"),
		strings.HasPrefix(lower, "scan "), strings.Contains(lower, " (all)"):
		return "全表扫描"
	case strings.Contains(lower, "filesort"), strings.Contains(lower, "using temporary"):
		return "排序/临时表"
	}
	return ""
}

// ParseJSON parses PostgreSQL's or MySQL's JSON plan. The engine decides the
// shape, so one function handles both: a top-level array is PostgreSQL, a
// top-level object with "query_block" is MySQL.
func ParseJSON(raw string) (*Plan, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("计划为空")
	}
	plan := &Plan{Format: "json", Raw: raw}
	switch trimmed[0] {
	case '[':
		var doc []pgNode
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return nil, fmt.Errorf("解析 PostgreSQL 计划: %w", err)
		}
		if len(doc) == 0 {
			return nil, fmt.Errorf("计划数组为空")
		}
		root := pgToNode(doc[0].Plan, 0)
		plan.Root = &root
	case '{':
		var doc map[string]any
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return nil, fmt.Errorf("解析 MySQL 计划: %w", err)
		}
		block, ok := doc["query_block"]
		if !ok {
			// A MySQL error document (`{"error": ...}`) or an unexpected shape:
			// the raw text still tells the user something.
			plan.Note = "计划里没有 query_block"
			return plan, nil
		}
		root := mysqlWalk(block, 0)
		if root == nil {
			plan.Note = "计划里没有可识别的操作节点"
			return plan, nil
		}
		plan.Root = root
	default:
		return nil, fmt.Errorf("无法识别的 JSON 计划")
	}
	plan.Nodes = countNodes(plan.Root)
	if plan.Root != nil {
		plan.Root.Depth = 0
	}
	return plan, nil
}

// pgNode mirrors PostgreSQL's Plan object. Only the fields the tree uses are
// declared; everything else in the document is ignored.
type pgNode struct {
	Plan pgPlanBody `json:"Plan"`
}

// pgPlanBody is PostgreSQL's plan node body (the value under "Plan").
type pgPlanBody struct {
	NodeType     string   `json:"Node Type"`
	RelationName string   `json:"Relation Name"`
	Alias        string   `json:"Alias"`
	IndexName    string   `json:"Index Name"`
	TotalCost    float64  `json:"Total Cost"`
	StartupCost  float64  `json:"Startup Cost"`
	PlanRows     int64    `json:"Plan Rows"`
	ActualRows   int64    `json:"Actual Rows"`
	Filter       string   `json:"Filter"`
	HashCond     string   `json:"Hash Cond"`
	MergeCond    string   `json:"Merge Cond"`
	JoinType     string   `json:"Join Type"`
	SortKey      []string     `json:"Sort Key"`
	SubplanName  string       `json:"Subplan Name"`
	// Plans holds the child nodes. In PostgreSQL's FORMAT JSON only the TOP level
	// wraps its body in {"Plan": …}; every nested node under "Plans" is a bare
	// plan body. Declaring them as pgNode would parse each child as an empty
	// object — the tree would come back with the right shape and no labels.
	Plans []pgPlanBody `json:"Plans"`
}

// pgToNode converts one PostgreSQL plan node, recursing into its children.
func pgToNode(p pgPlanBody, depth int) Node {
	label := p.NodeType
	// An index scan's most useful identity is BOTH the relation and the index:
	// "Index Scan on customers (c)" leaves out which index the planner picked,
	// which is exactly the thing a user opens the plan to find out.
	if p.RelationName != "" {
		label += " on " + p.RelationName
		if p.Alias != "" && p.Alias != p.RelationName {
			label += " (" + p.Alias + ")"
		}
	}
	if p.IndexName != "" {
		label += " using " + p.IndexName
	}
	if p.JoinType != "" {
		label += " (" + p.JoinType + ")"
	}
	var details []string
	if p.TotalCost > 0 {
		details = append(details, fmt.Sprintf("cost=%.2f..%.2f", p.StartupCost, p.TotalCost))
	}
	switch {
	case p.ActualRows > 0:
		details = append(details, fmt.Sprintf("rows=%d (估算 %d)", p.ActualRows, p.PlanRows))
	case p.PlanRows > 0:
		details = append(details, fmt.Sprintf("rows=%d", p.PlanRows))
	}
	for _, cond := range []struct {
		name string
		val  string
	}{{"Filter", p.Filter}, {"Hash Cond", p.HashCond}, {"Merge Cond", p.MergeCond}} {
		if cond.val != "" {
			details = append(details, cond.name+": "+cond.val)
		}
	}
	if len(p.SortKey) > 0 {
		details = append(details, "Sort Key: "+strings.Join(p.SortKey, ", "))
	}
	node := Node{
		Label: label, Detail: strings.Join(details, " · "),
		Cost: p.TotalCost, Rows: p.PlanRows, Depth: depth,
		Warn: scanWarn(p.NodeType),
	}
	if p.ActualRows > 0 {
		node.Rows = p.ActualRows
	}
	for _, child := range p.Plans {
		node.Children = append(node.Children, pgToNode(child, depth+1))
	}
	return node
}

// mysqlWalk turns MySQL's nested-object document into a tree.
//
// MySQL has no fixed schema here: the nesting depends on the query (nested_loop
// arrays, ordering_operation wrappers, materialized subqueries…). Rather than
// encode every key, this walks the document and treats any object carrying a
// table_name as a leaf operation, any object carrying a cost_info as a join, and
// recurses through everything else. Unknown keys are skipped, which is what keeps
// it working across versions.
func mysqlWalk(v any, depth int) *Node {
	switch x := v.(type) {
	case map[string]any:
		// A table access is the leaf.
		if name, ok := x["table_name"].(string); ok && name != "" {
			return mysqlTableNode(x, name, depth)
		}
		// Known wrapper keys whose child IS the operation.
		for _, key := range []string{
			"ordering_operation", "grouping_operation", "duplicates_removal",
			"union_result", "nested_loop", "query_block", "materialized_from_subquery",
			"attached_subqueries", "optimized_away_subqueries",
		} {
			if child, ok := x[key]; ok {
				if n := mysqlWalk(child, depth); n != nil {
					return n
				}
			}
		}
		// Otherwise recurse into the first value that yields a node, so an
		// unknown wrapper still does not hide the plan.
		keys := sortedKeys(x)
		for _, key := range keys {
			if key == "cost_info" || key == "select_id" || key == "message" {
				continue
			}
			if n := mysqlWalk(x[key], depth); n != nil {
				return n
			}
		}
		return nil
	case []any:
		// nested_loop is an array of operations: a parent with one child each, or
		// a chain. Represent it as a sequence under one node.
		if len(x) == 0 {
			return nil
		}
		var children []*Node
		for _, item := range x {
			if n := mysqlWalk(item, depth+1); n != nil {
				children = append(children, n)
			}
		}
		if len(children) == 0 {
			return nil
		}
		if len(children) == 1 {
			return children[0]
		}
		node := &Node{Label: "nested loop", Depth: depth, Rows: -1, Warn: scanWarn("nested loop")}
		for _, c := range children {
			node.Children = append(node.Children, *c)
		}
		node.Detail = fmt.Sprintf("%d 个操作串联", len(children))
		return node
	}
	return nil
}

// mysqlTableNode renders one table access.
func mysqlTableNode(x map[string]any, name string, depth int) *Node {
	access := str(x["access_type"])
	label := name
	if access != "" {
		label += " (" + access + ")"
	}
	rows := int64Or(x["rows_examined_per_scan"], -1)
	var details []string
	if access != "" {
		details = append(details, "access="+access)
	}
	if rows >= 0 {
		details = append(details, fmt.Sprintf("rows=%d", rows))
	}
	if f := str(x["filtered"]); f != "" {
		details = append(details, "filtered="+f+"%")
	}
	if using := str(x["using_index"]); using != "" {
		details = append(details, "index="+using)
	}
	if cond := str(x["attached_condition"]); cond != "" {
		details = append(details, "cond: "+cond)
	}
	node := &Node{
		Label: label, Detail: strings.Join(details, " · "),
		Rows: rows, Depth: depth,
	}
	// MySQL flags a full scan as access_type=ALL, which is the one thing worth
	// pointing at.
	if strings.EqualFold(access, "ALL") {
		node.Warn = "全表扫描"
	}
	if child, ok := x["materialized_from_subquery"]; ok {
		if n := mysqlWalk(child, depth+1); n != nil {
			node.Children = append(node.Children, *n)
		}
	}
	return node
}

// ParseRows parses SQLite's EXPLAIN QUERY PLAN result set. Each row is
// (id, parent, notused, detail); the tree is the parent links.
//
// The result reaches this function as rows of strings because that is how the
// generic statement path hands back any result set.
func ParseRows(columns []string, rows [][]string) (*Plan, error) {
	plan := &Plan{Format: "rows", Raw: rowsToText(columns, rows)}
	if len(rows) == 0 {
		return nil, fmt.Errorf("计划没有行")
	}
	idAt, parentAt, detailAt := -1, -1, -1
	for i, c := range columns {
		switch strings.ToLower(c) {
		case "id":
			idAt = i
		case "parent":
			parentAt = i
		case "detail":
			detailAt = i
		}
	}
	if detailAt < 0 {
		// Not SQLite's four-column shape: keep the text, say why.
		plan.Note = "结果集里没有 detail 列"
		return plan, nil
	}
	type row struct {
		id, parent int64
		detail     string
	}
	parsed := make([]row, 0, len(rows))
	byID := map[int64]*Node{}
	for _, r := range rows {
		item := row{id: -1, parent: -1}
		if idAt >= 0 && idAt < len(r) {
			item.id = int64Or(r[idAt], -1)
		}
		if parentAt >= 0 && parentAt < len(r) {
			item.parent = int64Or(r[parentAt], 0)
		}
		if detailAt < len(r) {
			item.detail = r[detailAt]
		}
		parsed = append(parsed, item)
		byID[item.id] = &Node{
			Label: item.detail, Rows: -1,
			Warn: scanWarn(item.detail),
		}
	}
	// Link children to parents. A row whose parent is missing becomes a root:
	// dropping it would silently hide part of the plan.
	var roots []*Node
	for _, item := range parsed {
		node := byID[item.id]
		parent, ok := byID[item.parent]
		switch {
		case item.parent == item.id || !ok:
			roots = append(roots, node)
		default:
			parent.Children = append(parent.Children, *node)
		}
	}
	if len(roots) == 0 {
		plan.Note = "计划没有根节点"
		return plan, nil
	}
	// One root is the normal case (SQLite's id 0). Several means the parent
	// links were incomplete, so wrap them under a synthetic root rather than
	// picking one and losing the rest.
	root := roots[0]
	if len(roots) > 1 {
		root = &Node{Label: "计划", Rows: -1}
		for _, r := range roots {
			root.Children = append(root.Children, *r)
		}
	}
	setDepth(root, 0)
	plan.Root = root
	plan.Nodes = countNodes(root)
	return plan, nil
}

// ParseXML parses SQL Server's SHOWPLAN_XML document into a tree of RelOp nodes.
//
// The document is walked token by token rather than unmarshalled into structs:
// RelOp nests under query-specific wrappers (NestedLoops, IndexScan, …) whose
// names change per plan, and a decoder walk survives that where a fixed struct
// would not.
// pnode is the builder's pointer node. The public Node holds children by value,
// which means anything appended to a node AFTER it was attached to its parent
// (SQL Server puts the scanned table in a child <Object> element, parsed after
// the RelOp opened) lands on a copy the tree no longer sees. Building with
// pointers and converting at the end is what makes those late details survive.
type pnode struct {
	node Node
	kids []*pnode
}

// toNode converts the pointer tree into the public value tree, assigning depth
// on the way down.
func toNode(p *pnode, depth int) Node {
	n := p.node
	n.Depth = depth
	n.Children = make([]Node, 0, len(p.kids))
	for _, kid := range p.kids {
		n.Children = append(n.Children, toNode(kid, depth+1))
	}
	return n
}

func ParseXML(raw string) (*Plan, error) {
	plan := &Plan{Format: "xml", Raw: raw}
	dec := xml.NewDecoder(strings.NewReader(raw))
	// SQL Server's SHOWPLAN_XML declares encoding="utf-16" because that is how it
	// writes the document over the wire. The payload reaching this function is
	// already a Go string (decoded by the driver), so the declared charset is
	// stale: pass the bytes through rather than failing on a charset Go has no
	// reader for. Failing here would make every MSSQL plan unrenderable.
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	type frame struct {
		node *pnode
	}
	var stack []frame
	var root *pnode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析 SHOWPLAN_XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "RelOp" {
				// The scan's table lives in a child <Object Table="[orders]">,
				// not on the RelOp: capture it for the enclosing node, or the
				// label would say "Clustered Index Scan" and leave out WHAT it
				// scans.
				if t.Name.Local == "Object" {
					for _, a := range t.Attr {
						if a.Name.Local != "Table" || a.Value == "" {
							continue
						}
						for i := len(stack) - 1; i >= 0; i-- {
							if stack[i].node != nil {
								if stack[i].node.node.Detail != "" {
									stack[i].node.node.Detail = a.Value + " · " + stack[i].node.node.Detail
								} else {
									stack[i].node.node.Detail = a.Value
								}
								break
							}
						}
					}
				}
				// A wrapper element: remember nothing, keep walking.
				stack = append(stack, frame{})
				continue
			}
			attrs := make(map[string]string, len(t.Attr))
			for _, a := range t.Attr {
				attrs[a.Name.Local] = a.Value
			}
			label := attrs["PhysicalOp"]
			if label == "" {
				label = attrs["LogicalOp"]
			}
			if label == "" {
				label = "RelOp"
			}
			cur := &pnode{node: Node{Label: label, Rows: int64Or(attrs["EstimateRows"], -1)}}
			if cost := floatOr(attrs["EstimatedTotalSubtreeCost"], 0); cost > 0 {
				cur.node.Cost = cost
			}
			var details []string
			if cost := attrs["EstimatedTotalSubtreeCost"]; cost != "" {
				details = append(details, "cost="+shortCost(cost))
			}
			if rows := attrs["EstimateRows"]; rows != "" {
				details = append(details, "rows="+shortCost(rows))
			}
			if cpus := attrs["EstimateCPU"]; cpus != "" && cpus != "0" {
				details = append(details, "cpu="+shortCost(cpus))
			}
			cur.node.Detail = strings.Join(details, " · ")
			cur.node.Warn = scanWarn(label)
			// Attach to the nearest enclosing RelOp on the stack.
			attached := false
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].node != nil {
					stack[i].node.kids = append(stack[i].node.kids, cur)
					attached = true
					break
				}
			}
			if !attached {
				if root == nil {
					root = cur
				} else {
					// A second top-level RelOp: keep it rather than losing it.
					wrapper := &pnode{node: Node{Label: "计划", Rows: -1}}
					wrapper.kids = append(wrapper.kids, root, cur)
					root = wrapper
				}
			}
			stack = append(stack, frame{node: cur})
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil {
		plan.Note = "SHOWPLAN_XML 里没有 RelOp 节点"
		return plan, nil
	}
	converted := toNode(root, 0)
	plan.Root = &converted
	plan.Nodes = countNodes(plan.Root)
	return plan, nil
}

// TextPlan is the fallback for an engine that only offers prose (Oracle): the
// payload is kept verbatim and marked as not drawable.
func TextPlan(raw string) *Plan {
	return &Plan{Format: "text", Raw: raw, Note: "该引擎只提供文本计划"}
}

// ---- helpers ----

func countNodes(n *Node) int {
	if n == nil {
		return 0
	}
	total := 1
	for i := range n.Children {
		total += countNodes(&n.Children[i])
	}
	return total
}

// setDepth assigns each node's level, so the renderer never recomputes it.
func setDepth(n *Node, depth int) {
	n.Depth = depth
	for i := range n.Children {
		setDepth(&n.Children[i], depth+1)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// str reads a string field, ignoring non-strings (MySQL uses numbers and
// strings for the same fields across versions).
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.6f", x), "0"), ".")
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return ""
}

// int64Or reads an integer from either a number or a numeric string.
func int64Or(v any, fallback int64) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		var n int64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%d", &n); err == nil {
			return n
		}
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
	}
	return fallback
}

func floatOr(v any, fallback float64) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%g", &f); err == nil {
			return f
		}
	}
	return fallback
}

// shortCost trims a numeric string to something that fits a node label.
func shortCost(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// rowsToText renders a result-set plan into the raw view's text.
func rowsToText(columns []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString(strings.Join(columns, " | "))
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, " | "))
		b.WriteString("\n")
	}
	return b.String()
}

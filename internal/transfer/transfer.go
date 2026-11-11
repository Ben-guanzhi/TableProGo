// Package transfer plans a whole-table copy from one connection to another.
//
// It is the one piece W5 adds to the workspace: the schema rules already live in
// dbcore/schemadiff, the DDL text in dbcore/ddl, the row writing in the existing
// import pipeline and the reads in BrowseRows. What none of those own is the
// ORDER, and that is what this package is for — a table whose foreign keys point
// at a table not yet created cannot be built, and a table whose keys point at one
// not yet loaded cannot be populated.
//
// The plan is a value, not an action: Plan() reads both catalogs and returns the
// ordered work plus every reason some of it cannot be done. Running it is the
// caller's job, statement by statement, so a failure stops at a known place.
package transfer

import (
	"fmt"
	"sort"
	"strings"
)

// Table is one table to copy: its source name, its target name, and the foreign
// keys that constrain when it can be created and loaded.
type Table struct {
	Source string
	// Target is the name it gets on the other side; equal to Source unless the
	// caller renames (a schema-qualified source moving into a flat database).
	Target string
	// FKs are the tables this one references, by TARGET name, after filtering to
	// the tables in this plan. A reference to a table outside the set imposes no
	// order (nothing in this run creates or loads it).
	FKs []string
	// Rows is the approximate source row count, so the UI can show the size
	// before anyone commits to it. -1 when unknown.
	Rows int64
}

// Options narrows what the plan does.
type Options struct {
	// IgnoreForeignKeys drops FK constraints from the plan: tables are then
	// ordered by name and no constraint is created. Needed when the target
	// engine cannot express them, and useful when the caller will add them
	// later in one pass.
	IgnoreForeignKeys bool
	// SkipData copies structure only.
	SkipData bool
}

// Cycle is a group of tables that reference each other. Their CREATE order
// cannot satisfy every constraint at once, so the plan splits their work: create
// them WITHOUT foreign keys, load the rows, then add the constraints in a final
// pass. That is the standard resolution and it is the only one that keeps the
// data copy a single pass.
type Cycle struct {
	// Tables are the members, in name order for determinism.
	Tables []string
}

// Step is one unit of work, in execution order.
type Step struct {
	// Kind is what the step does: "create", "rows", "fk".
	Kind string
	Table string
	// DependsOn names the steps that must have completed first (table names,
	// not step ids, since one table contributes several steps).
	DependsOn []string
	// Deferred is set on a "fk" step: the constraint this table owns was held
	// back because of a cycle and is added after every row step.
	Deferred bool
}

// Plan is the ordered work plus everything the caller must know before running
// it.
type Plan struct {
	Steps []Step
	// CreateOrder is the table order for CREATE (FK topological).
	CreateOrder []string
	// LoadOrder is the table order for row loading (same order: parents first).
	LoadOrder []string
	// Cycles lists the groups that needed deferred constraints. Empty is the
	// common case.
	Cycles []Cycle
	// Warnings are non-fatal problems, each naming the table it concerns.
	Warnings []string
	// Errors are reasons the plan cannot run at all (an unknown dependency, a
	// table listed twice).
	Errors []string
}

// PlanTables orders the copy of one schema's tables.
//
// The algorithm is Kahn's topological sort with a name tiebreak, which makes the
// output deterministic — a plan that reorders itself between two runs of the same
// input is untestable and unnerving. A cycle is detected by the residue of the
// sort, exactly as Kahn's algorithm leaves it.
//
// Correctness argument for the order: after the sort, every edge (child →
// parent) points from a parent that appears earlier, so creating in that order
// never references a missing table and loading in that order never inserts a row
// whose parent row is absent. For a cycle no such order exists (that is what the
// cycle IS), which is why those tables move their FK work to the end instead.
func PlanTables(tables []Table, opts Options) (Plan, error) {
	var plan Plan
	if len(tables) == 0 {
		return plan, nil
	}

	// Index by target name, and reject duplicates: two tables writing the same
	// target name would silently overwrite each other's rows.
	index := make(map[string]int, len(tables))
	for i, t := range tables {
		if t.Target == "" {
			plan.Errors = append(plan.Errors, fmt.Sprintf("表 %s 缺少目标名称", t.Source))
			continue
		}
		if prev, dup := index[t.Target]; dup {
			plan.Errors = append(plan.Errors, fmt.Sprintf(
				"目标表名重复: %s（来自 %s 与 %s）", t.Target, tables[prev].Source, t.Source))
			continue
		}
		index[t.Target] = i
	}
	if len(plan.Errors) > 0 {
		return plan, fmt.Errorf("传输计划有 %d 个错误", len(plan.Errors))
	}

	// Normalize edges: only references to tables in THIS plan impose an order,
	// and drop self-references unless the table is genuinely cyclic (a table
	// referencing itself can be created, it just cannot be loaded in one pass
	// unless the FK is deferred).
	parents := make(map[string][]string, len(tables))
	children := make(map[string][]string, len(tables))
	selfRef := map[string]bool{}
	for _, t := range tables {
		var deps []string
		seen := map[string]bool{}
		for _, fk := range t.FKs {
			if opts.IgnoreForeignKeys {
				break
			}
			if fk == t.Target {
				selfRef[t.Target] = true
				continue
			}
			if _, in := index[fk]; !in {
				// A parent outside the plan: nothing here creates or loads it,
				// so it cannot constrain the order. Worth a word in the report
				// — the target may reject the constraint.
				plan.Warnings = append(plan.Warnings,
					fmt.Sprintf("%s 引用了不在本次传输范围内的表 %s（其约束可能因目标缺失而失败）", t.Target, fk))
				continue
			}
			if seen[fk] {
				continue
			}
			seen[fk] = true
			deps = append(deps, fk)
		}
		sort.Strings(deps)
		parents[t.Target] = deps
		for _, p := range deps {
			children[p] = append(children[p], t.Target)
		}
	}

	// Kahn's sort: repeatedly emit every table whose parents are all emitted,
	// taking the smallest name each round so the order is a function of the
	// input and nothing else.
	emitted := map[string]bool{}
	var order []string
	for len(order) < len(tables) {
		var ready []string
		for _, t := range tables {
			if emitted[t.Target] {
				continue
			}
			allDone := true
			for _, p := range parents[t.Target] {
				if !emitted[p] {
					allDone = false
					break
				}
			}
			if allDone {
				ready = append(ready, t.Target)
			}
		}
		if len(ready) == 0 {
			break // the residue is exactly one or more cycles
		}
		sort.Strings(ready)
		for _, name := range ready {
			emitted[name] = true
			order = append(order, name)
		}
	}

	// Whatever the sort could not emit sits on a cycle (or below one). Break the
	// cycle by emitting its members in name order: their constraints are the
	// deferred ones, so the order among them does not matter for creation.
	var remaining []string
	for _, t := range tables {
		if !emitted[t.Target] {
			remaining = append(remaining, t.Target)
		}
	}
	if len(remaining) > 0 {
		sort.Strings(remaining)
		plan.Cycles = append(plan.Cycles, Cycle{Tables: append([]string(nil), remaining...)})
		for _, name := range remaining {
			emitted[name] = true
			order = append(order, name)
		}
	}
	// A table that references itself is a cycle of one, even when nothing else
	// does: its FK must be deferred for the row load to succeed.
	for name := range selfRef {
		plan.Cycles = append(plan.Cycles, Cycle{Tables: []string{name}})
	}

	plan.CreateOrder = append([]string(nil), order...)
	plan.LoadOrder = append([]string(nil), order...)

	// Steps. One "create" and (unless data is skipped) one "rows" per table, in
	// the same positional order; the deferred "fk" steps go last so every row
	// exists before any constraint that could reject them is added.
	deferred := map[string]bool{}
	for _, c := range plan.Cycles {
		for _, name := range c.Tables {
			deferred[name] = true
		}
	}
	for _, name := range order {
		deps := parents[name]
		// A cycle member's parents may not have run yet; its constraint is
		// deferred instead, so drop the dependency that would deadlock the run.
		if deferred[name] {
			deps = nil
		}
		plan.Steps = append(plan.Steps, Step{Kind: "create", Table: name, DependsOn: deps})
	}
	if !opts.SkipData {
		for _, name := range order {
			plan.Steps = append(plan.Steps, Step{Kind: "rows", Table: name,
				DependsOn: []string{name}})
		}
	}
	if !opts.IgnoreForeignKeys {
		for _, c := range plan.Cycles {
			for _, name := range c.Tables {
				plan.Steps = append(plan.Steps, Step{
					Kind: "fk", Table: name, Deferred: true,
					// Every row step precedes this, and the constraint itself
					// needs the table to exist.
					DependsOn: []string{name},
				})
			}
		}
	}

	sortWarnings(&plan.Warnings)
	return plan, nil
}

// sortWarnings makes the report deterministic.
func sortWarnings(warnings *[]string) {
	sort.Strings(*warnings)
}

// ---- cross-engine downgrade report ----

// TypeLoss is one column whose source type has no faithful counterpart on the
// target engine. It is reported rather than silently coerced: a decimal that
// becomes a float is data loss the user must choose, not a copy detail.
type TypeLoss struct {
	Table    string
	Column   string
	Source   string
	Target   string
	Reason   string
}

// typeFamilies groups type spellings by what they can hold, which is what a
// cross-engine copy actually needs to compare. Being conservative here is the
// point: an unknown type is reported, not assumed compatible.
var typeFamilies = []struct {
	family string
	words  []string
}{
	// Order matters: a longer/more specific word must be tried before a shorter
	// one that is its prefix ("tinyint" before "int", "datetime" before "date").
	{"bool", []string{"boolean", "bool", "bit"}},
	{"tinyint", []string{"tinyint"}},
	{"smallint", []string{"smallint", "int2"}},
	{"int", []string{"bigint", "int8", "integer", "int4", "int", "mediumint"}},
	{"decimal", []string{"numeric", "decimal", "number", "money", "smallmoney"}},
	{"float", []string{"double", "float8", "real", "float4", "float"}},
	{"datetime", []string{"timestamp", "datetime2", "datetime", "smalldatetime", "timestamptz"}},
	{"date", []string{"date"}},
	{"time", []string{"time"}},
	{"text", []string{"text", "clob", "ntext", "longtext", "mediumtext", "tinytext"}},
	{"string", []string{"varchar", "nvarchar", "varchar2", "nvarchar2", "char", "nchar", "character", "string"}},
	{"binary", []string{"blob", "bytea", "varbinary", "binary", "image", "raw", "longblob", "mediumblob"}},
	{"json", []string{"json", "jsonb"}},
	{"uuid", []string{"uuid", "uniqueidentifier"}},
	{"geometry", []string{"geometry", "geography", "point", "polygon"}},
}

// familyOf classifies a column type by its base word, ignoring length and any
// parameter list.
func familyOf(raw string) string {
	t := strings.ToLower(strings.TrimSpace(raw))
	// Cut at the first delimiter: "varchar(64)" → "varchar",
	// "timestamp without time zone" → "timestamp".
	if i := strings.IndexAny(t, "( "); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	for _, f := range typeFamilies {
		for _, w := range f.words {
			if t == w {
				return f.family
			}
		}
	}
	return ""
}

// sameFamily reports whether two types can hold each other's values without the
// caller deciding. Integers widen (int → bigint) but never narrow silently, and
// decimal ↔ float never qualifies: that is the loss the report exists for.
func sameFamily(source, target string) (ok bool, reason string) {
	if source == "" || target == "" {
		return false, "类型无法识别，需人工确认"
	}
	if source == target {
		return true, ""
	}
	// Integers may widen in one direction; the caller copies source → target,
	// so a target that holds more is fine.
	if widenTargetOf(source) == target {
		return true, ""
	}
	// text/string are interchangeable for copying purposes (both hold text).
	if (source == "text" && target == "string") || (source == "string" && target == "text") {
		return true, ""
	}
	if (source == "datetime" && target == "date") || (source == "date" && target == "datetime") {
		return false, "日期与时间戳之间会丢精度或补零点"
	}
	return false, fmt.Sprintf("目标引擎没有等价的 %s 类型", source)
}

// widenTargetOf names the wider family a narrower integer can move into.
func widenTargetOf(family string) string {
	switch family {
	case "bool", "tinyint":
		return "smallint"
	case "smallint":
		return "int"
	}
	return ""
}

// Downgrades reports every column whose type is not faithfully representable on
// the target engine.
//
// Both sides are given as (table, column, type) triples because the caller has
// one snapshot per side and the tables may be named differently. A column that
// exists only on the source is reported too: it has nowhere to go.
func Downgrades(source, target []ColumnType) []TypeLoss {
	targetIndex := make(map[string]string, len(target))
	for _, c := range target {
		targetIndex[c.Table+"\x00"+c.Column] = c.Type
	}
	var losses []TypeLoss
	for _, c := range source {
		tt, ok := targetIndex[c.Table+"\x00"+c.Column]
		if !ok {
			losses = append(losses, TypeLoss{
				Table: c.Table, Column: c.Column, Source: c.Type,
				Reason: "目标表没有这一列（列映射需补齐，否则数据被丢弃）",
			})
			continue
		}
		src, dst := familyOf(c.Type), familyOf(tt)
		if ok, reason := sameFamily(src, dst); ok {
			continue
		} else {
			losses = append(losses, TypeLoss{
				Table: c.Table, Column: c.Column,
				Source: c.Type, Target: tt, Reason: reason,
			})
		}
	}
	return losses
}

// ColumnType is one column of one side of a cross-engine comparison.
type ColumnType struct {
	Table  string
	Column string
	Type   string
}

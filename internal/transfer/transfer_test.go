package transfer

import (
	"strings"
	"testing"
)

// names is the test shorthand for a create order.
func names(steps []Step, kind string) []string {
	var out []string
	for _, s := range steps {
		if s.Kind == kind {
			out = append(out, s.Table)
		}
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPlanEmptyIsEmpty(t *testing.T) {
	p, err := PlanTables(nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 0 || len(p.Errors) != 0 {
		t.Fatalf("got %+v", p)
	}
}

func TestPlanOrdersParentsBeforeChildren(t *testing.T) {
	// orders references customers; customers must be created (and loaded) first.
	p, err := PlanTables([]Table{
		{Source: "orders", Target: "orders", FKs: []string{"customers"}},
		{Source: "customers", Target: "customers"},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CreateOrder; !eq(got, []string{"customers", "orders"}) {
		t.Fatalf("create order = %v", got)
	}
	if got := p.LoadOrder; !eq(got, []string{"customers", "orders"}) {
		t.Fatalf("load order = %v", got)
	}
	if len(p.Cycles) != 0 {
		t.Fatalf("no cycle expected: %+v", p.Cycles)
	}
	// Every dependency a step names must appear before it.
	assertDependenciesPrecede(t, p.Steps)
}

func TestPlanOrderIsDeterministic(t *testing.T) {
	// Independent tables are ordered by name, so two runs of the same input
	// cannot disagree — a plan that reorders itself is untestable.
	tables := []Table{
		{Source: "b", Target: "b"}, {Source: "a", Target: "a"}, {Source: "c", Target: "c"},
	}
	first, _ := PlanTables(tables, Options{})
	for i := 0; i < 5; i++ {
		again, _ := PlanTables(tables, Options{})
		if !eq(first.CreateOrder, again.CreateOrder) {
			t.Fatalf("order changed between runs: %v vs %v", first.CreateOrder, again.CreateOrder)
		}
	}
	if !eq(first.CreateOrder, []string{"a", "b", "c"}) {
		t.Fatalf("create order = %v", first.CreateOrder)
	}
}

func TestPlanHandlesCycleWithDeferredConstraints(t *testing.T) {
	// a → b → a: no order satisfies both, so the constraints move to the end.
	p, err := PlanTables([]Table{
		{Source: "a", Target: "a", FKs: []string{"b"}},
		{Source: "b", Target: "b", FKs: []string{"a"}},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Cycles) != 1 || len(p.Cycles[0].Tables) != 2 {
		t.Fatalf("cycles = %+v", p.Cycles)
	}
	// Both tables are created and loaded; the constraint steps come last.
	if got := names(p.Steps, "create"); len(got) != 2 {
		t.Fatalf("create steps = %v", got)
	}
	if got := names(p.Steps, "rows"); len(got) != 2 {
		t.Fatalf("rows steps = %v", got)
	}
	fks := names(p.Steps, "fk")
	if len(fks) != 2 {
		t.Fatalf("fk steps = %v, want one per cycle member", fks)
	}
	// Every fk step must come after every rows step: constraints are added only
	// once all data is in, or the second table's rows would be rejected.
	lastRows, firstFK := -1, len(p.Steps)
	for i, s := range p.Steps {
		if s.Kind == "rows" {
			lastRows = i
		}
		if s.Kind == "fk" && i < firstFK {
			firstFK = i
		}
	}
	if firstFK < lastRows {
		t.Fatalf("an fk step (%d) precedes a rows step (%d)", firstFK, lastRows)
	}
}

func TestPlanSelfReferenceIsACycleOfOne(t *testing.T) {
	// A table referencing itself can be created, but its rows cannot all load
	// with the constraint live — so it defers too.
	p, _ := PlanTables([]Table{
		{Source: "emp", Target: "emp", FKs: []string{"emp"}},
	}, Options{})
	if len(p.Cycles) != 1 || !eq(p.Cycles[0].Tables, []string{"emp"}) {
		t.Fatalf("cycles = %+v", p.Cycles)
	}
	if got := names(p.Steps, "fk"); !eq(got, []string{"emp"}) {
		t.Fatalf("fk steps = %v", got)
	}
}

func TestPlanIgnoresReferencesOutsideTheSet(t *testing.T) {
	// A parent not in the plan cannot order anything, but it is worth a warning:
	// the target may reject the constraint for a missing table.
	p, _ := PlanTables([]Table{
		{Source: "child", Target: "child", FKs: []string{"external"}},
	}, Options{})
	if len(p.Cycles) != 0 {
		t.Fatalf("an outside reference is not a cycle: %+v", p.Cycles)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "external") {
		t.Fatalf("warnings = %v, want one naming the outside table", p.Warnings)
	}
}

func TestPlanIgnoreForeignKeysDropsOrderingAndConstraints(t *testing.T) {
	p, _ := PlanTables([]Table{
		{Source: "orders", Target: "orders", FKs: []string{"customers"}},
		{Source: "customers", Target: "customers"},
	}, Options{IgnoreForeignKeys: true})
	// No ordering is imposed, so the name tiebreak decides.
	if !eq(p.CreateOrder, []string{"customers", "orders"}) {
		t.Fatalf("create order = %v", p.CreateOrder)
	}
	if len(names(p.Steps, "fk")) != 0 {
		t.Fatal("IgnoreForeignKeys must emit no fk steps")
	}
}

func TestPlanSkipDataOmitsRowSteps(t *testing.T) {
	p, _ := PlanTables([]Table{{Source: "a", Target: "a"}}, Options{SkipData: true})
	if len(names(p.Steps, "rows")) != 0 {
		t.Fatal("SkipData must emit no rows steps")
	}
	if len(names(p.Steps, "create")) != 1 {
		t.Fatal("SkipData still creates the table")
	}
}

func TestPlanRejectsDuplicateTargetNames(t *testing.T) {
	// Two sources writing one target would silently overwrite each other.
	_, err := PlanTables([]Table{
		{Source: "a", Target: "same"},
		{Source: "b", Target: "same"},
	}, Options{})
	if err == nil {
		t.Fatal("duplicate target names must be refused")
	}
}

func TestPlanRejectsEmptyTargetName(t *testing.T) {
	if _, err := PlanTables([]Table{{Source: "a"}}, Options{}); err == nil {
		t.Fatal("an empty target name must be refused")
	}
}

func TestPlanDiamondDependency(t *testing.T) {
	// d references b and c; both reference a. a first, then b/c, then d.
	p, _ := PlanTables([]Table{
		{Source: "d", Target: "d", FKs: []string{"b", "c"}},
		{Source: "b", Target: "b", FKs: []string{"a"}},
		{Source: "c", Target: "c", FKs: []string{"a"}},
		{Source: "a", Target: "a"},
	}, Options{})
	if !eq(p.CreateOrder, []string{"a", "b", "c", "d"}) {
		t.Fatalf("create order = %v", p.CreateOrder)
	}
	assertDependenciesPrecede(t, p.Steps)
}

// assertDependenciesPrecede checks the plan's own contract: no step runs before a
// table it depends on has been created.
func assertDependenciesPrecede(t *testing.T, steps []Step) {
	t.Helper()
	created := map[string]bool{}
	handled := map[string]bool{}
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			switch s.Kind {
			case "create":
				// For a create step the dependency means "that table's create
				// ran first".
				if dep != s.Table && !created[dep] {
					t.Fatalf("%s created before its dependency %s", s.Table, dep)
				}
			case "rows":
				if !created[dep] {
					t.Fatalf("rows for %s before %s was created", s.Table, dep)
				}
			case "fk":
				if !created[dep] || !handled[dep] {
					t.Fatalf("fk for %s before %s was created and loaded", s.Table, dep)
				}
			}
		}
		if s.Kind == "create" {
			created[s.Table] = true
		}
		if s.Kind == "rows" {
			handled[s.Table] = true
		}
	}
}

// ---- type downgrade report ----

func TestFamilyOf(t *testing.T) {
	cases := map[string]string{
		"varchar(64)":                  "string",
		"character varying(255)":       "string",
		"timestamp without time zone":  "datetime",
		"timestamptz":                  "datetime",
		"numeric(10,2)":                "decimal",
		"bigint":                       "int",
		"tinyint(1)":                   "tinyint",
		"bytea":                        "binary",
		"double precision":             "float",
		"jsonb":                        "json",
		"bool":                         "bool",
		"uniqueidentifier":             "uuid",
		"weird_custom_type":            "",
	}
	for raw, want := range cases {
		if got := familyOf(raw); got != want {
			t.Fatalf("familyOf(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestDowngradesReportsDecimalToFloat(t *testing.T) {
	// The loss this report exists for: a DECIMAL copied into a float column
	// silently rounds. It must be surfaced, not coerced.
	losses := Downgrades(
		[]ColumnType{{Table: "t", Column: "amount", Type: "numeric(10,2)"}},
		[]ColumnType{{Table: "t", Column: "amount", Type: "double precision"}},
	)
	if len(losses) != 1 {
		t.Fatalf("losses = %+v", losses)
	}
	if !strings.Contains(losses[0].Reason, "等价") {
		t.Fatalf("reason = %q", losses[0].Reason)
	}
}

func TestDowngradesAllowsIntegerWidening(t *testing.T) {
	losses := Downgrades(
		[]ColumnType{{Table: "t", Column: "n", Type: "smallint"}},
		[]ColumnType{{Table: "t", Column: "n", Type: "bigint"}},
	)
	if len(losses) != 0 {
		t.Fatalf("widening should be silent: %+v", losses)
	}
}

func TestDowngradesReportsMissingColumn(t *testing.T) {
	losses := Downgrades(
		[]ColumnType{{Table: "t", Column: "extra", Type: "text"}},
		[]ColumnType{{Table: "t", Column: "other", Type: "text"}},
	)
	if len(losses) != 1 || !strings.Contains(losses[0].Reason, "没有这一列") {
		t.Fatalf("losses = %+v", losses)
	}
}

func TestDowngradesIgnoresIdenticalTypes(t *testing.T) {
	// Same engine, same type: the common case must be silent.
	cols := []ColumnType{
		{Table: "t", Column: "a", Type: "varchar(64)"},
		{Table: "t", Column: "b", Type: "integer"},
		{Table: "t", Column: "c", Type: "timestamp"},
	}
	if losses := Downgrades(cols, cols); len(losses) != 0 {
		t.Fatalf("identical schemas must report no loss: %+v", losses)
	}
}

func TestDowngradesReportsUnknownType(t *testing.T) {
	// An unknown type is reported rather than assumed compatible: guessing is
	// how a "successful" copy loses data.
	losses := Downgrades(
		[]ColumnType{{Table: "t", Column: "x", Type: "custom_thing"}},
		[]ColumnType{{Table: "t", Column: "x", Type: "custom_thing"}},
	)
	if len(losses) != 1 {
		t.Fatalf("an unknown type must be reported: %+v", losses)
	}
	if !strings.Contains(losses[0].Reason, "无法识别") {
		t.Fatalf("reason = %q", losses[0].Reason)
	}
}

func TestDowngradesTextAndStringAreInterchangeable(t *testing.T) {
	losses := Downgrades(
		[]ColumnType{{Table: "t", Column: "c", Type: "text"}},
		[]ColumnType{{Table: "t", Column: "c", Type: "nvarchar(max)"}},
	)
	if len(losses) != 0 {
		t.Fatalf("text → nvarchar should be silent: %+v", losses)
	}
}

package importfile

import (
	"reflect"
	"testing"
)

func pairs(ms []ColumnMap) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.File + "->" + m.Table
	}
	return out
}

func TestMatchColumnsExactAndFold(t *testing.T) {
	got := MatchColumns(
		[]string{"id", "Name", "EMAIL"},
		[]string{"id", "name", "email"},
		true,
	)
	want := []string{"id->id", "Name->name", "EMAIL->email"}
	if !reflect.DeepEqual(pairs(got), want) {
		t.Fatalf("pairs = %v, want %v", pairs(got), want)
	}
}

func TestMatchColumnsNormalizedSeparators(t *testing.T) {
	got := MatchColumns(
		[]string{"Order No", "first-name", "OrderNo"},
		[]string{"order_no", "firstName", "note"},
		true,
	)
	// "Order No" and "OrderNo" both normalize to orderno; the first one in
	// file order claims order_no, the later one stays unmatched rather than
	// displacing it.
	want := []string{"Order No->order_no", "first-name->firstName", "OrderNo->"}
	if !reflect.DeepEqual(pairs(got), want) {
		t.Fatalf("pairs = %v, want %v", pairs(got), want)
	}
}

func TestMatchColumnsTierPreference(t *testing.T) {
	// A case-insensitive name beats a normalized one: email maps email, and
	// the normalized fallback only runs for what is left over.
	got := MatchColumns(
		[]string{"Email", "e_mail"},
		[]string{"email", "e mail"},
		true,
	)
	want := []string{"Email->email", "e_mail->e mail"}
	if !reflect.DeepEqual(pairs(got), want) {
		t.Fatalf("pairs = %v, want %v", pairs(got), want)
	}
}

func TestMatchColumnsExtraAndMissing(t *testing.T) {
	got := MatchColumns(
		[]string{"id", "junk", "name"},
		[]string{"id", "name", "created_at"},
		true,
	)
	want := []string{"id->id", "junk->", "name->name"}
	if !reflect.DeepEqual(pairs(got), want) {
		t.Fatalf("pairs = %v, want %v", pairs(got), want)
	}
}

func TestMatchColumnsHeaderless(t *testing.T) {
	positional := MatchColumns([]string{"column1", "column2"}, []string{"a", "b"}, false)
	want := []string{"column1->a", "column2->b"}
	if !reflect.DeepEqual(pairs(positional), want) {
		t.Fatalf("positional pairs = %v, want %v", pairs(positional), want)
	}
	// Arity disagreement: no identity the file can offer, nothing matches.
	none := MatchColumns([]string{"column1", "column2", "column3"}, []string{"a", "b"}, false)
	want = []string{"column1->", "column2->", "column3->"}
	if !reflect.DeepEqual(pairs(none), want) {
		t.Fatalf("arity-mismatch pairs = %v, want %v", pairs(none), want)
	}
}

func TestMatchColumnsNonASCIIHeadersVerbatim(t *testing.T) {
	got := MatchColumns([]string{"订单号", "金额"}, []string{"订单号", "金额"}, true)
	want := []string{"订单号->订单号", "金额->金额"}
	if !reflect.DeepEqual(pairs(got), want) {
		t.Fatalf("pairs = %v, want %v", pairs(got), want)
	}
}

// ── K-F1: the fuzzy suggestion tier ────────────────────────────────────────

func suggestions(ms []ColumnMap) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.File + "~>" + m.Suggested
	}
	return out
}

// The canonical drift: cust_name → customer_name is four normalized edits
// (insertion 'o', insertion 'r', substitution 's'→'o', deletion of the 'e'
// before '_'), beyond the threshold of three — so no match is suggested.
func TestSuggestFuzzyFourEditDrift(t *testing.T) {
	got := MatchColumns(
		[]string{"id", "cust_name"},
		[]string{"id", "customer_name"},
		true,
	)
	want := []string{"id->id", "cust_name->"}
	if !reflect.DeepEqual(pairs(got), want) {
		t.Fatalf("pairs = %v, want %v", pairs(got), want)
	}
	wantS := []string{"id~>", "cust_name~>customer_name"}
	if !reflect.DeepEqual(suggestions(got), wantS) {
		t.Fatalf("suggestions = %v, want %v", suggestions(got), wantS)
	}
}

// Suggestions never touch the matched columns' Table field, and a proposal
// never lands on a column an exact match already claimed.
func TestSuggestFuzzyNeverOverridesLiteralTiers(t *testing.T) {
	got := MatchColumns(
		[]string{"id", "status", "stat"},
		[]string{"id", "status", "stat_us"},
		true,
	)
	// "stat" is unmatched after the three tiers ("status" was taken by the
	// exact match), and proposes its nearest free neighbor.
	want := []string{"id->id", "status->status", "stat->"}
	if !reflect.DeepEqual(pairs(got), want) {
		t.Fatalf("pairs = %v, want %v", pairs(got), want)
	}
	for _, m := range got {
		if m.Table != "" && m.Suggested != "" {
			t.Errorf("column %q is both matched (%q) and suggested (%q)", m.File, m.Table, m.Suggested)
		}
	}
}

// Function property under acceptance: every suggestion is distinct and no
// suggestion re-proposes a mapped column, so accepting them all keeps the
// mapping a function.
func TestSuggestFuzzySuggestionsAreDistinct(t *testing.T) {
	got := MatchColumns(
		[]string{"a1", "a2", "a3"},
		[]string{"a11", "a12", "a13"},
		true,
	)
	seen := map[string]string{}
	for _, m := range got {
		if m.Table != "" {
			seen[m.Table] = m.File
			continue
		}
		if m.Suggested == "" {
			t.Errorf("column %q got no suggestion against near names", m.File)
			continue
		}
		if prev, dup := seen[m.Suggested]; dup {
			t.Errorf("suggestion %q proposed for both %q and %q", m.Suggested, prev, m.File)
		}
		seen[m.Suggested] = m.File
	}
}

// Distance 5 ("completely_unrelated_name" vs "x" is far) stays silent, and a
// distance-4 name ("junk" vs "name") DOES propose — that is the threshold's
// trade: the customer_name recall costs proposals the user may decline.
// Suggestions are confirm-first, so a declined proposal costs one click.
func TestSuggestFuzzyThresholdBoundary(t *testing.T) {
	far := MatchColumns([]string{"completely_unrelated_name"}, []string{"x"}, true)
	if far[0].Suggested != "" || far[0].Table != "" {
		t.Errorf("distant column landed %+v; want unmatched and unsuggested", far[0])
	}
	edge := MatchColumns([]string{"id", "junk"}, []string{"id", "name"}, true)
	if edge[1].Table != "" {
		t.Errorf("junk matched %q; want unmatched", edge[1].Table)
	}
	if edge[1].Suggested != "name" {
		t.Errorf("junk suggestion = %q; want name (distance 4 is within the threshold)", edge[1].Suggested)
	}
}

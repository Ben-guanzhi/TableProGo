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

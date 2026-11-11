package importfile

import (
	"slices"
	"testing"
)

func TestValidateColumnMap(t *testing.T) {
	cols := []string{"id", "name", "score"}
	tests := []struct {
		name    string
		specs   []ColumnMapSpec
		wantErr bool
	}{
		{"empty mapping is not authoritative", nil, false},
		{"exact rename", []ColumnMapSpec{{Source: "id", Target: "person_id"}}, false},
		{"skip a column", []ColumnMapSpec{{Source: "id", Target: "id"}, {Source: "name", Target: ""}}, false},
		{"unknown source", []ColumnMapSpec{{Source: "nope", Target: "id"}}, true},
		{"duplicate target", []ColumnMapSpec{{Source: "id", Target: "x"}, {Source: "name", Target: "x"}}, true},
		{"all skipped", []ColumnMapSpec{{Source: "id", Target: ""}, {Source: "name", Target: ""}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateColumnMap(tt.specs, cols)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateColumnMap(%v) err = %v, wantErr = %v", tt.specs, err, tt.wantErr)
			}
		})
	}
}

func TestBuildColumnMapIsAuthoritativeAndKeepsFileOrder(t *testing.T) {
	cols := []string{"id", "name", "score", "extra"}
	// Only two of the four file columns are named, so the others are skipped;
	// the targets come back in file order, not in the order the specs are written.
	got, keep := BuildColumnMap([]ColumnMapSpec{
		{Source: "score", Target: "score"},
		{Source: "id", Target: "person_id"},
	}, cols)
	if !slices.Equal(got, []string{"person_id", "score"}) {
		t.Fatalf("cols = %v", got)
	}
	if !slices.Equal(keep, []int{0, 2}) {
		t.Fatalf("keep = %v", keep)
	}
}

func TestMappedColumnsReportsSkips(t *testing.T) {
	cols := []string{"id", "name"}
	got := MappedColumns([]ColumnMapSpec{{Source: "id", Target: "id"}, {Source: "name", Target: ""}}, cols)
	if len(got) != 2 || got[0].Table != "id" || got[1].Table != "" {
		t.Fatalf("MappedColumns = %+v", got)
	}
}

func TestDropSourcesAndKeptHelpers(t *testing.T) {
	cols := []string{"id", "name", "score"}
	mapping := MatchColumns(cols, []string{"id", "name", "score"}, true)
	DropSources(mapping, []string{"name"})
	if len(KeptTargets(mapping)) != 2 {
		t.Fatalf("kept targets = %v", KeptTargets(mapping))
	}
	if !slices.Equal(KeptIndexes(mapping), []int{0, 2}) {
		t.Fatalf("kept indexes = %v", KeptIndexes(mapping))
	}
}

func TestSanitizeSheetName(t *testing.T) {
	tests := map[string]string{
		"Sheet1":       "sheet1",
		"Staff Roster": "staff_roster",
		"Q1/Q2":        "q1_q2",
		"  Leads ":     "leads",
		"员工 名单":        "sheet",
		"":             "sheet",
		"a---b":        "a_b",
		"123":          "123",
	}
	for in, want := range tests {
		if got := SanitizeSheetName(in); got != want {
			t.Errorf("SanitizeSheetName(%q) = %q, want %q", in, got, want)
		}
	}
}

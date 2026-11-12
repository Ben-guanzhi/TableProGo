package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"tablepro-go/internal/mcpcompact"
)

// sampleResult is the shape a SELECT produces: a few typed columns and rows that
// carry a NULL and an empty string, which are the two cells a compact format can
// easily confuse.
func sampleResult() *QueryResult {
	return &QueryResult{
		Columns: []string{"id", "name", "qty", "note"},
		Rows: [][]interface{}{
			{float64(1), "alice", float64(10), nil},
			{float64(2), "bob", float64(20), ""},
			{float64(3), "carol", float64(30), "ok"},
		},
		RowCount: 3,
	}
}

func TestCompactIsSmallerThanJSON(t *testing.T) {
	// The whole point of the format: the same information for fewer bytes. If this
	// ever stops holding, the format has no reason to exist.
	result := sampleResult()
	jsonText, err := marshalJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	compact := mcpcompact.Render(mcpcompact.Result{
		Columns: result.Columns, Rows: result.Rows, Truncated: result.Truncated,
	})
	if len(compact) >= len(jsonText) {
		t.Fatalf("compact (%d bytes) is not smaller than json (%d bytes)\ncompact:\n%s\njson:\n%s",
			len(compact), len(jsonText), compact, jsonText)
	}
	// A third is the rough expectation for tabular data with repeated keys; assert
	// a real reduction rather than a single byte.
	if float64(len(compact)) > 0.7*float64(len(jsonText)) {
		t.Fatalf("compaction is weak: %d vs %d bytes", len(compact), len(jsonText))
	}
}

func TestCompactKeepsStructureRecoverable(t *testing.T) {
	// Size alone would be satisfied by dropping data. The block must still carry
	// the column names, the row count, NULL vs empty, and truncation.
	result := sampleResult()
	result.Truncated = true
	result.Masked = true
	text := mcpcompact.Render(mcpcompact.Result{
		Columns: result.Columns, Rows: result.Rows,
		Truncated: result.Truncated, Masked: result.Masked,
	})
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "# rows=3") {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "id\tname\tqty\tnote") {
		t.Fatalf("header = %q", lines[1])
	}
	// NULL and empty must be distinguishable, or a reader cannot tell a missing
	// value from a blank one.
	if !strings.Contains(text, "\tNULL") {
		t.Fatalf("NULL is not spelled out:\n%s", text)
	}
	if !strings.Contains(text, `""`) {
		t.Fatalf("an empty string is not distinguishable from NULL:\n%s", text)
	}
	if !strings.Contains(text, "# truncated") || !strings.Contains(text, "# masked") {
		t.Fatalf("the flags are missing:\n%s", text)
	}
}

func TestCompactEscapesStructureBreakingCharacters(t *testing.T) {
	// A value holding a tab or a newline would otherwise fabricate columns and
	// rows. The escape is what makes "split on tab" safe for a consumer.
	result := &QueryResult{
		Columns: []string{"a", "b"},
		Rows: [][]interface{}{
			{"has\ttab", "has\nnewline"},
			{`has\backslash`, "plain"},
		},
		RowCount: 2,
	}
	text := mcpcompact.Render(mcpcompact.Result{Columns: result.Columns, Rows: result.Rows})
	if strings.Contains(text, "has\ttab") {
		t.Fatalf("a tab in a value was not escaped:\n%s", text)
	}
	if strings.Count(text, "\n") != 4 { // rows= line + header + 2 data rows
		t.Fatalf("a newline in a value broke the row structure:\n%s", text)
	}
	if !strings.Contains(text, `has\tbackslash`) == !strings.Contains(text, `has\\backslash`) {
		t.Fatalf("backslash escaping is ambiguous:\n%s", text)
	}
}

func TestRenderResultRejectsUnknownFormat(t *testing.T) {
	// A typo must be a tool error the model can see, not a silent fallback to
	// JSON: a client asking for compact and getting JSON may blow its budget.
	if _, err := renderResult(sampleResult(), "toon"); err == nil {
		t.Fatal("an unknown format must be refused")
	}
}

func TestRenderResultDefaultsToJSON(t *testing.T) {
	result := sampleResult()
	out, err := renderResult(result, "")
	if err != nil {
		t.Fatal(err)
	}
	var decoded QueryResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("the default output is not JSON: %v\n%s", err, out)
	}
	if len(decoded.Rows) != len(result.Rows) {
		t.Fatalf("decoded rows = %d", len(decoded.Rows))
	}
}

func TestQueryToolAdvertisesFormat(t *testing.T) {
	// The parameter has to be discoverable, or no client will ever send it.
	s := New(&fakeCatalog{}, Options{})
	seen := 0
	for _, tool := range s.tools() {
		if tool.Name != "query" && tool.Name != "sample_table" {
			continue
		}
		// InputSchema is interface{} by design (it mirrors the protocol's own
		// loose typing); marshal it to inspect the advertised property.
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s schema decode: %v", tool.Name, err)
		}
		if _, ok := schema.Properties["format"]; !ok {
			t.Fatalf("%s does not advertise format: %s", tool.Name, raw)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("expected both query tools to advertise format, saw %d", seen)
	}
}

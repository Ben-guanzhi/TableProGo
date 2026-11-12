package main

import (
	"encoding/json"
	"testing"
)

// W13: the proposal card reads lowercase keys; the wire must send them.
func TestProposalWireShapeIsLowercase(t *testing.T) {
	p := Proposal{ID: "prop-1", Connection: "c1", Database: "d", SQL: "SELECT 1", Reason: "r"}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "connection", "database", "sql", "reason"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("wire is missing %q: %s", key, raw)
		}
	}
	if _, ok := decoded["ID"]; ok {
		t.Fatalf("wire still carries the Go field name: %s", raw)
	}
}

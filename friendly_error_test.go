package main

import (
	"errors"
	"strings"
	"testing"
)

func TestFriendlyDBError(t *testing.T) {
	if got := friendlyDBError(errors.New("some odd engine wording")); got != "some odd engine wording" {
		t.Fatalf("unclassified errors pass through verbatim, got %q", got)
	}
	got := friendlyDBError(errors.New("Error 1062: Duplicate entry 'a' for key 'uk'"))
	if !strings.Contains(got, "唯一键冲突") || !strings.Contains(got, "Error 1062") {
		t.Fatalf("hint missing: %q", got)
	}
	if got := friendlyDBError(nil); got != "" {
		t.Fatalf("nil → %q", got)
	}
}

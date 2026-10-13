package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/TableProGo/tableproxlsx"
)

// The rendering itself is covered in tableproxlsx, which is where it lives. This
// covers only what this API wrapper adds on top: the refusals it states in its
// own words, the row cap the shared exporter enforces, and the fact that what
// comes back is a file a browser can save rather than JSON text.
func TestWorkbookBase64Guardrails(t *testing.T) {
	app := &App{}
	one := []tableproxlsx.Column{{Name: "id", Type: "INTEGER"}}
	rows := [][]interface{}{{float64(1)}}

	if _, err := app.WorkbookBase64(nil, rows); err == nil ||
		!strings.Contains(err.Error(), "没有列") {
		t.Errorf("want a refusal without columns, got: %v", err)
	}
	if _, err := app.WorkbookBase64(one, nil); err == nil ||
		!strings.Contains(err.Error(), "没有可导出的行") {
		t.Errorf("want a refusal for an empty result, got: %v", err)
	}

	// The cap is checked inside the shared exporter, so this proves the wrapper
	// passes an oversized result through to it instead of swallowing the error.
	oversized := make([][]interface{}, tableproxlsx.MaxRows+1)
	for i := range oversized {
		oversized[i] = []interface{}{i}
	}
	if _, err := app.WorkbookBase64(one, oversized); err == nil ||
		!strings.Contains(err.Error(), "一次最多导出") {
		t.Errorf("want a refusal above the cap, got: %v", err)
	}

	encoded, err := app.WorkbookBase64(one, rows)
	if err != nil {
		t.Fatalf("workbook: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// An xlsx file is a zip archive; the signature is what lets the browser save
	// bytes the client cannot inspect.
	if len(data) < 4 || string(data[:2]) != "PK" {
		t.Fatalf("want zip bytes, got %q", data[:min(4, len(data))])
	}
}

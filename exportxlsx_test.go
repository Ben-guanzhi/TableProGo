package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/TableProGo/tableproxlsx"
)

// The rendering itself is covered in tableproxlsx, which is where it lives. This
// covers only what this API wrapper adds on top: the refusals it states in its
// own words, the row cap the shared exporter enforces, and the fact that what
// comes back is a file a browser can save rather than JSON text.
func TestWorkbookToGuardrails(t *testing.T) {
	app := &App{}
	one := []tableproxlsx.Column{{Name: "id", Type: "INTEGER"}}
	rows := [][]interface{}{{float64(1)}}

	if err := app.WorkbookTo(&bytes.Buffer{}, nil, rows); err == nil ||
		!strings.Contains(err.Error(), "没有列") {
		t.Errorf("want a refusal without columns, got: %v", err)
	}
	if err := app.WorkbookTo(&bytes.Buffer{}, one, nil); err == nil ||
		!strings.Contains(err.Error(), "没有可导出的行") {
		t.Errorf("want a refusal for an empty result, got: %v", err)
	}

	// The cap is checked inside the shared exporter, so this proves the wrapper
	// passes an oversized result through to it instead of swallowing the error.
	oversized := make([][]interface{}, tableproxlsx.MaxRows+1)
	for i := range oversized {
		oversized[i] = []interface{}{i}
	}
	if err := app.WorkbookTo(&bytes.Buffer{}, one, oversized); err == nil ||
		!strings.Contains(err.Error(), "一次最多导出") {
		t.Errorf("want a refusal above the cap, got: %v", err)
	}

	// The file streams straight into the writer: the response body is the
	// workbook, so there is no base64 step left to decode here.
	var out bytes.Buffer
	if err := app.WorkbookTo(&out, one, rows); err != nil {
		t.Fatalf("workbook: %v", err)
	}
	// An xlsx file is a zip archive; the signature is what lets the browser save
	// bytes the client cannot inspect.
	if data := out.Bytes(); len(data) < 4 || string(data[:2]) != "PK" {
		t.Fatalf("want zip bytes, got %q", data[:min(4, len(data))])
	}
}

// A refusal must not leave a partial attachment: the handler renders into a temp
// file first, and only a successful render is copied to the response.
func TestWorkbookToRefusalWritesNothing(t *testing.T) {
	app := &App{}
	var out bytes.Buffer
	if err := app.WorkbookTo(&out, nil, [][]interface{}{{1}}); err == nil {
		t.Fatal("expected a refusal")
	}
	if out.Len() != 0 {
		t.Fatalf("a refused export wrote %d bytes", out.Len())
	}
}

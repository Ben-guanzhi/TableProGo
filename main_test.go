package main

import (
	"os"
	"testing"
)

// The startup command line is the binary's only user-facing contract for
// scripts (MCP client configs, CI): the accepted spellings are pinned here.
func TestParseFlags(t *testing.T) {
	orig := os.Args
	defer func() { os.Args = orig }()

	cases := []struct {
		name  string
		args  []string
		mcp   bool
		port  int
		config string
	}{
		{"plain", []string{"tablepro-go"}, false, 0, ""},
		{"mcp short", []string{"tablepro-go", "-mcp"}, true, 0, ""},
		{"mcp long", []string{"tablepro-go", "--mcp"}, true, 0, ""},
		{"mcp value true", []string{"tablepro-go", "--mcp=true"}, true, 0, ""},
		{"mcp value 1", []string{"tablepro-go", "--mcp=1"}, true, 0, ""},
		{"mcp false spelling", []string{"tablepro-go", "--mcp=false"}, false, 0, ""},
		{"port and config", []string{"tablepro-go", "--port=9911", "--config=/tmp/c.json"}, false, 9911, "/tmp/c.json"},
		{"port spelled out", []string{"tablepro-go", "-port", "9912"}, false, 9912, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = tc.args
			opts, err := parseFlags()
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if opts.mcp != tc.mcp || opts.apiPort != tc.port || opts.configPath != tc.config {
				t.Fatalf("opts = %+v, want mcp=%v port=%d config=%q", opts, tc.mcp, tc.port, tc.config)
			}
		})
	}

	// An unparsable value is an error, not a silent default.
	os.Args = []string{"tablepro-go", "--port=notanumber"}
	if _, err := parseFlags(); err == nil {
		t.Fatal("--port=notanumber must fail")
	}
	os.Args = []string{"tablepro-go", "--port=99999"}
	if _, err := parseFlags(); err == nil {
		t.Fatal("--port=99999 must fail")
	}
	os.Args = []string{"tablepro-go", "--nonsense"}
	if _, err := parseFlags(); err == nil {
		t.Fatal("--nonsense must fail")
	}
}

package main

// W-A1's "单测锁死绕不过": the HTTP surface has no execute path for NL→SQL. A
// request that asks for one is REFUSED with a message saying so — not silently
// downgraded, because a client that believes it ran something is worse than one
// that got an error.

import (
	"net/http"
	"strings"
	"testing"
)

func TestAINL2SQLRefusesExecute(t *testing.T) {
	app := NewApp()
	mux := app.routes()

	body := `{"action":"nl2sql","session":"x","question":"删掉所有订单","execute":true}`
	rec := postJSON(t, mux, "/api/workbench/ai", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "预览") {
		t.Fatalf("the refusal must say it is preview-only: %s", rec.Body.String())
	}
}

func TestAIMCPToolsGateIsListable(t *testing.T) {
	// With no plugin loaded the tool table is empty — and that is a valid answer,
	// not an error: the capability is simply absent.
	app := NewApp()
	mux := app.routes()
	rec := postJSON(t, mux, "/api/workbench/mcptools", `{"action":"list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tools") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

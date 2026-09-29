package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The API fronts saved database connections; the CORS grant must stay inside
// the machine. A wildcard would let any page the user browses read responses
// and run SQL.

func TestCorsGrantsOnlyLocalOrigins(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	granted := []string{
		"http://127.0.0.1:5173", // vite dev server
		"http://localhost:1420",
		"http://[::1]:5173",
	}
	for _, origin := range granted {
		req := httptest.NewRequest(http.MethodPost, "/api/execute", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("origin %s: grant = %q, want the origin echoed", origin, got)
		}
	}

	refused := []string{
		"https://evil.example",
		"http://127.0.0.1.evil.example",
		"",
	}
	for _, origin := range refused {
		req := httptest.NewRequest(http.MethodPost, "/api/execute", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q must not be granted, got %q", origin, got)
		}
	}
}

func TestCorsPreflightShortCircuits(t *testing.T) {
	reached := false
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/execute", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if reached {
		t.Error("preflight must not reach the API handler")
	}
	if rec.Code != 204 {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
}

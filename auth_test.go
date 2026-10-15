package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The server binds 127.0.0.1:0, so the port is unpredictable, but any page on
// another localhost port could still POST to /api/execute. The per-process
// token is what stops that; CORS alone only keeps the response away.

func TestAuthMiddlewareRequiresTheProcessToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	handler := authMiddleware("s3cret-token", inner)

	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "something-else", http.StatusUnauthorized},
		{"right", "s3cret-token", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/execute", nil)
		if tc.token != "" {
			req.Header.Set(apiTokenHeader, tc.token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s token: got %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

// The browser preflights a custom header with OPTIONS and cannot attach the
// token to it, so a 401 here would break the very first call.
func TestAuthMiddlewareLetsPreflightThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})
	handler := authMiddleware("s3cret-token", inner)
	req := httptest.NewRequest(http.MethodOptions, "/api/execute", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight: got %d, want 204", rec.Code)
	}
}

// The token header is not CORS-safelisted, so the grant must name it or the
// preflight answer is useless.
func TestCorsGrantsTheTokenHeader(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/execute", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	allowed := rec.Header().Get("Access-Control-Allow-Headers")
	hasToken := false
	for _, item := range strings.Split(allowed, ",") {
		if strings.TrimSpace(item) == apiTokenHeader {
			hasToken = true
		}
	}
	if !hasToken {
		t.Errorf("Access-Control-Allow-Headers = %q, want it to include %s", allowed, apiTokenHeader)
	}
}

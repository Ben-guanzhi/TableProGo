package main

// W16: server-mode auth, exercised over real HTTP.
//
// A login gate is only worth what its worst path is worth, so these drive the actual
// middleware and handlers with a real listener: no session → 401, session → the
// allowlisted route, a mutating route → 403 while writes are off, a replayed TOTP code
// → 401, an expired session → 401.

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"tablepro-go/internal/totp"
)

// serverTestApp builds an app with a passcode and a temp home.
func serverTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TABLEPRO_HOME", dir)
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: nil,
		settings:         AppSettings{SafeMode: "off", ResultLimit: 1000},
		passcodePath:     filepath.Join(dir, "passcode.json"),
	}
	if err := app.SetPasscode("hunter2!"); err != nil {
		t.Fatalf("passcode: %v", err)
	}
	return app
}

// serveAuth starts the API behind the server-mode middleware and returns its base URL
// plus a client that does NOT follow redirects (so a 401 is visible).
func serveAuth(t *testing.T, app *App, sa *serverAuth) (*http.Client, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		app.handleLogin(sa, w, r)
	})
	mux.HandleFunc("/api/logout", func(w http.ResponseWriter, r *http.Request) {
		app.handleLogout(sa, w, r)
	})
	mux.Handle("/api/connections", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"connections": []any{}})
	}))
	mux.Handle("/api/backup/run", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ran": true})
	}))
	srv := &http.Server{Handler: app.serverAuthMiddleware(sa, mux)}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &http.Client{Timeout: 5 * time.Second}, "http://" + listener.Addr().String()
}

func newTestServerAuth(app *App, allowWrites bool, now func() time.Time) *serverAuth {
	return &serverAuth{
		enabled:     true,
		app:         app,
		sessions:    newSessionStore(),
		allowWrites: allowWrites,
		now:         now,
	}
}

func postJSONCookie(t *testing.T, client *http.Client, url string, body any, cookie *http.Cookie) (int, map[string]any, *http.Cookie) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	var set *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			set = c
		}
	}
	return resp.StatusCode, decoded, set
}

func TestServerAuthRequiresLoginForAPI(t *testing.T) {
	app := serverTestApp(t)
	sa := newTestServerAuth(app, false, time.Now)
	client, base := serveAuth(t, app, sa)

	// No cookie: the API refuses.
	code, _, _ := postJSONCookie(t, client, base+"/api/connections", nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", code)
	}

	// Wrong password is 401 and sets no cookie.
	code, body, cookie := postJSONCookie(t, client, base+"/api/login", map[string]string{"password": "nope"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", code)
	}
	if cookie != nil {
		t.Fatal("a failed login must not set a session cookie")
	}
	_ = body

	// The right password logs in, and the cookie works.
	code, body, cookie = postJSONCookie(t, client, base+"/api/login", map[string]string{"password": "hunter2!"}, nil)
	if code != http.StatusOK || cookie == nil {
		t.Fatalf("login = %d cookie=%v body=%v", code, cookie, body)
	}
	if !cookie.HttpOnly {
		t.Fatal("the session cookie must be HttpOnly")
	}
	if code, _, _ := postJSONCookie(t, client, base+"/api/connections", nil, cookie); code != http.StatusOK {
		t.Fatalf("authenticated read = %d", code)
	}
}

func TestServerAuthReadOnlyBlocksMutatingRoutes(t *testing.T) {
	app := serverTestApp(t)
	sa := newTestServerAuth(app, false, time.Now)
	client, base := serveAuth(t, app, sa)
	_, _, cookie := postJSONCookie(t, client, base+"/api/login", map[string]string{"password": "hunter2!"}, nil)

	// A mutating route is refused while writes are off — even with a valid session,
	// and even though the handler exists.
	code, body, _ := postJSONCookie(t, client, base+"/api/backup/run", nil, cookie)
	if code != http.StatusForbidden {
		t.Fatalf("mutating route = %d, want 403 (%v)", code, body)
	}
	if msg, _ := body["error"].(string); !contains(msg, "只读") {
		t.Fatalf("the refusal should say it is read-only: %v", body)
	}

	// Turning writes on publishes the same route.
	sa.allowWrites = true
	if code, _, _ := postJSONCookie(t, client, base+"/api/backup/run", nil, cookie); code != http.StatusOK {
		t.Fatalf("with writes on = %d", code)
	}
}

func TestServerAuthLogoutAndExpiry(t *testing.T) {
	app := serverTestApp(t)
	now := time.Now()
	clock := func() time.Time { return now }
	sa := newTestServerAuth(app, false, clock)
	client, base := serveAuth(t, app, sa)
	_, _, cookie := postJSONCookie(t, client, base+"/api/login", map[string]string{"password": "hunter2!"}, nil)

	// Idle expiry: past the idle timeout the session is gone.
	now = now.Add(sessionIdleTimeout + time.Minute)
	if code, _, _ := postJSONCookie(t, client, base+"/api/connections", nil, cookie); code != http.StatusUnauthorized {
		t.Fatalf("idle-expired session = %d, want 401", code)
	}
	// And the cookie cannot be resumed by waiting.
	if code, _, _ := postJSONCookie(t, client, base+"/api/connections", nil, cookie); code != http.StatusUnauthorized {
		t.Fatalf("an expired session must stay expired = %d", code)
	}

	// A fresh login works, and logout drops it immediately.
	_, _, fresh := postJSONCookie(t, client, base+"/api/login", map[string]string{"password": "hunter2!"}, nil)
	if code, _, _ := postJSONCookie(t, client, base+"/api/connections", nil, fresh); code != http.StatusOK {
		t.Fatalf("fresh session = %d", code)
	}
	if code, _, _ := postJSONCookie(t, client, base+"/api/logout", nil, fresh); code != http.StatusOK {
		t.Fatalf("logout = %d", code)
	}
	if code, _, _ := postJSONCookie(t, client, base+"/api/connections", nil, fresh); code != http.StatusUnauthorized {
		t.Fatalf("after logout = %d, want 401", code)
	}
}

func TestServerAuthTOTPSecondFactor(t *testing.T) {
	app := serverTestApp(t)
	// Enrol a secret through the real path.
	if _, err := app.EnrolTOTP("hunter2!", "alice"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	secret, err := app.totpSecret()
	if err != nil || secret.Empty() {
		t.Fatalf("secret: %v", err)
	}

	now := time.Unix(1_700_000_000, 0)
	sa := newTestServerAuth(app, false, func() time.Time { return now })
	client, base := serveAuth(t, app, sa)

	// Password alone is not enough: the server asks for the code rather than
	// rejecting, so the UI can show the field.
	code, body, cookie := postJSONCookie(t, client, base+"/api/login", map[string]string{"password": "hunter2!"}, nil)
	if code != http.StatusOK || body["needsTotp"] != true || cookie != nil {
		t.Fatalf("without code: status=%d body=%v cookie=%v", code, body, cookie)
	}

	// Wrong code: 401.
	if code, _, _ := postJSONCookie(t, client, base+"/api/login",
		map[string]string{"password": "hunter2!", "totp": "000000"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong code = %d", code)
	}

	// Right code: logged in.
	good := secret.Code(now)
	code, body, cookie = postJSONCookie(t, client, base+"/api/login",
		map[string]string{"password": "hunter2!", "totp": good}, nil)
	if code != http.StatusOK || cookie == nil {
		t.Fatalf("with code: status=%d body=%v", code, body)
	}

	// The same code cannot be used again, even in its own validity window.
	if code, _, _ := postJSONCookie(t, client, base+"/api/login",
		map[string]string{"password": "hunter2!", "totp": good}, nil); code != http.StatusUnauthorized {
		t.Fatalf("replayed code = %d, want 401", code)
	}

	// The next step's code works.
	now = now.Add(totp.Period * time.Second)
	next := secret.Code(now)
	if code, _, _ := postJSONCookie(t, client, base+"/api/login",
		map[string]string{"password": "hunter2!", "totp": next}, nil); code != http.StatusOK {
		t.Fatalf("next step code = %d", code)
	}
}

func TestServerAuthIgnoresTheTokenHeader(t *testing.T) {
	// Server mode must not accept the desktop token: it travels in a page, so anyone
	// who loaded the login page once would hold it.
	app := serverTestApp(t)
	sa := newTestServerAuth(app, false, time.Now)
	client, base := serveAuth(t, app, sa)
	req, _ := http.NewRequest(http.MethodPost, base+"/api/connections", nil)
	req.Header.Set(apiTokenHeader, app.apiToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token header in server mode = %d, want 401", resp.StatusCode)
	}
}

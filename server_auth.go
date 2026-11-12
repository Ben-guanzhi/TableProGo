package main

// W16: the server form's authentication.
//
// The desktop app authenticates with a token the page receives from the Wails host, so
// "who is calling" is answered by the process boundary. Exposing the same API to a
// browser over a network removes that boundary, and the token in a page is no longer
// a secret — so the server form needs a real login:
//
//	password (argon2id, the SAME store the lock screen uses)
//	  + optional TOTP second factor (internal/totp)
//	  -> a short-lived session cookie (HttpOnly, SameSite=Strict)
//
// Three decisions shape it:
//
//  1. READ-ONLY BY DEFAULT. A server exposing a desktop client's full API to a LAN
//     would expose backup/transfer/rowsync/connection-delete too. Those are refused
//     unless the operator explicitly turns writes on, and even then they keep their
//     own confirmations.
//  2. The session lives in memory. A restart logs everyone out — the right default for
//     a database client, where a stale session is a credential with database access.
//  3. Sessions are bound to nothing but their own expiry and are dropped on logout,
//     password change and shutdown. There is no "remember me" because there is no
//     place to remember it that a database client should trust.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"tablepro-go/internal/totp"
)

// sessionCookieName is the session cookie. Deliberately not the API token header: the
// browser attaches a cookie by itself, which is the point, and HttpOnly keeps it away
// from scripts.
const sessionCookieName = "tablepro_session"

// sessionIdleTimeout logs an idle session out. A database client left open in a browser
// tab is the same risk as an unlocked desktop app, so it uses the same shape of rule.
const sessionIdleTimeout = 30 * time.Minute

// sessionMaxLifetime caps a session even while it is being used: an attacker holding
// the cookie should not have it forever.
const sessionMaxLifetime = 12 * time.Hour

// serverSession is one logged-in browser.
type serverSession struct {
	// created and lastSeen drive the two timeouts.
	created  time.Time
	lastSeen time.Time
	// totpStep is the last TOTP step this session used, so a code cannot be replayed
	// into a second login.
	totpStep int64
}

// sessionStore holds sessions in memory, keyed by a random id.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*serverSession
	// totpStep is process-wide: TOTP replay protection must not be per-session, or a
	// code used for one login could be reused for another inside its window.
	lastTOTPStep int64
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]*serverSession{}}
}

// create mints a session id.
func (s *sessionStore) create(now time.Time) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = &serverSession{created: now, lastSeen: now}
	return id, nil
}

// touch validates a session id and refreshes its idle clock.
func (s *sessionStore) touch(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return false
	}
	if now.Sub(sess.created) > sessionMaxLifetime || now.Sub(sess.lastSeen) > sessionIdleTimeout {
		delete(s.sessions, id)
		return false
	}
	sess.lastSeen = now
	return true
}

// drop removes one session (logout).
func (s *sessionStore) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// clear drops every session: password change and shutdown both invalidate them.
func (s *sessionStore) clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.sessions)
	s.sessions = map[string]*serverSession{}
	return n
}

// noteTOTPStep records the TOTP step a login consumed and reports whether it was fresh.
// Reusing a step is refused process-wide, not per session.
func (s *sessionStore) noteTOTPStep(step int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if step <= s.lastTOTPStep {
		return false
	}
	s.lastTOTPStep = step
	return true
}

// lastStep reports the newest consumed TOTP step, so Verify can refuse older ones.
func (s *sessionStore) lastStep() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTOTPStep
}

// serverAuth is the whole server-mode auth surface.
type serverAuth struct {
	// enabled is false for the desktop app, where the token header is the boundary.
	enabled bool
	// app supplies the password store and the TOTP secret.
	app *App
	// sessions holds the logged-in browsers.
	sessions *sessionStore
	// allowWrites decides whether the mutating routes are reachable at all.
	allowWrites bool
	// now is injectable so the tests do not sleep.
	now func() time.Time
}

// readOnlyRoutes are the paths a server-mode session may call when writes are off.
//
// This is an ALLOWLIST, not a blocklist: a new endpoint added later is refused by
// default, and the reviewer has to decide which side it belongs on. A blocklist would
// silently publish every future route.
var readOnlyRoutes = map[string]bool{
	"/api/connections":              true,
	"/api/connections/test":         true,
	"/api/schemas":                  true,
	"/api/tables":                   true,
	"/api/columns":                  true,
	"/api/indexes":                  true,
	"/api/foreignkeys":              true,
	"/api/query":                    true,
	"/api/explain":                  true,
	"/api/explain-plan":             true,
	"/api/preview":                  true,
	"/api/history":                  true,
	"/api/favorites":                true,
	"/api/search":                   true,
	"/api/objects/search":           true,
	"/api/data-search":              true,
	"/api/notebooks":                true,
	"/api/notebooks/run":            true,
	"/api/build-info":               true,
	"/api/plugins":                  true,
	// Listing a plugin's console actions is read-only, so a server-mode viewer can see
	// that a connection offers "向量检索" or "消费消息". RUNNING one is deliberately NOT
	// whitelisted: the host cannot tell a read-only action (a similarity search) from a
	// writing one (publish-config, produce-message), and guessing wrong in the permissive
	// direction is exactly what the read-only default exists to prevent.
	"/api/plugin/actions":           true,
	"/api/oracle/invalid-objects":   true,
	"/api/oracle/dependencies":      true,
	"/api/settings":                 true,
}

// loginRequest is the login form.
type loginRequest struct {
	Password string `json:"password"`
	// TOTP is required only when a secret is enrolled.
	TOTP string `json:"totp"`
}

// loginResult tells the browser what to do next.
type loginResult struct {
	OK bool `json:"ok"`
	// NeedsTOTP is set when the password was right but the second factor is missing:
	// the UI then shows the code field instead of saying "wrong password".
	NeedsTOTP bool   `json:"needsTotp,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleLogin verifies the password (and the code when enrolled) and sets the cookie.
func (a *App) handleLogin(sa *serverAuth, w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	now := sa.now()
	if !a.VerifyPasscode(req.Password) {
		// One message for "no password set" and "wrong password": telling an
		// unauthenticated caller which one it is hands them the setup state.
		writeError(w, http.StatusUnauthorized, "口令错误")
		return
	}
	secret, err := a.totpSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !secret.Empty() {
		if strings.TrimSpace(req.TOTP) == "" {
			// The password was right, so saying "code required" leaks nothing new.
			writeJSON(w, loginResult{NeedsTOTP: true})
			return
		}
		step, ok, verr := secret.Verify(req.TOTP, now, totp.Options{Skew: 1, LastUsedStep: sa.sessions.lastStep()})
		if verr != nil {
			writeError(w, http.StatusBadRequest, verr.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "验证码错误（或已使用过）")
			return
		}
		if !sa.sessions.noteTOTPStep(step) {
			writeError(w, http.StatusUnauthorized, "验证码已使用过")
			return
		}
	}
	id, err := sa.sessions.create(now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Secure is left false because the local default is plain HTTP on loopback. A
		// deployment behind TLS should set it, and server-mode docs say so.
		MaxAge: int(sessionMaxLifetime.Seconds()),
	})
	writeJSON(w, loginResult{OK: true})
}

// handleLogout drops the caller's session.
func (a *App) handleLogout(sa *serverAuth, w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		sa.sessions.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]bool{"ok": true})
}

// serverAuthMiddleware gates the API in server mode.
//
// The login and logout routes are the only ones reachable without a session, and the
// static frontend is served without one too (it is the login page). Everything else
// needs a live session; mutating routes additionally need writes enabled.
func (a *App) serverAuthMiddleware(sa *serverAuth, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/login" || path == "/api/logout" {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(path, "/api/") {
			// Static assets: the login page has to load before anyone is logged in.
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !sa.sessions.touch(cookie.Value, sa.now()) {
			writeError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if !sa.allowWrites && !readOnlyRoutes[path] {
			writeError(w, http.StatusForbidden, "服务器模式默认只读：该接口未在只读白名单内")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// totpSecret reads the enrolled secret, or an empty one.
//
// A missing file means "not enrolled", which is the normal case — refusing to log in
// because an optional file is absent would lock everyone out of a fresh install.
func (a *App) totpSecret() (totp.Secret, error) {
	raw, err := os.ReadFile(userDataFilePath("totp.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return totp.Secret{}, nil
		}
		return totp.Secret{}, err
	}
	var stored struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(trimBOMBytes(raw), &stored); err != nil {
		return totp.Secret{}, fmt.Errorf("读取两步验证配置: %w", err)
	}
	if stored.Secret == "" {
		return totp.Secret{}, nil
	}
	return totp.SecretFromBase32(stored.Secret)
}

// EnrolTOTP generates and stores a secret, returning the otpauth URI for the user's
// authenticator. It requires the passcode, so enrolment is not something a passer-by
// can trigger on an unlocked desktop app.
func (a *App) EnrolTOTP(passcode, account string) (string, error) {
	if !a.VerifyPasscode(passcode) {
		return "", errors.New("口令错误")
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		return "", err
	}
	if account == "" {
		account = "user"
	}
	body, err := json.MarshalIndent(map[string]string{"secret": secret.String()}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(userDataFilePath("totp.json"), body); err != nil {
		return "", err
	}
	return secret.ProvisioningURI("TablePro Go", account), nil
}

// DisableTOTP removes the enrolled secret.
func (a *App) DisableTOTP(passcode string) error {
	if !a.VerifyPasscode(passcode) {
		return errors.New("口令错误")
	}
	path := userDataFilePath("totp.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// verifyTOTPNow is the constant-time-ish entry the tests use: it verifies a code for the
// current wall clock without touching the session store.
func (a *App) verifyTOTPNow(code string, now time.Time) (bool, error) {
	secret, err := a.totpSecret()
	if err != nil {
		return false, err
	}
	if secret.Empty() {
		return false, errors.New("未启用两步验证")
	}
	_, ok, verr := secret.Verify(code, now, totp.Options{Skew: 1})
	return ok, verr
}


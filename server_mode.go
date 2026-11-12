package main

// W16: the server form.
//
// `tablepro-go --serve` runs the same HTTP API the desktop app exposes, but on a real
// address, in front of a login. Three things make it safe to expose where the desktop
// app needs no login:
//
//   - a password (the SAME argon2id store the lock screen uses) plus optional TOTP;
//   - a read-only allowlist, so a viewer cannot back up, transfer or drop anything;
//   - sessions in memory, so a restart logs everyone out.
//
// What it is NOT: a multi-tenant service. There is one credential, no per-user
// accounts, and no TLS (a deployment puts it behind a reverse proxy). The docs say so
// rather than implying a hardened appliance.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// defaultServeAddr is loopback: a server mode that bound 0.0.0.0 by default would
// publish a database client to whatever network the machine is on the moment the user
// tried the flag.
const defaultServeAddr = "127.0.0.1:8765"

// serveStatic serves the embedded frontend WITHOUT the token injection the desktop
// host does: in server mode there is no token, and injecting one would hand every
// visitor a credential the API ignores anyway.
func serveStatic(assets fs.FS) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A single-page app: any unknown path is the app shell, not a 404.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(assets, path); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

// runServe starts the server form and blocks until the process is signalled.
//
// It returns an error rather than calling log.Fatal so the caller decides the exit code
// and the tests can drive it.
func runServe(svc *App, opts cliOptions) error {
	if !svc.HasPasscode() {
		return errors.New("服务器模式需要先设置口令（在桌面应用里设置一次，或用 --passcode 指定已有文件）")
	}
	addr := strings.TrimSpace(opts.listen)
	if addr == "" {
		addr = defaultServeAddr
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", addr, err)
	}
	sa := &serverAuth{
		enabled:     true,
		app:         svc,
		sessions:    newSessionStore(),
		allowWrites: opts.serveWrites,
		now:         time.Now,
	}

	// The login/logout routes are registered here rather than in routes(): they exist
	// ONLY in server mode, and the desktop app must not expose a password endpoint.
	api := svc.routes()
	sessionAPI := http.NewServeMux()
	sessionAPI.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		svc.handleLogin(sa, w, r)
	})
	sessionAPI.HandleFunc("/api/logout", func(w http.ResponseWriter, r *http.Request) {
		svc.handleLogout(sa, w, r)
	})
	sessionAPI.Handle("/", api)

	root := http.NewServeMux()
	root.Handle("/api/", svc.serverAuthMiddleware(sa, sessionAPI))
	root.Handle("/", serveStatic(assets))

	srv := &http.Server{
		Handler:      root,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	mode := "只读"
	if opts.serveWrites {
		mode = "可写"
	}
	log.Printf("服务器模式已启动：http://%s（%s，%s）", listener.Addr().String(), mode, totpState(svc))
	log.Printf("用浏览器打开上面的地址，输入桌面应用里设置的口令登录")

	done := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		// A graceful shutdown drops the sessions and every open transaction, the same
		// way closing the desktop window does.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		close(done)
	}()
	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	svc.Shutdown(context.Background())
	return nil
}

// totpState describes the second factor in the startup log, so an operator can tell at
// a glance whether a deployment they thought had 2FA actually does.
func totpState(svc *App) string {
	secret, err := svc.totpSecret()
	if err != nil {
		return "两步验证状态未知：" + err.Error()
	}
	if secret.Empty() {
		return "未启用两步验证"
	}
	return "已启用两步验证"
}

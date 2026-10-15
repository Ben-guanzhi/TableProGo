package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"log"
	"net"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

// randomToken returns 32 random bytes, hex encoded. Hex keeps the value safe to
// splice straight into the injected <script> tag.
func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func main() {
	svc := NewApp()

	// Minted per process, so a page cached from an earlier run — or served by
	// another instance — keeps a stale value and is refused.
	token, err := randomToken()
	if err != nil {
		log.Fatal("Failed to mint API token:", err)
	}
	svc.apiToken = token

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal("Failed to start API server:", err)
	}
	apiPort := listener.Addr().(*net.TCPAddr).Port
	log.Printf("HTTP API server on port %d (token %s)", apiPort, token)

	svc.startHTTPServerOnListener(listener)

	wailsApp := application.New(application.Options{
		Name:        "TablePro Go",
		Description: "A fast, native database client built with Go and Wails.",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: &injectHandler{
				inner:  application.AssetFileServerFS(assets),
				inject: fmt.Sprintf("<script>window.__API_PORT__=%d;window.__API_TOKEN__=%q;</script>", apiPort, token),
			},
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "TablePro Go",
		MinWidth:  800,
		MinHeight: 600,
		Width:     1200,
		Height:    800,
		URL:       "/",
	})

	if err := wailsApp.Run(); err != nil {
		log.Fatal(err)
	}
}

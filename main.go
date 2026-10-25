package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

const usage = `TablePro Go

用法:
  tablepro-go          打开图形界面
  tablepro-go --mcp    以 MCP 服务器运行（stdio JSON-RPC，只读）
  tablepro-go --help   显示本帮助
`

// mcpMode reports whether the process was started as an MCP server, accepting
// the spellings a shell or an MCP client config may actually produce.
func mcpMode() bool {
	mcp := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--mcp", "-mcp", "--mcp=true", "--mcp=1", "-mcp=true", "-mcp=1":
			mcp = true
		case "-h", "--help", "-help":
			fmt.Print(usage)
			os.Exit(0)
		}
	}
	return mcp
}

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
	// Engines this binary does not compile can still be used through a plugin
	// process (plugins.go). Read the declarations once, before any connection opens.
	for _, err := range plugins.Load() {
		log.Printf("plugin: %v", err)
	}

	// `tablepro-go --mcp` runs as an MCP server over stdio instead of opening the
	// window. An MCP client launches this binary as a child process and speaks
	// JSON-RPC on its pipes, so the flag has to be handled before any UI starts.
	if mcpMode() {
		svc := NewApp()
		// A saved connection is only a config; a tool needs a live one. Connect
		// every saved connection up front, reporting each failure on stderr (the
		// protocol owns stdout) and continuing with whatever did connect.
		for _, err := range svc.ConnectSavedConnections() {
			fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		}
		// A child process has no UI, so no write confirmation can be answered:
		// the server is read-only here by construction.
		if err := svc.RunMCPStdio(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
			os.Exit(1)
		}
		return
	}

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
	log.Printf("HTTP API server on port %d", apiPort)

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

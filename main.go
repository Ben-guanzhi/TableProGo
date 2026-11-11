package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"

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
  tablepro-go [选项]           打开图形界面
  tablepro-go --mcp            以 MCP 服务器运行（stdio JSON-RPC，只读）
  tablepro-go --job=<路径>     运行一份作业配方（供 cron / 任务计划调用）

选项:
  --port=<n>         HTTP API 服务器端口（默认随机，日志会打印实际端口）
  --config=<path>    连接列表文件路径（默认用户数据目录的 connections.json）
  --passcode=<path>  本地口令存储路径（默认用户数据目录的 passcode.json）
  --job=<path>       作业配方 JSON；执行完即退出（0=全部成功，1=有步骤失败，2=配方/用法错误）
  --dry-run          与 --job 连用：只校验并报告计划，不写任何数据
  --help, -h          显示本帮助

作业配方只看已保存的连接 ID，不读取任何口令；凭据仍只在连接存储里。
--mcp 的多种拼写（--mcp=true/--mcp=1/-mcp=...）由 flag 包原生处理。
`

// cliOptions is what one startup command line resolves to.
type cliOptions struct {
	mcp          bool
	jobPath      string
	dryRun       bool
	apiPort      int
	configPath   string
	passcodePath string
}

// parseFlags resolves the command line with the flag package. -h prints the
// usage and exits 0; anything unparsable returns an error the caller turns
// into an exit-2 message.
func parseFlags() (cliOptions, error) {
	opts := cliOptions{}
	port := ""
	config := ""
	passcode := ""
	mcp := false
	fs := flag.NewFlagSet("tablepro-go", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // -h and errors print via our own calls below
	fs.Usage = func() { fmt.Print(usage) }
	fs.StringVar(&port, "port", "", "")
	fs.StringVar(&config, "config", "", "")
	fs.StringVar(&passcode, "passcode", "", "")
	fs.BoolVar(&mcp, "mcp", false, "")
	job := ""
	dryRun := false
	fs.StringVar(&job, "job", "", "")
	fs.BoolVar(&dryRun, "dry-run", false, "")
	err := fs.Parse(os.Args[1:])
	if err == flag.ErrHelp {
		os.Exit(0)
	}
	if err != nil {
		return opts, err
	}
	// The flag package's bool accepts every spelling the old hand-rolled
	// switch did (bare -mcp, --mcp=true, --mcp=1, -mcp=false).
	opts.mcp = mcp
	opts.jobPath = job
	opts.dryRun = dryRun
	if port != "" {
		n, perr := strconv.Atoi(port)
		if perr != nil || n < 1 || n > 65535 {
			return opts, fmt.Errorf("invalid --port %q: want 1-65535", port)
		}
		opts.apiPort = n
	}
	opts.configPath = config
	opts.passcodePath = passcode
	return opts, nil
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
	opts, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Print(usage)
		os.Exit(2)
	}

	if opts.mcp {
		svc := newAppWithPaths(opts.configPath, opts.passcodePath)
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

	// W7: a scheduled job runs headless and exits. The recipe is validated before
	// any connection opens, so a broken schedule fails as a config error (exit 2)
	// rather than as a database error (exit 1) — the two send an operator to
	// different places at 3am.
	if opts.jobPath != "" {
		recipe, err := LoadJobRecipe(opts.jobPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "job: %v\n", err)
			os.Exit(2)
		}
		svc := newAppWithPaths(opts.configPath, opts.passcodePath)
		// The job's steps address saved connections by id; connecting them up
		// front is the same contract the MCP path uses.
		for _, err := range svc.ConnectSavedConnections() {
			fmt.Fprintf(os.Stderr, "job: 连接失败（该步骤会报错）: %v\n", err)
		}
		res := svc.RunJob(recipe, opts.dryRun, nil)
		// The summary goes to stdout so a cron wrapper can capture it; per-step
		// diagnostics already went to the log.
		out, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(out))
		svc.DisconnectAll()
		os.Exit(jobExitCode(res))
	}

	svc := newAppWithPaths(opts.configPath, opts.passcodePath)

	// Minted per process, so a page cached from an earlier run — or served by
	// another instance — keeps a stale value and is refused.
	token, err := randomToken()
	if err != nil {
		log.Fatal("Failed to mint API token:", err)
	}
	svc.apiToken = token

	addr := "127.0.0.1:0"
	if opts.apiPort > 0 {
		addr = fmt.Sprintf("127.0.0.1:%d", opts.apiPort)
	}
	listener, err := net.Listen("tcp", addr)
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

	// The Wails run has ended: release the API listener, the connections and
	// every transaction the tabs left open. Without this the process keeps a
	// bound socket and its server-side transactions alive after the window is
	// gone (TP-08).
	svc.Shutdown(context.Background())
}

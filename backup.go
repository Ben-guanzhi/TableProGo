package main

// 原生工具备份（移植自 db-dev §9，对标 TablePlus 的模式）: 客户端不实现
// dump 语义，只做 探测服务端版本 → 选择官方命令行工具 (pg_dump / mysqldump)
// → 拼参数 → stderr 逐行日志 → ctx 取消即杀进程。一致性快照、序列、大表
// 流式等难点全部交给官方工具，备份产物与官方工具逐字节一致。
//
// 工具来源: 环境变量 TABLEPRO_PG_DUMP / TABLEPRO_MYSQLDUMP 指定路径（沿用
// db-dev 的 TABULA_* 别名），否则在 PATH 上查找。口令经 PGPASSWORD /
// MYSQL_PWD 环境变量传递，不进 argv（argv 在进程列表里可见）。SQLite 与
// SQL Server / Oracle 无此模式的对应物，明确报错而不是假装成功。
//
// 与 db-dev 的两点差异: 隧道由连接自己持有（Connect 时建好），工具对
// dialTarget() 的回环地址跑；输出必须给 OutPath，由工具自己写文件，
// 大备份不经进程内存，也不经 JSON。

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// BackupRequest 描述一次原生工具备份。
type BackupRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	// Mode: full（结构+数据）/ structure（仅结构）/ data（仅数据）；空为 full。
	Mode    string   `json:"mode"`
	Tables  []string `json:"tables"`
	OutPath string   `json:"outPath"`
}

// BackupResult 汇总一次备份：日志行（工具的进度/告警都走 stderr）与结果。
type BackupResult struct {
	OK    bool     `json:"ok"`
	Log   []string `json:"log"`
	Error string   `json:"error,omitempty"`
}

// backupLog 逐行收集日志，上限防止一次失控工具把响应撑爆。
type backupLog struct {
	lines []string
}

func (l *backupLog) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	if len(l.lines) > 400 {
		l.lines = l.lines[len(l.lines)-400:]
	}
}

// nativeBackupSupported 报告该引擎能否走原生工具备份。
func nativeBackupSupported(dbType string) bool {
	return dbType == "postgresql" || dbType == "mysql"
}

// Backup 执行一次备份。postgres/mysql 走原生工具；其余引擎明确拒绝。
func (a *App) Backup(req BackupRequest) (BackupResult, error) {
	res := BackupResult{Log: []string{}}
	log := &backupLog{lines: res.Log}
	conn, err := a.connection(req.ConnID)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	if req.OutPath == "" {
		res.Error = "备份需要输出文件路径"
		return res, fmt.Errorf("%s", res.Error)
	}
	if !nativeBackupSupported(conn.Type) {
		res.Error = fmt.Sprintf("引擎 %s 无官方单文件 dump 工具（SQLite 是单文件库；SQL Server/Oracle 的备份是服务端操作），此对话仅支持 PostgreSQL/MySQL", conn.Type)
		return res, fmt.Errorf("%s", res.Error)
	}
	database := req.Database
	if database == "" {
		database = conn.Database
	}
	// 取消经 CancelQuery 生效：ctx 取消即杀掉 dump 进程。
	ctx, done := a.beginRun(req.ConnID)
	defer done()

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "full"
	}
	host, port := conn.dialTarget()
	if conn.Type == "postgresql" {
		err = a.runPgDump(ctx, conn, log, req, database, mode, host, port)
	} else {
		err = a.runMysqlDump(ctx, conn, log, req, database, mode, host, port)
	}
	if err == nil {
		log.logf("完成")
	}
	res.OK = err == nil
	res.Log = log.lines
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	return res, nil
}

// serverVersion 探测服务端版本原文，供日志与工具版本比对。复用连接自己的
// 池（版本查询对任意库都成立），并走可取消 ctx。
func (a *App) serverVersion(conn *Connection, ctx context.Context, query string) (string, error) {
	result := runSelectCtx(ctx, conn.DB, query)
	if result.Error != "" {
		return "", fmt.Errorf("%s", result.Error)
	}
	if result.RowCount == 0 || len(result.Rows) == 0 {
		return "", fmt.Errorf("版本查询未返回结果: %s", query)
	}
	if s, ok := result.Rows[0][0].(string); ok {
		return s, nil
	}
	return fmt.Sprintf("%v", result.Rows[0][0]), nil
}

var versionNumRe = regexp.MustCompile(`\d+`)

// ParseServerMajor 取版本原文里的首个数字段: "PostgreSQL 16.4 (...)" → 16,
// "8.4.3" → 8。解析失败返回 0（视为未知，只影响告警）。
func ParseServerMajor(raw string) int {
	m := versionNumRe.FindString(raw)
	if m == "" {
		return 0
	}
	n, err := strconv.Atoi(m)
	if err != nil {
		return 0
	}
	return n
}

// ResolveDumpTool 定位 dump 工具：先看本应用的环境变量（兼容 db-dev 的
// TABULA_* 拼写），再查 PATH。
func ResolveDumpTool(envVars []string, name string) (string, error) {
	for _, envVar := range envVars {
		if p := os.Getenv(envVar); p != "" {
			if _, err := os.Stat(p); err != nil {
				return "", fmt.Errorf("%s 指向的工具不可用: %w", envVar, err)
			}
			return p, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("PATH 上未找到 %s，请安装客户端工具或用环境变量指定路径", name)
	}
	return p, nil
}

// toolMajor 取工具自身的主版本号（"pg_dump --version"），失败返回 0。
func toolMajor(ctx context.Context, tool string) int {
	out, err := exec.CommandContext(ctx, tool, "--version").Output()
	if err != nil {
		return 0
	}
	return ParseServerMajor(string(out))
}

// pgSSLMode 把连接配置的 SSLMode 翻译成 PGSSLMODE 的拼写；空串表示交给
// 工具的默认（prefer）。
func pgSSLMode(sslMode string) string {
	switch strings.ToLower(strings.TrimSpace(sslMode)) {
	case "false", "disable":
		return "disable"
	case "true", "require", "enable":
		return "require"
	case "strict":
		return "verify-full"
	}
	return ""
}

func (a *App) runPgDump(ctx context.Context, conn *Connection, log *backupLog, req BackupRequest, database, mode, host string, port int) error {
	tool, err := ResolveDumpTool([]string{"TABLEPRO_PG_DUMP", "TABULA_PG_DUMP"}, "pg_dump")
	if err != nil {
		return err
	}
	raw, err := a.serverVersion(conn, ctx, "SELECT version()")
	if err != nil {
		return fmt.Errorf("探测服务端版本: %w", err)
	}
	server := ParseServerMajor(raw)
	toolVer := toolMajor(ctx, tool)
	log.logf("pg_dump: 服务端 %s（主版本 %d），工具主版本 %d", raw, server, toolVer)
	if server > 0 && toolVer > 0 && server != toolVer {
		log.logf("警告: pg_dump 主版本与服务端不一致，官方建议使用与服务器相同或更新的 pg_dump")
	}
	args := pgDumpArgs(conn.Username, req, database, mode, host, port)
	env := append(os.Environ(), "PGPASSWORD="+conn.Password)
	if ssl := pgSSLMode(conn.SSLMode); ssl != "" {
		env = append(env, "PGSSLMODE="+ssl)
	}
	log.logf("+ %s %s", tool, strings.Join(args, " "))
	return runDumpTool(ctx, log, tool, args, env, req.OutPath)
}

// pgDumpArgs 生成 pg_dump 参数（口令走 PGPASSWORD，不出现在 argv）。
func pgDumpArgs(user string, req BackupRequest, database, mode, host string, port int) []string {
	var args []string
	switch mode {
	case "structure":
		args = append(args, "--schema-only")
	case "data":
		args = append(args, "--data-only")
	}
	for _, t := range req.Tables {
		args = append(args, "-t", t)
	}
	args = append(args,
		"--file="+req.OutPath,
		"--host="+host,
		"--port="+strconv.Itoa(port),
		"--username="+user,
		"--dbname="+database,
	)
	return args
}

func (a *App) runMysqlDump(ctx context.Context, conn *Connection, log *backupLog, req BackupRequest, database, mode, host string, port int) error {
	tool, err := ResolveDumpTool([]string{"TABLEPRO_MYSQLDUMP", "TABULA_MYSQLDUMP"}, "mysqldump")
	if err != nil {
		return err
	}
	raw, err := a.serverVersion(conn, ctx, "SELECT VERSION()")
	if err != nil {
		return fmt.Errorf("探测服务端版本: %w", err)
	}
	server := ParseServerMajor(raw)
	toolVer := toolMajor(ctx, tool)
	log.logf("mysqldump: 服务端 %s（主版本 %d），工具主版本 %d", raw, server, toolVer)
	args := mysqlDumpArgs(conn.Username, conn.SSLMode, req, database, mode, host, port, server)
	env := append(os.Environ(), "MYSQL_PWD="+conn.Password)
	log.logf("+ %s %s", tool, strings.Join(args, " "))
	return runDumpTool(ctx, log, tool, args, env, req.OutPath)
}

// mysqlDumpArgs 生成 mysqldump 参数。--single-transaction 保证 InnoDB 一致性
// 快照且不锁表；--quick 流式读大表。5.x 与 8.x 的 SSL 开关拼写不同，按服务端
// 主版本选择。
func mysqlDumpArgs(user, sslMode string, req BackupRequest, database, mode string, host string, port int, serverMajor int) []string {
	var args []string
	args = append(args,
		"--single-transaction", "--quick",
		"--host="+host,
		"--port="+strconv.Itoa(port),
		"--user="+user,
		"--protocol=TCP",
	)
	if sslWanted(sslMode) {
		if serverMajor >= 8 {
			args = append(args, "--ssl-mode=REQUIRED")
		} else {
			args = append(args, "--ssl")
		}
	}
	switch mode {
	case "structure":
		args = append(args, "--no-data")
	case "data":
		args = append(args, "--no-create-info")
	default:
		args = append(args, "--routines", "--events")
	}
	args = append(args, "--result-file="+req.OutPath)
	args = append(args, database)
	return append(args, req.Tables...)
}

func sslWanted(sslMode string) bool {
	switch strings.ToLower(strings.TrimSpace(sslMode)) {
	case "true", "require", "enable", "strict":
		return true
	}
	return false
}

// runDumpTool 启动工具：stderr 逐行回传日志，stdout 弃（工具自己写文件）；
// ctx 取消即杀进程。返回时把 stderr 的关键行带进错误信息，避免用户只看到
// "exit status 1"。
func runDumpTool(ctx context.Context, log *backupLog, tool string, args, env []string, outPath string) error {
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = env
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s: %w", tool, err)
	}
	var tail []string // stderr 末尾几行，失败时进错误信息
	errc := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			log.logf("%s", line)
			tail = append(tail, line)
			if len(tail) > 10 {
				tail = tail[1:]
			}
		}
		errc <- sc.Err()
	}()
	waitErr := cmd.Wait()
	scanErr := <-errc
	if ctx.Err() != nil {
		return fmt.Errorf("备份已取消")
	}
	if waitErr != nil {
		msg := strings.Join(tail, " | ")
		if msg == "" {
			msg = waitErr.Error()
		}
		return fmt.Errorf("%s 失败: %s", tool, msg)
	}
	if scanErr != nil {
		return fmt.Errorf("读取 %s 的 stderr: %w", tool, scanErr)
	}
	// 工具成功但没写出文件也是失败（例如 --file 指向不存在的目录）。
	if info, err := os.Stat(outPath); err != nil || info.Size() == 0 {
		return fmt.Errorf("%s 已退出但输出文件为空或不存在: %s", tool, outPath)
	}
	return nil
}

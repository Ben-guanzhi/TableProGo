package workbenchhost

// proc.go — the workbench host's own process framing.
//
// The framing is byte-identical to plugindriver's (Banner line + Request/Response
// JSON lines), but the runner owns the process instead of borrowing the SQL
// driver host: a workbench plugin is not a database driver, and the runner needs
// things the SQL host does not expose — a hard KILL for the kill/recover
// acceptance, and a process-exit wait that fires the deactivation path the
// moment the child dies rather than on the next failed call.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/dbcore/dbcore/plugindriver"
	"github.com/dbcore/dbcore/plugindriver/workbench"
)

// proc is one running plugin process.
type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	wmu    sync.Mutex // serializes writes (one request line at a time)
	rmu    sync.Mutex // serializes reads: bufio.Reader is not safe for concurrent use
	nextID int64
	idMu   sync.Mutex
	// onPush handles a PUSH frame (the provider answering a forwarded proxy
	// request): the frame's method names the push kind and its ID matches the
	// pending consumer request. Nil = pushes are discarded (single-plugin).
	onPush func(plugindriver.Response)

	// deadCh closes when the process exits or a pipe breaks; exitErr records
	// why. wait() runs the goroutine that closes it — the runner's death
	// notification hangs off this channel, which is what makes kill→recover a
	// push, not a poll.
	deadCh  chan struct{}
	deadMu  sync.Mutex
	deadSet bool
	exitErr error
}

// startProc launches the plugin binary and reads its banner. The banner is the
// protocol's contract (proto name + version): a stale binary fails at launch
// with a clear message instead of at the first effect.
func startProc(path string, args []string) (*proc, workbench.Banner, error) {
	var banner workbench.Banner
	cmd := exec.Command(path, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, banner, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, banner, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, banner, fmt.Errorf("launch plugin %s: %w", path, err)
	}
	p := &proc{
		cmd:    cmd,
		stdin:  stdin,
		out:    bufio.NewReaderSize(stdout, 1<<16),
		deadCh: make(chan struct{}),
	}

	// The plugindriver banner comes first (protocol identity), then the runner
	// asks for the workbench hello (component interface).
	line, err := p.readLine(10 * time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, banner, fmt.Errorf("plugin %s: read banner: %w", path, err)
	}
	var pb plugindriver.Banner
	if err := json.Unmarshal([]byte(line), &pb); err != nil {
		_ = cmd.Process.Kill()
		return nil, banner, fmt.Errorf("plugin %s: bad banner: %w", path, err)
	}
	if pb.Proto != plugindriver.ProtocolName || pb.Version != plugindriver.ProtocolVersion {
		_ = cmd.Process.Kill()
		return nil, banner, fmt.Errorf("plugin %s: speaks %s v%d, want %s v%d",
			path, pb.Proto, pb.Version, plugindriver.ProtocolName, plugindriver.ProtocolVersion)
	}

	p.wait()
	if err := p.call(context.Background(), workbench.MethodHello, nil, &banner); err != nil {
		_ = cmd.Process.Kill()
		return nil, banner, fmt.Errorf("plugin %s: hello: %w", path, err)
	}
	return p, banner, nil
}

// wait watches the process and closes deadCh the moment it is gone.
func (p *proc) wait() {
	go func() {
		err := p.cmd.Wait()
		p.deadMu.Lock()
		p.exitErr = err
		p.deadSet = true
		p.deadMu.Unlock()
		close(p.deadCh)
	}()
}

// Dead reports whether the process is gone. Non-blocking, for diagnostics.
func (p *proc) Dead() bool {
	select {
	case <-p.deadCh:
		return true
	default:
		return false
	}
}

// ExitErr returns the process exit error after death.
func (p *proc) ExitErr() error {
	p.deadMu.Lock()
	defer p.deadMu.Unlock()
	return p.exitErr
}

// DeadCh is the death notification channel.
func (p *proc) DeadCh() <-chan struct{} { return p.deadCh }

// readLine reads one framed response with a deadline: a wedged plugin must fail
// the launch, not hang the host.
//
// The read is serialized under rmu AND guarded by the deadline. A timed-out read
// abandons the goroutine, which may consume the NEXT response — so a timed-out
// call is terminal for this proc; the runner's kill/recover path replaces the
// whole process anyway.
func (p *proc) readLine(timeout time.Duration) (string, error) {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := p.out.ReadString('\n')
		ch <- res{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return "", r.err
		}
		return r.line, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("plugin did not answer within %v", timeout)
	}
}

// call issues one request and reads exactly one response frame.
//
// A DEAD process fails immediately: an in-flight unload's inverse (a .tmp
// discard, a recording stop) must not block 30s on a read that can never
// complete — the unloader holds the converge loop, and one slow inverse would
// stall it.
func (p *proc) call(ctx context.Context, method string, params any, result any) error {
	if p.Dead() {
		return fmt.Errorf("%s: plugin process is dead", method)
	}
	p.idMu.Lock()
	p.nextID++
	id := p.nextID
	p.idMu.Unlock()

	req := plugindriver.Request{ID: id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = raw
	}
	frame, err := json.Marshal(req)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	if _, werr := fmt.Fprintf(p.stdin, "%s\n", frame); werr != nil {
		p.wmu.Unlock()
		return fmt.Errorf("write %s: %w", method, werr)
	}
	if f, ok := p.stdin.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
	p.wmu.Unlock()

	line, err := p.readLine(30 * time.Second)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	var resp plugindriver.Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return fmt.Errorf("decode %s: %w (%s)", method, err, line)
	}
	if resp.ID != id {
		return fmt.Errorf("%s: response id %d, want %d", method, resp.ID, id)
	}
	if resp.Error != "" {
		return fmt.Errorf("%s: %s", method, resp.Error)
	}
	if result != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

// kill is the hard kill the W-T1 acceptance needs: the process dies mid-load or
// mid-session with no chance to clean up, which is exactly the case the
// accumulator must handle (Thm 68 despite arbitrary failure).
func (p *proc) kill() error {
	return p.cmd.Process.Kill()
}

// Close is the POLITE shutdown: ask the plugin to exit, then fall back to the
// hard kill if it does not go within a grace period.
func (p *proc) Close() error {
	p.call(context.Background(), plugindriver.MethodClose, nil, nil)
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		return p.kill()
	}
}

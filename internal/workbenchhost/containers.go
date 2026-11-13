package workbenchhost

// containers.go — W-C1's host-side container surface.
//
// The consumer plugin (navop-containers) exposes its container actions over the
// SAME channel-data method the terminal uses, dispatched by an `action` field:
//
//	ps         list containers through the tunnel
//	exec       start a shell inside a container, framed as a terminal channel
//	exec-data  write keystrokes, drain the shell's output
//	exec-close drop one container shell
//	kubectl    passthrough (the remote host runs kubectl)
//
// The host adds no semantics of its own: it relays, and it keeps the
// declaration discipline (the consumer's fiber must be Active, i.e. the
// tunnel.proxy dependency is satisfied — an inactive fiber cannot reach the
// daemon at all, which is AD-2's "Inactive 等待" doing real work).

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore/plugindriver/workbench"
	"tablepro-go/internal/ctxcore"
)

// ContainerCall is one generic container action round trip. It exists so the
// HTTP layer can dispatch without a method per action; the typed wrappers below
// are what Go callers use.
func (r *Runner) ContainerCall(action, session string, args any) (map[string]any, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return nil, fmt.Errorf("%s: the container plugin fiber is not active (is navop-ssh running?)", workbench.CodeInactive)
	}
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return nil, fmt.Errorf("container plugin process is not running")
	}
	params := map[string]any{"action": action, "session": session}
	if args != nil {
		params["args"] = args
	}
	var out map[string]any
	if err := p.call(context.Background(), workbench.MethodChannelData, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ContainerPS lists the containers the daemon reports. The rows are the
// plugin's shape ({id,name,image,state}) — the host renders them as-is.
func (r *Runner) ContainerPS(session string) ([]map[string]any, error) {
	out, err := r.ContainerCall("ps", session, nil)
	if err != nil {
		return nil, err
	}
	rows, _ := out["containers"].([]any)
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result, nil
}

// ContainerKubectl runs kubectl ON the remote host and returns its output (the
// plan's "kubectl 透传插件化").
func (r *Runner) ContainerKubectl(session, args string) (string, error) {
	out, err := r.ContainerCall("kubectl", session, args)
	if err != nil {
		return "", err
	}
	text, _ := out["output"].(string)
	return text, nil
}

// ContainerExec opens a shell INSIDE one container. The returned id is a
// terminal channel in the WORKBENCH sense (the plugin frames the container's
// shell as one) — exec reuses terminal.channels, as the plan requires.
func (r *Runner) ContainerExec(session, container string) (string, error) {
	out, err := r.ContainerCall("exec", session, map[string]any{"container": container})
	if err != nil {
		return "", err
	}
	if id, ok := out["channelId"].(string); ok && id != "" {
		return id, nil
	}
	return "", fmt.Errorf("the plugin did not return a channel id")
}

// ContainerExecData writes keystrokes and drains the shell's output.
func (r *Runner) ContainerExecData(channelID, data string) (string, bool, error) {
	out, err := r.ContainerCall("exec-data", "", map[string]any{"channelId": channelID, "data": data})
	if err != nil {
		return "", false, err
	}
	text, _ := out["output"].(string)
	ended, _ := out["ended"].(bool)
	return text, ended, nil
}

// ContainerExecClose drops one container shell.
func (r *Runner) ContainerExecClose(channelID string) error {
	_, err := r.ContainerCall("exec-close", "", map[string]any{"channelId": channelID})
	return err
}

// ContainerCloseSession drops a container session on the plugin side. The
// fiber's accumulator is the inverse of record; this is the operator's early
// teardown (the same shape as CloseChannel/StopMonitor).
func (r *Runner) ContainerCloseSession(session string) error {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return nil // dead process: nothing to close, the recovery path owns it
	}
	return p.call(context.Background(), workbench.MethodChannelClose, map[string]any{"session": session}, nil)
}

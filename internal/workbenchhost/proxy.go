package workbenchhost

// proxy.go — W-C1's host half: the coeffect access flow between TWO plugins.
//
// The flow (§5.1.4's get-trap at the protocol boundary, two processes apart):
//
//	CONSUMER's action needs a key it declared (tunnel.proxy)
//	  → the host checks the consumer's committed view — the key IS in d and the
//	    fiber is Active; UNDECLARED/INACTIVE_ACCESS otherwise;
//	  → the host CALLS the provider plugin (proc.call with
//	    workbench/proxy/request — the provider answers a NORMAL response frame
//	    with the same id, so no push plumbing is needed);
//	  → the provider's answer (the tunnel coordinates) is relayed to the
//	    consumer, which dials the daemon inside the chain.
//
// The host is a SWITCH, not an interpreter: it resolves WHICH fiber answers and
// refuses what the declarations forbid — it never reads the value's meaning.
// Confinement (Def 55) is what makes this safe: the consumer only reads keys it
// declared, and the provider only answers keys it provided.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dbcore/dbcore/plugindriver/workbench"
	"tablepro-go/internal/ctxcore"
)

// proxyRequest asks the provider plugin for a key this runner's fiber declared.
// It is the host-mediated version of ctx[key]:
//   - the key must be in the fiber's committed view (d enforced at the point of
//     use) — UNDECLARED_ACCESS otherwise;
//   - the request is forwarded to the PROVIDER the committed view names —
//     provider uids, not values (Def 53);
//   - the value comes back as an opaque JSON blob the CALLER interprets.
func (r *Runner) proxyRequest(key, op string, args map[string]any, timeout time.Duration) (jsonValue, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return nil, fmt.Errorf("%s: the consumer fiber is not active", workbench.CodeInactive)
	}
	// The declaration gate: ctx[key]'s first branch. An undeclared key is
	// refused here — the consumer never sees a value it did not ask for.
	declared := false
	for _, d := range f.Comp().Depends {
		if d == ctxcore.Key(key) {
			declared = true
			break
		}
	}
	if !declared {
		return nil, fmt.Errorf("%s: %s reads key %s it did not declare",
			workbench.CodeUndeclared, f.ID(), key)
	}
	// The provider: whoever is ACTIVE and provides this key (single-source
	// discipline — at most one, enforced at insert).
	provider := r.providerRunnerOf(key)
	if provider == nil {
		return nil, fmt.Errorf("%s: no active provider for key %s", workbench.CodeInactive, key)
	}
	params := map[string]any{"key": key, "op": op}
	if args != nil {
		params["args"] = args
	}
	var value jsonValue
	if err := provider.proc.call(context.Background(),
		workbench.MethodProxyRequest, params, &value); err != nil {
		return nil, fmt.Errorf("proxy %s/%s: %w", key, op, err)
	}
	return value, nil
}

// jsonValue is the opaque proxy answer: the caller interprets it, the host
// never does.
type jsonValue = map[string]any

// providerRunnerOf resolves the ACTIVE runner whose fiber provides key. The
// orchestrator owns the forwarding table — only tunnel.proxy is wired (the one
// dependency the plan names); more keys join as more providers ship.
func (r *Runner) providerRunnerOf(key string) *Runner {
	if r.Orchestrator == nil {
		return nil
	}
	if key == "tunnel.proxy" {
		return r.Orchestrator.ssh
	}
	return nil
}

// OpenContainerSession is the consumer's session open (W-C1's proof flow):
// resolve tunnel.proxy through the orchestrator, lend the SSH secret (AD-4), then
// hand the coordinates to the consumer plugin's open action. The consumer dials
// the daemon INSIDE the resolved chain.
func (r *Runner) OpenContainerSession(targetHost string, sshPort int, dockerAddr, secretName string, secret []byte) (string, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return "", fmt.Errorf("%s: the consumer fiber is not active", workbench.CodeInactive)
	}
	erase, err := r.LendSecret(secretName, secret)
	if err != nil {
		return "", err
	}
	defer func() { _ = erase() }()
	value, err := r.proxyRequest("tunnel.proxy", "dial",
		map[string]any{"targetHost": targetHost}, 15*time.Second)
	if err != nil {
		return "", err
	}
	// Relay the provider's coordinates to the CONSUMER plugin: its sessionOpen
	// dials the daemon inside the chain they describe.
	var info struct {
		SSHHost string `json:"sshHost"`
		SSHPort int    `json:"sshPort"`
		SSHUser string `json:"sshUser"`
	}
	if err := decodeInto(value, &info); err != nil {
		return "", fmt.Errorf("bad proxy value: %w", err)
	}
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return "", fmt.Errorf("consumer plugin process is not running")
	}
	var res struct {
		SessionID string `json:"sessionId"`
	}
	err = p.call(context.Background(), workbench.MethodChannelOpen, map[string]any{
		"tunnel":     value,
		"secret":     secretName,
		"dockerAddr": dockerAddr,
	}, &res)
	if err != nil {
		return "", err
	}
	return res.SessionID, nil
}

// decodeInto decodes an opaque proxy value into a typed shape.
func decodeInto(value jsonValue, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

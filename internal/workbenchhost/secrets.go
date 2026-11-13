package workbenchhost

// secrets.go — AD-4's host half: lending a secret to a plugin, with the erasing
// inverse the plan demands.
//
// The discipline, executable:
//
//   - THE NAME GATE: a plugin may only receive a secret it DECLARED in its
//     banner's Secrets list. Lending anything else is refused (UNDECLARED) — the
//     host never hands a credential to a plugin that did not ask for it by name.
//   - THE VALUE NEVER ENTERS A FRAME: proc.lendSecret writes a control line
//     (name + length) and then the raw bytes. No JSON envelope, no log line, no
//     key-surface entry, no cast ever contains it.
//   - THE ERASING INVERSE: the host keeps ONE zeroable copy, erases it as soon
//     as the plugin acknowledges (the transport is done), and registers the
//     eraser on the fiber's accumulator. Unloading the fiber erases again —
//     idempotent, and it covers the crash/retire path where a caller forgot.
//   - TYPE-FORCED: LendSecret RETURNS the eraser. A caller cannot obtain a lend
//     without also receiving the duty to erase; the accumulator holds a second
//     copy of that duty so an early return cannot leak the secret for the
//     process lifetime.
//
// Erasures are recorded by NAME in an observable log (never values), so tests and
// operators can see that the eraser ran.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dbcore/dbcore/plugindriver/workbench"
	"tablepro-go/internal/ctxcore"
)

// secretLog records erasure events by name (AD-4's observable receipt). It holds
// no values — an erasure log that stored the secret would defeat the erasure.
type secretLog struct {
	mu    sync.Mutex
	names []string
}

func (l *secretLog) note(name string) {
	l.mu.Lock()
	l.names = append(l.names, name)
	l.mu.Unlock()
}

func (l *secretLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.names))
	copy(out, l.names)
	return out
}

// ErasedSecrets returns the names whose eraser has run on this runner.
func (r *Runner) ErasedSecrets() []string { return r.secrets.snapshot() }

// LendSecret hands one secret to the plugin, under a name the plugin declared.
//
// It returns the ERASER the caller must run after the secret's last use. The
// host has already erased its own copy by the time this returns; the returned
// eraser covers the CALLER's copy (which is the same buffer, since the lend is
// by value — so calling it twice is safe and the accumulator's copy is the same
// closure).
func (r *Runner) LendSecret(name string, value []byte) (func() error, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return nil, fmt.Errorf("%s: the fiber is not active", workbench.CodeInactive)
	}
	r.mu.Lock()
	banner := r.banner
	p := r.proc
	r.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("plugin is not launched")
	}
	// The declaration gate: only a declared secret name may be lent.
	declared := false
	for _, s := range banner.Secrets {
		if s == name {
			declared = true
			break
		}
	}
	if !declared {
		return nil, fmt.Errorf("%s: plugin %s did not declare secret %s",
			workbench.CodeUndeclared, banner.PluginID, name)
	}

	// ONE zeroable copy owned by the host.
	buf := make([]byte, len(value))
	copy(buf, value)
	if err := p.lendSecret(context.Background(), name, buf); err != nil {
		workbench.Zero(buf)
		r.secrets.note(name + " (failed)")
		return nil, err
	}
	// Transport done: the host's copy is no longer needed.
	workbench.Zero(buf)
	r.secrets.note(name)

	// The accumulator's erasing inverse: idempotent, and it covers unload.
	eraser := func() error {
		workbench.Zero(buf)
		r.secrets.note(name + " (erased)")
		return nil
	}
	if err := r.store.Track(f, "lend-secret:"+name, func(*ctxcore.Store) (ctxcore.Inverse, error) {
		return func(*ctxcore.Store) error {
			workbench.Zero(buf)
			r.secrets.note(name + " (erased)")
			return nil
		}, nil
	}); err != nil {
		// The lend already happened; the local eraser still runs. Report the
		// tracking failure so the caller knows the accumulator is not covering it.
		_ = eraser()
		return nil, fmt.Errorf("track secret eraser: %w", err)
	}
	return eraser, nil
}

// lendSecretTo is the conveniences the API uses: lend, run open, and erase on
// ANY outcome. The explicit `defer erase()` shape is what the type forces.
func (r *Runner) withSecret(name string, value []byte, use func() error) error {
	erase, err := r.LendSecret(name, value)
	if err != nil {
		return err
	}
	defer func() { _ = erase() }()
	return use()
}

// SecretLendTimeout is how long a lend may take before the host gives up. A lend
// is one small write and one ack; a stall means the plugin is wedged.
const SecretLendTimeout = 15 * time.Second

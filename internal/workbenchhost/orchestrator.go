package workbenchhost

// orchestrator.go — W-C1's two-plugin orchestration.
//
// P0's ctxcore modeled the whole registry in ONE store; W-T1 ran ONE runner.
// W-C1 is where two plugin processes meet: the plan's canonical dependency
// (navop-containers declares tunnel.proxy; navop-ssh provides it) becomes a
// running system.
//
// The Orchestrator owns:
//   - the shared ctxcore store (ONE registry, two fibers — the calculus's F_γ
//     was always the WHOLE system, not per-plugin);
//   - the launch/stop sequencing the acceptance names: launch the provider and
//     the consumer in either order, observe Inactive-until-satisfied, kill the
//     provider, and the consumer deactivates FIRST (Thm 70's guard), then
//     re-activates against the fresh provider uid (§5.1.3 inertial reload);
//   - the proxy switch: ProxyRequest resolves the key's provider and CALLS it
//     (a plain request/response — the provider answers workbench/proxy/value
//     with the same frame id, which proc.call already matches).
//
// What the orchestrator does NOT do: interpret any key's value. The proxy
// forwards opaque JSON; only the two plugins know what "dial" means.

import (
	"fmt"
	"sync"
	"time"

	"tablepro-go/internal/ctxcore"
)

// Orchestrator runs TWO workbench plugins over one shared context store.
type Orchestrator struct {
	mu sync.Mutex
	// ssh is the provider runner (navop-ssh); other is the consumer
	// (navop-containers). The names are the plan's; a third plugin joins as
	// another runner with its own declared dependencies.
	ssh   *Runner
	other *Runner
	store *ctxcore.Store
}

// NewOrchestrator builds the two-runner system over one store. One registry,
// two fibers — which is why killing the provider deactivates the consumer: the
// derived σ unions over ACTIVE fibers, and the consumer's target view goes ⊥.
func NewOrchestrator() *Orchestrator {
	store := ctxcore.NewStore()
	o := &Orchestrator{store: store}
	o.ssh = New(store)
	o.ssh.Orchestrator = o
	o.other = New(store)
	o.other.Orchestrator = o
	return o
}

// SSH returns the provider runner (navop-ssh), for the terminal/files panels.
func (o *Orchestrator) SSH() *Runner { return o.ssh }

// Other returns the consumer runner (navop-containers).
func (o *Orchestrator) Other() *Runner { return o.other }

// Store exposes the shared registry for state snapshots.
func (o *Orchestrator) Store() *ctxcore.Store { return o.store }

// LaunchProvider launches navop-ssh and provides the host keys it declares.
func (o *Orchestrator) LaunchProvider(path string) error {
	if err := o.ssh.ProvideConnectionsArchive(map[string]string{"kind": "orchestrated"}); err != nil {
		return err
	}
	return o.ssh.Launch(path)
}

// LaunchConsumer launches the consumer plugin and REFUSES one whose banner does
// not declare tunnel.proxy — the orchestrator is wired for that dependency, and
// a silent mis-wiring would make the proxy flow meaningless.
func (o *Orchestrator) LaunchConsumer(path string) error {
	if err := o.other.Launch(path); err != nil {
		return err
	}
	b := o.other.Banner()
	for _, d := range b.DependsKeys() {
		if d == "tunnel.proxy" {
			return nil
		}
	}
	o.other.Stop()
	return fmt.Errorf("consumer %s does not declare tunnel.proxy", b.PluginID)
}

// WaitConsumerState is the acceptance's observation helper: block until the
// consumer's fiber reaches the wanted state.
func (o *Orchestrator) WaitConsumerState(want ctxcore.FiberState, d time.Duration) error {
	f := o.other.Fiber()
	if f == nil {
		return fmt.Errorf("consumer fiber is nil")
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f.State() == want {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("consumer fiber stayed %s for %v (reason=%q)", f.State(), d, f.Reason)
}

// Stop shuts both runners down in DEPENDENCY ORDER: the consumer first (its
// teardown must finish before the provider's), then the provider. The calculus
// would release the guard eventually even in the wrong order — the explicit
// order is what a real operator does and what the acceptance asserts.
func (o *Orchestrator) Stop() {
	o.mu.Lock()
	o.other.Stop()
	o.mu.Unlock()
	o.ssh.Stop()
}

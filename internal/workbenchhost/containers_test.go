package workbenchhost

// W-C1 host-surface tests: the container methods relay the plugin's actions and
// keep the AD-2 gate (an inactive consumer cannot reach the daemon at all).
//
// The fake plugin answers the container actions deterministically, so these
// tests pin the HOST half: dispatch shape, row decoding, and the inactive
// refusal. The end-to-end dial through a real tunnel is covered by
// TestProxyFlowThroughOrchestrator (real navop-containers + in-process sshd).

import (
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

func TestContainerActionsRelay(t *testing.T) {
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	waitFiberState(t, r.Fiber(), ctxcore.Active, 5*time.Second)

	// ps: rows decode from the plugin's shape.
	rows, err := r.ContainerPS("docker-1")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0]["name"] != "web" || rows[0]["state"] != "running" {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[1]["image"] != "pg:16" {
		t.Fatalf("second row = %+v", rows[1])
	}

	// kubectl passthrough returns the plugin's output verbatim.
	out, err := r.ContainerKubectl("docker-1", "get pods")
	if err != nil {
		t.Fatalf("kubectl: %v", err)
	}
	if !strings.Contains(out, "web-0") {
		t.Fatalf("kubectl output = %q", out)
	}

	// exec: the plugin frames a container shell as a channel id.
	ch, err := r.ContainerExec("docker-1", "aaa111")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.HasPrefix(ch, "cexec-") {
		t.Fatalf("exec channel = %q", ch)
	}
	// exec-data: keystrokes in, output out.
	text, ended, err := r.ContainerExecData(ch, "ls\n")
	if err != nil {
		t.Fatalf("exec-data: %v", err)
	}
	if text != "shell-ok\n" || ended {
		t.Fatalf("exec-data = %q ended=%v", text, ended)
	}
	if err := r.ContainerExecClose(ch); err != nil {
		t.Fatalf("exec-close: %v", err)
	}
}

func TestContainerActionsRefusedWhenInactive(t *testing.T) {
	// AD-2 at the container surface: with the fiber Inactive (its dependency
	// missing), every container action refuses with the Inactive code instead of
	// dialing something. The message names the reason the UI shows.
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	// NO ProvideConnectionsArchive → the fake's connections.store dependency is
	// unsatisfied → the fiber stays Inactive.
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	waitFiberState(t, r.Fiber(), ctxcore.Inactive, 5*time.Second)

	if _, err := r.ContainerPS("docker-1"); err == nil {
		t.Fatal("ps must be refused while the fiber is Inactive")
	} else if !strings.Contains(err.Error(), "not active") {
		t.Fatalf("the refusal must name the cause: %v", err)
	}
	if _, err := r.ContainerKubectl("docker-1", "get pods"); err == nil {
		t.Fatal("kubectl must be refused while the fiber is Inactive")
	}
	if _, err := r.ContainerExec("docker-1", "c"); err == nil {
		t.Fatal("exec must be refused while the fiber is Inactive")
	}
}

package workbenchhost

// W-T2 acceptance: broadcast + the Safe Mode interception gate.
//
// The contract under test:
//   - broadcast is an EMISSION (AD-3): it writes N channels and tracks nothing —
//     the key surface must be untouched afterwards (zero confluence burden);
//   - the withhold decision reads the safe.mode INTERCEPTION key, and flipping
//     the key must NOT reload any fiber (Σinter is a derived realization —
//     "改策略不触发重载");
//   - per-channel failures do not undo the others: there is no undo.

import (
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

func TestBroadcastExecutesAndTracksNothing(t *testing.T) {
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)

	ch1, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatal(err)
	}
	ch2, err := r.OpenChannel(map[string]any{"host": "y"})
	if err != nil {
		t.Fatal(err)
	}
	chanSurfaceBefore := strings.Join(chanTags(store), ",")

	res := r.Broadcast([]string{ch1, ch2}, "uptime", 4096)
	if len(res.Errors) != 0 {
		t.Fatalf("broadcast errors: %v", res.Errors)
	}
	if got := res.Results[ch1]; got != "UPTIME" {
		t.Fatalf("ch1 echo = %q", got)
	}
	if got := res.Results[ch2]; got != "UPTIME" {
		t.Fatalf("ch2 echo = %q", got)
	}

	// EMISSION: the key surface is untouched — broadcast carries no tracked
	// effects, which is why it adds ZERO confluence burden (the plan's point).
	if after := strings.Join(chanTags(store), ","); after != chanSurfaceBefore {
		t.Fatalf("broadcast moved the key surface: %q → %q", chanSurfaceBefore, after)
	}
	if !store.Quiescent() {
		t.Fatal("the store did not quiesce after the broadcast")
	}
	r.Stop()
}

func TestBroadcastWithholdFollowsInterceptionKey(t *testing.T) {
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	ch, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatal(err)
	}

	// Safe Mode OFF (default): the broadcast runs.
	if r.WithholdBroadcast() {
		t.Fatal("safe mode must default to off")
	}
	if res := r.Broadcast([]string{ch}, "a", 4096); res.Results[ch] != "A" {
		t.Fatalf("off-gate broadcast = %+v", res)
	}

	// Safe Mode ON: the gate withholds — and flipping the key must NOT reload the
	// fiber (Σinter is a derived realization; the fiber's uid and state survive).
	fiberUID := f.UID()
	store.Log().Reset() // the flip's own log starts here, not Launch's
	r.SetSafeMode(true)
	if !r.WithholdBroadcast() {
		t.Fatal("the interception key must withhold")
	}
	if f.State() != ctxcore.Active || f.UID() != fiberUID {
		t.Fatalf("flipping safe.mode reloaded the fiber: uid=%s state=%s", f.UID(), f.State())
	}
	for _, e := range store.Log().Snapshot() {
		if e.Kind == "begin" || e.Kind == "leave" || e.Kind == "unload" {
			t.Fatalf("a safe.mode flip produced a lifecycle event: %+v", e)
		}
	}

	// Back OFF: the gate opens again.
	r.SetSafeMode(false)
	if r.WithholdBroadcast() {
		t.Fatal("safe mode must turn back off")
	}
	if res := r.Broadcast([]string{ch}, "b", 4096); res.Results[ch] != "B" {
		t.Fatalf("re-gated broadcast = %+v", res)
	}
	r.Stop()
}

func TestBroadcastPerChannelFailureDoesNotUndoOthers(t *testing.T) {
	// EMISSION honesty: a failure on one channel is reported per-channel; the
	// others' outputs stand. There is no undo to offer.
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	ch1, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatal(err)
	}

	res := r.Broadcast([]string{ch1, "ch-missing"}, "c", 4096)
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly the missing channel", res.Errors)
	}
	if _, ok := res.Errors["ch-missing"]; !ok {
		t.Fatalf("the missing channel is not named: %v", res.Errors)
	}
	if res.Results[ch1] != "C" {
		t.Fatalf("the healthy channel must still run: %+v", res)
	}
	r.Stop()
}

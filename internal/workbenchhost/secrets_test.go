package workbenchhost

// AD-4 acceptance: the credential reaches the plugin as a RAW STDIN BODY, with
// an erasing inverse, and the envelope never sees it.
//
// The tests pin four properties:
//   1. THE VALUE IS NOT IN ANY LINE: the fake plugin reports (from the channel
//      open) whether any JSON line it ever read contained the secret — it must
//      be false, while the SHA-256 of the body it RECEIVED proves the value did
//      arrive intact out of band.
//   2. ONE-SHOT: a second open naming the same lend is refused (the vault's Take
//      removed it on the first use).
//   3. THE ERASER RUNS: LendSecret returns it, and unloading the fiber runs the
//      accumulator's copy too — both recorded by name in ErasedSecrets().
//   4. THE DECLARATION GATE: a name the banner did not declare is refused, and a
//      plugin that receives `password` in its params refuses loudly (the leak the
//      whole mechanism exists to prevent).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

const testSecret = "hunter2-correct-horse"

func secretSetup(t *testing.T) *Runner {
	t.Helper()
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	waitFiberState(t, r.Fiber(), ctxcore.Active, 5*time.Second)
	return r
}

func TestSecretTravelsOutOfBand(t *testing.T) {
	r := secretSetup(t)

	erase, err := r.LendSecret("ssh.password", []byte(testSecret))
	if err != nil {
		t.Fatalf("lend: %v", err)
	}
	defer func() { _ = erase() }()

	// The open names the lend; the plugin answers with the body's hash and with
	// whether any line it read ever contained the value.
	var res struct {
		ChannelID     string `json:"channelId"`
		SecretSha     string `json:"secretSha"`
		SecretInLines bool   `json:"secretInLines"`
	}
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if err := p.call(context.Background(), "workbench/channel/open",
		map[string]any{"host": "x", "secret": "ssh.password"}, &res); err != nil {
		t.Fatalf("open: %v", err)
	}
	want := sha256.Sum256([]byte(testSecret))
	if res.SecretSha != hex.EncodeToString(want[:]) {
		t.Fatalf("the plugin received a different body: %q", res.SecretSha)
	}
	if res.SecretInLines {
		t.Fatal("the secret appeared in a JSON line — the lend is leaking into the envelope")
	}
}

func TestSecretLendIsOneShot(t *testing.T) {
	r := secretSetup(t)
	erase, err := r.LendSecret("ssh.password", []byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = erase() }()
	// First open consumes the lend.
	if _, err := r.OpenChannel(map[string]any{"host": "x", "secret": "ssh.password"}); err != nil {
		t.Fatalf("first open: %v", err)
	}
	// Second open must fail: the vault took the secret on the first use.
	if _, err := r.OpenChannel(map[string]any{"host": "x", "secret": "ssh.password"}); err == nil {
		t.Fatal("a second open must not reuse a consumed lend")
	} else if !strings.Contains(err.Error(), "not lent") {
		t.Fatalf("the refusal must name the cause: %v", err)
	}
}

func TestSecretLendRefusesUndeclaredName(t *testing.T) {
	r := secretSetup(t)
	if _, err := r.LendSecret("aws.secret", []byte("nope")); err == nil {
		t.Fatal("a secret the banner did not declare must be refused")
	} else if !strings.Contains(err.Error(), "did not declare") {
		t.Fatalf("the refusal must name the declaration: %v", err)
	}
}

func TestSecretEraserRunsOnLendAndOnUnload(t *testing.T) {
	r := secretSetup(t)
	erase, err := r.LendSecret("ssh.password", []byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	// The lend itself erases the host's transport copy: the receipt is recorded.
	if got := r.ErasedSecrets(); len(got) == 0 {
		t.Fatal("the lend left no erasure receipt")
	}
	// The returned eraser runs idempotently.
	if err := erase(); err != nil {
		t.Fatal(err)
	}
	if err := erase(); err != nil {
		t.Fatal("the eraser must be idempotent")
	}
	// Unloading the fiber runs the accumulator's copy of the eraser.
	r.store.Retire(r.Fiber())
	waitFiberState(t, r.Fiber(), ctxcore.Inactive, 10*time.Second)
	found := false
	for _, name := range r.ErasedSecrets() {
		if name == "ssh.password (erased)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the accumulator did not run the secret eraser: %v", r.ErasedSecrets())
	}
}

func TestPluginRefusesPasswordInParams(t *testing.T) {
	// The provider refuses a password in the open params: accepting one would
	// silently reintroduce the leak. (The fake enforces the same rule so the test
	// exercises the HOST's error surfacing.)
	r := secretSetup(t)
	if _, err := r.OpenChannel(map[string]any{"host": "x", "password": testSecret}); err == nil {
		t.Fatal("a password in the params must be refused")
	} else if strings.Contains(err.Error(), testSecret) {
		t.Fatalf("the error echoed the secret: %v", err)
	}
}

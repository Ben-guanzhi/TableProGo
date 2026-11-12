package workbenchhost

// W-T3: the shortcut library lives in HOST storage (same layer as
// connections.json), written atomically, CRUD-tested here. The run path reuses
// the broadcast gate — a shortcut is sugar over a broadcast, and the withhold
// rule must not be bypassable by naming it differently.

import (
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

func TestShortcutCRUD(t *testing.T) {
	shortcutsOverride = t.TempDir() + "/shortcuts.json"
	defer func() { shortcutsOverride = "" }()

	if err := SaveShortcut(Shortcut{Name: "df", Command: "df -h", Description: "磁盘"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveShortcut(Shortcut{Name: "uptime", Command: "uptime"}); err != nil {
		t.Fatal(err)
	}
	items, err := ListShortcuts()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	// Name-sorted for a stable picker order.
	if items[0].Name != "df" || items[1].Name != "uptime" {
		t.Fatalf("order = %v/%v", items[0].Name, items[1].Name)
	}

	// Upsert by name: the picker shows names, so a duplicate would be invisible.
	if err := SaveShortcut(Shortcut{Name: "df", Command: "df -hT"}); err != nil {
		t.Fatal(err)
	}
	items, _ = ListShortcuts()
	if len(items) != 2 {
		t.Fatalf("upsert duplicated: %+v", items)
	}
	if items[0].Command != "df -hT" {
		t.Fatalf("upsert lost the command: %+v", items[0])
	}
	if items[0].Updated == "" {
		t.Fatal("the upsert did not stamp Updated")
	}

	// Delete; deleting an absent name is visible, not silent.
	if err := DeleteShortcut("df"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteShortcut("df"); err == nil {
		t.Fatal("deleting an absent shortcut must be an error")
	}
	items, _ = ListShortcuts()
	if len(items) != 1 {
		t.Fatalf("after delete: %+v", items)
	}
}

func TestShortcutRefusesEmptyNameOrCommand(t *testing.T) {
	shortcutsOverride = t.TempDir() + "/shortcuts.json"
	defer func() { shortcutsOverride = "" }()
	if err := SaveShortcut(Shortcut{Name: "", Command: "x"}); err == nil {
		t.Fatal("an empty name must be refused")
	}
	if err := SaveShortcut(Shortcut{Name: "x", Command: ""}); err == nil {
		t.Fatal("an empty command must be refused")
	}
}

func TestRunShortcutReusesTheBroadcastGate(t *testing.T) {
	// The withhold rule lives in ONE place: Safe Mode ON withholds the shortcut
	// run exactly like a broadcast; confirmed=true bypasses ONLY as the separate
	// confirmed call, never as a flag on the gated path.
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
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	ch, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatal(err)
	}

	shortcutsOverride = t.TempDir() + "/shortcuts.json"
	defer func() { shortcutsOverride = "" }()
	if err := SaveShortcut(Shortcut{Name: "who", Command: "whoami"}); err != nil {
		t.Fatal(err)
	}

	// Gate OFF: runs immediately.
	res, err := r.RunShortcut("who", []string{ch}, false, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if res.Withheld || res.Results[ch] != "WHOAMI" {
		t.Fatalf("gate-off run = %+v", res)
	}

	// Gate ON: withheld — the shortcut does NOT bypass the gate.
	r.SetSafeMode(true)
	res, err = r.RunShortcut("who", []string{ch}, false, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Withheld {
		t.Fatalf("Safe Mode ON must withhold the shortcut: %+v", res)
	}

	// The confirmed run executes (that route IS the confirmation).
	res, err = r.RunShortcut("who", []string{ch}, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if res.Withheld || !strings.Contains(res.Results[ch], "WHOAMI") {
		t.Fatalf("confirmed run = %+v", res)
	}

	// An unknown shortcut is an error, not a silent no-op.
	if _, err := r.RunShortcut("nope", []string{ch}, true, 4096); err == nil {
		t.Fatal("an unknown shortcut must be refused")
	}
}

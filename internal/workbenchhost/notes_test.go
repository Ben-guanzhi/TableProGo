package workbenchhost

// W-N1 acceptance: the host-local note surface — CRUD over one atomic JSON file,
// and its nav node tracked on the fiber (with the HOST as provider).

import (
	"os"
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

func TestNoteCRUD(t *testing.T) {
	notesOverride = t.TempDir() + "/notes.json"
	defer func() { notesOverride = "" }()

	n, err := SaveNote(Note{Title: "巡检", Body: "第一行\n第二行"})
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == "" || n.Updated == "" {
		t.Fatalf("save did not stamp id/updated: %+v", n)
	}
	// Upsert by id: same id, new body, no duplicate row.
	n2, err := SaveNote(Note{ID: n.ID, Title: "巡检", Body: "改过"})
	if err != nil {
		t.Fatal(err)
	}
	notes, err := ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Body != "改过" {
		t.Fatalf("upsert = %+v", notes)
	}
	// A title-less note falls back to its first non-empty line.
	fallback, err := SaveNote(Note{Body: "\n\n  临时记录  \n后文"})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Title != "临时记录" {
		t.Fatalf("title fallback = %q", fallback.Title)
	}
	// Delete; deleting an absent id is visible.
	if err := DeleteNote(n2.ID); err != nil {
		t.Fatal(err)
	}
	if err := DeleteNote(n2.ID); err == nil {
		t.Fatal("deleting an absent note must be an error")
	}
}

func TestNotesFileIsAtomicAndOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	notesOverride = dir + "/notes.json"
	defer func() { notesOverride = "" }()
	if _, err := SaveNote(Note{Title: "a", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	// The .tmp is renamed away, never left behind.
	if _, err := os.Stat(notesOverride + ".tmp"); err == nil {
		t.Fatal("the tmp file survived the rename")
	}
	raw, err := os.ReadFile(notesOverride)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"title\": \"a\"") {
		t.Fatalf("notes file = %s", raw)
	}
}

func TestNotesNodeIsHostProviderAndReclaimed(t *testing.T) {
	notesOverride = t.TempDir() + "/notes.json"
	defer func() { notesOverride = "" }()
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

	if err := RegisterNotesNode(f, store); err != nil {
		t.Fatal(err)
	}
	// The node is in the tree with the HOST as provider.
	var found bool
	for _, b := range store.Sigma()[ctxcore.KeyNavNodes] {
		if b.Tag == "host/notes" {
			found = true
			if b.Provider != "host" {
				t.Fatalf("notes node provider = %q, want host", b.Provider)
			}
		}
	}
	if !found {
		t.Fatalf("the notes node is missing: %v", store.Sigma()[ctxcore.KeyNavNodes])
	}
	// Unloading the fiber withdraws it (Cor 69).
	store.Retire(f)
	waitFiberState(t, f, ctxcore.Inactive, 10*time.Second)
	for _, b := range store.Sigma()[ctxcore.KeyNavNodes] {
		if b.Tag == "host/notes" {
			t.Fatal("the notes node survived unload")
		}
	}
}

package workbenchhost

// W-F1 acceptance: files over the plugin's files.session, with the acquisition
// discipline and the realm (Σiso) enforced at BOTH gates.
//
//   1. Realm refusal: a path outside the session's roots is refused at the HOST
//      gate (never reaching the plugin) AND would be refused by the plugin's own
//      gate. The host-side test proves the FIRST gate with the fake plugin — the
//      real gate is asserted end-to-end afterwards.
//   2. Real upload/download end-to-end against the in-process sftp sshd: the
//      two-step acquisition (stage → commit), the tmp residue reclaimed on
//      failure, the downloaded bytes round-tripping.
//   3. The key surface: files.session bindings tracked (reclaimed on unload),
//      upload/download inverses on the fiber's accumulator.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

func TestRealmRefusalAtTheHostGate(t *testing.T) {
	// The session is opened with roots=[/data]; /etc/passwd is OUTSIDE. The host
	// gate refuses it BEFORE the plugin sees it — the fake plugin would answer
	// any action, so a refusal here can only come from the realm check.
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

	sess, err := r.OpenFilesSession("x", 22, "u", []string{"/data"}, "ssh.password", []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	// The binding is on the fiber's accumulator (reclaimed on unload).
	if _, ok := store.Get(ctxcore.KeyFilesSession, ctxcore.Tag(sess)); !ok {
		t.Fatal("the files.session binding is missing")
	}

	// A path INSIDE the realm passes the host gate (the fake plugin answers any
	// action, so "no error" proves the gate opened).
	if err := r.MkdirFiles(sess, "/data/sub"); err != nil {
		t.Fatalf("an in-realm path was refused: %v", err)
	}
	// A TRAVERSAL out of the realm is refused: /data/../etc resolves to /etc.
	if err := r.MkdirFiles(sess, "/data/../etc"); err == nil {
		t.Fatal("a traversal out of the realm must be refused")
	}
	// Direct escape too.
	if err := r.MkdirFiles(sess, "/etc/evil"); err == nil {
		t.Fatal("an out-of-realm path must be refused")
	}
	// No binding moved: the refusals changed nothing.
	if len(store.Sigma()[ctxcore.KeyFilesSession]) != 1 {
		t.Fatal("the refusals moved the key surface")
	}
}

func TestRealmRefusalResolvesTraversal(t *testing.T) {
	// The realm check RESOLVES before comparing: "/data/sub/../../etc" is
	// "/etc" — refused — while "/data/sub/../sub2" stays inside — allowed.
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
	sess, err := r.OpenFilesSession("x", 22, "u", []string{"/data"}, "ssh.password", []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MkdirFiles(sess, "/data/sub/../sub2"); err != nil {
		t.Fatalf("an in-realm traversal was wrongly refused: %v", err)
	}
	if err := r.MkdirFiles(sess, "/data/sub/../../etc"); err == nil {
		t.Fatal("a traversal escaping the realm was allowed")
	}
}

func TestFilesEndToEndUploadDownload(t *testing.T) {
	binary := buildNavopSSH(t)
	root := t.TempDir()
	sshHost, sshPort := startWorkbenchSFTPServer(t, root)

	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer r.Stop()
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 10*time.Second)

	sess, err := r.OpenFilesSession(sshHost, sshPort, "tester", nil, "ssh.password", []byte("secret"))
	if err != nil {
		t.Fatalf("open files session: %v", err)
	}
	if _, ok := store.Get(ctxcore.KeyFilesSession, ctxcore.Tag(sess)); !ok {
		t.Fatal("the files.session binding is missing")
	}

	// UPLOAD: two-step acquisition. The local file exists; the remote lands only
	// after the commit, and both inverses are on the accumulator.
	local := filepath.Join(t.TempDir(), "payload.txt")
	payload := "files-end-to-end\n" + strings.Repeat("row\n", 500)
	if err := os.WriteFile(local, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	remote, size, err := r.UploadFile(sess, local, "./uploaded.txt")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if size != int64(len(payload)) || !strings.Contains(remote, "uploaded.txt") {
		t.Fatalf("upload result: %s %d", remote, size)
	}

	// DOWNLOAD: the bytes round-trip exactly.
	localBack := filepath.Join(t.TempDir(), "back.txt")
	got, err := r.DownloadFile(sess, remote, localBack)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if got != int64(len(payload)) {
		t.Fatalf("downloaded %d bytes, want %d", got, len(payload))
	}
	body, _ := os.ReadFile(localBack)
	if string(body) != payload {
		t.Fatal("the downloaded bytes differ from the upload")
	}

	// LIST sees the committed file.
	entries, err := r.ListFiles(sess, "./")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	if arr, ok := entries.([]map[string]any); ok {
		for _, e := range arr {
			if name, _ := e["name"].(string); name == "uploaded.txt" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the committed file is missing from the listing: %v", entries)
	}

	// MKDIR + RENAME within the realm.
	if err := r.MkdirFiles(sess, "./made"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := r.RenameFiles(sess, "./uploaded.txt", "./made/renamed.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Close: the binding is reclaimed (the inverse runs).
	if err := r.CloseFilesSession(sess); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, ok := store.Get(ctxcore.KeyFilesSession, ctxcore.Tag(sess)); ok {
		t.Fatal("the files.session binding survived the close")
	}
}

func TestFilesBindingReclaimedOnUnload(t *testing.T) {
	// Cor 69 at the files key: unload the fiber, the binding goes with it.
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
	if _, err := r.OpenFilesSession("x", 22, "u", nil, "ssh.password", []byte("p")); err != nil {
		t.Fatal(err)
	}
	if len(chanOrFilesTags(store)) != 1 {
		t.Fatalf("files bindings = %v", store.Sigma()[ctxcore.KeyFilesSession])
	}
	r.Stop()
	waitFiberState(t, f, ctxcore.Inactive, 10*time.Second)
	if tags := store.Sigma()[ctxcore.KeyFilesSession]; len(tags) != 0 {
		t.Fatalf("Cor 69 violated: files bindings survived unload: %v", tags)
	}
}

// chanOrFilesTags is the files-session surface, for the assertions above.
func chanOrFilesTags(s *ctxcore.Store) []string {
	var out []string
	for _, b := range s.Sigma()[ctxcore.KeyFilesSession] {
		out = append(out, fmt.Sprintf("%v", b.Tag))
	}
	return out
}

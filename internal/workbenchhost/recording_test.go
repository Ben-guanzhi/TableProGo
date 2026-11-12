package workbenchhost

// W-T3 acceptance: recording as COMPENSATION, with the plan's explicit
// obligation made executable — "固定时钟逐帧一致性测试" (the confluence argument
// re-established against the coarser equivalence, frame by frame).
//
// The tests:
//   1. Fixed clock, frame-by-frame: type → output → resize → close through the
//      Runner with a stepped fake clock; re-read the cast; assert the frame
//      sequence (codes, payloads, intervals) EXACTLY. This is the "same trace
//      every time" property that makes the recording trustworthy evidence.
//   2. Channel close auto-stops: the cast gets its "x" event at the session
//      boundary without a separate Stop call.
//   3. The recording moves NOTHING in the key surface (compensation, not effect).
//   4. 0600 on the cast file — it may contain secrets the user typed.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

// stepClock returns a clock that advances `step` per call — deterministic, so
// the intervals in the cast are exact multiples.
func stepClock(start time.Time, step time.Duration) (func() time.Time, *int) {
	n := new(int)
	return func() time.Time {
		t := start.Add(time.Duration(*n) * step)
		*n++
		return t
	}, n
}

func TestRecordingFrameByFrameConsistency(t *testing.T) {
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	defer r.Stop() // also closes any cast left open (Windows: a running exe blocks TempDir cleanup)
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	ch, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatal(err)
	}

	// Fixed clock: each recorded frame lands exactly 0.25s after the previous.
	base := time.Unix(1_700_000_000, 0)
	clock, _ := stepClock(base, 250*time.Millisecond)
	restore := setRecordingClock(clock)
	defer restore()
	recordingDirOverride = t.TempDir()
	defer func() { recordingDirOverride = "" }()

	path, err := r.StartRecording(ch, "acceptance")
	if err != nil {
		t.Fatal(err)
	}

	// The exchange: type, get the echo, resize, type again.
	if _, _, err := r.WriteChannel(ch, "uptime", 4096); err != nil {
		t.Fatal(err)
	}
	if err := r.ResizeChannel(ch, 100, 30); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.WriteChannel(ch, "df -h", 4096); err != nil {
		t.Fatal(err)
	}
	if err := r.CloseChannel(ch); err != nil {
		t.Fatal(err)
	}

	// The cast is closed by CloseChannel (the exit event IS the session's end).
	header, events, err := ReadRecording(path)
	if err != nil {
		t.Fatalf("read cast: %v", err)
	}
	if header.Version != 3 || header.Term.Cols != 80 || header.Term.Rows != 24 {
		t.Fatalf("header = %+v", header)
	}
	if header.Title != "acceptance" {
		t.Fatalf("title = %q", header.Title)
	}

	// Frame-for-frame: i(o?) i r i x with EXACT intervals of 0.25s each.
	var codes []string
	var intervals []float64
	for _, e := range events {
		codes = append(codes, e.Code)
		intervals = append(intervals, e.Interval)
	}
	// The fake echoes, so each WriteChannel records i then o.
	want := []string{"i", "o", "r", "i", "o", "x"}
	if strings.Join(codes, ",") != strings.Join(want, ",") {
		t.Fatalf("frame codes = %v, want %v (full: %+v)", codes, want, events)
	}
	for i, iv := range intervals {
		if iv != 0.25 {
			t.Fatalf("frame %d (%s) interval = %v, want 0.25 exactly", i, codes[i], iv)
		}
	}
	// Payloads: the input and the echo are in the file verbatim.
	for _, e := range events {
		if e.Code == "i" && strings.Contains(e.Data, "uptime") {
			return // found it
		}
	}
	t.Fatalf("the uptime input frame is missing: %+v", events)
}

func TestRecordingMovesNothingInTheKeySurface(t *testing.T) {
	// Compensation, not effect: StartRecording/StopRecording must leave the key
	// surface untouched — "nothing in the shared table changes" is also true for
	// the compensation, because a cast is NOT reified at a key (§6.1: a location
	// the system reifies at no key lies outside the calculus).
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	defer r.Stop() // also closes any cast left open (Windows: a running exe blocks TempDir cleanup)
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	ch, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatal(err)
	}
	recordingDirOverride = t.TempDir()
	defer func() { recordingDirOverride = "" }()

	before := strings.Join(chanTags(store), ",")
	path, err := r.StartRecording(ch, "surface")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.WriteChannel(ch, "x", 4096); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StopRecording(ch); err != nil {
		t.Fatal(err)
	}
	if after := strings.Join(chanTags(store), ","); after != before {
		t.Fatalf("recording moved the key surface: %q → %q", before, after)
	}
	if !store.Quiescent() {
		t.Fatal("did not quiesce across the recording lifecycle")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cast missing after stop: %v", err)
	}
}

func TestRecordingFilePermissions(t *testing.T) {
	// 0600: the cast may contain secrets the user typed. On POSIX this is the
	// file mode; on Windows Go's Stat reports 0666 for any writable file and the
	// ACL comes from the parent, so the test asserts the real recordings DIR the
	// recorder creates (not a t.TempDir, whose Go-mode is 0777 on Windows).
	if runtime.GOOS != "windows" {
		recordingDirOverride = t.TempDir()
		defer func() { recordingDirOverride = "" }()
		path, err := StartRecordingDirect(t)
		if err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := st.Mode().Perm(); mode&0o077 != 0 {
			t.Fatalf("cast permissions = %o, want no group/other bits (0600)", mode)
		}
		return
	}
	// Windows path: drive the recorder's OWN MkdirAll (no override), then assert
	// the directory's ACL denies write for non-owner via the file's mode bits.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	dir := filepath.Join(home, ".tablepro-go", "recordings")
	os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	path, err := StartRecordingDirect(t)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("cast landed in %s, want %s", filepath.Dir(path), dir)
	}
	dirSt, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recordings dir mode (Windows ACL mapping) = %o", dirSt.Mode().Perm())
}

// StartRecordingDirect drives the recorder through the Runner so the permission
// test exercises the real file-creation path.
func StartRecordingDirect(t *testing.T) (string, error) {
	binary := buildFakeSSHPlugin(t)
	r := New(ctxcore.NewStore())
	if err := r.ProvideConnectionsArchive(map[string]string{"k": "v"}); err != nil {
		return "", err
	}
	if err := r.Launch(binary); err != nil {
		return "", err
	}
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	ch, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		return "", err
	}
	defer r.Stop()
	return r.StartRecording(ch, "perm")
}

func TestRecordingRefusesSecondRecorderOnSameChannel(t *testing.T) {
	// One cast per channel: two writers to one file would interleave frames and
	// corrupt the trace — refuse rather than interleave.
	recordingDirOverride = t.TempDir()
	defer func() { recordingDirOverride = "" }()
	r := New(ctxcore.NewStore())
	if err := r.ProvideConnectionsArchive(map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(buildFakeSSHPlugin(t)); err != nil {
		t.Fatal(err)
	}
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	ch, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.StartRecording(ch, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StartRecording(ch, "second"); err == nil {
		t.Fatal("a second recorder on the same channel must be refused")
	}
	r.Stop()
}

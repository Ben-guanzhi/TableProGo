package workbenchhost

// recorder.go — W-T3's session recorder, writing asciinema v3 (.cast).
//
// WHY RECORD: the plan's §4 names the reason. The bytes a terminal renders are
// an EMISSION (AD-3): already sent, no inverse exists. The compensation the plan
// picks is a session recording — not to undo the emission, but to keep a faithful
// trace of what was sent and what came back. The confluence argument is
// re-established "against a coarser equivalence" and recorded as an explicit
// obligation: the acceptance test below (fixed clock, frame-by-frame) is that
// obligation made executable — the same commands played back produce the same
// frames in the same time order.
//
// FORMAT: asciinema v3 (docs.asciinema.org/manual/asciicast/v3/). NDJSON:
//   header line {"version":3,"term":{"cols":80,"rows":24},...}
//   event lines [interval, code, data] — interval is seconds since the PREVIOUS
//   event, rounded to milliseconds with error diffusion (the spec's Bresenham
//   note), so long recordings do not accumulate rounding drift.
//
// EVENT MAPPING:
//   channel output frames → "o" (what the terminal printed),
//   user input frames     → "i" (what was sent — asciinema does NOT capture
//                           input by default; we do, because a COMPENSATION
//                           record without the input cannot reconstruct what
//                           happened; the plugin channel is our own, so capture
//                           is explicit rather than accidental),
//   resize                → "r" ("COLSxROWS"),
//   session end           → "x" ("0").
//
// THE CLOCK is injectable (now func): the acceptance test drives a FIXED clock
// so the frame sequence is deterministic, and the test then re-reads the file
// and asserts frame-for-frame equality of times and payloads. Real recording
// passes time.Now.
//
// FILE DISCIPLINE: 0600 permissions — a terminal recording contains whatever the
// user typed, which may include secrets they did not think of. Written atomically
// (temp + rename) like every other host file.
//
// LOCKING NOTE (from the W-T3 debugging session, kept to prevent a regression):
// event() takes r.mu ONCE and holds it for the whole body. The first hang was
// the debugging attempt's OWN fault — a TryLock followed by a second Lock on the
// same goroutine, which is a self-deadlock. If this ever hangs again, dump all
// goroutines and look for a re-entrant acquisition, not for a mysterious second
// locker.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CastHeader is the asciicast v3 header (only the fields we write/read).
type CastHeader struct {
	Version   int      `json:"version"`
	Term      CastTerm `json:"term"`
	Timestamp int64    `json:"timestamp,omitempty"`
	Title     string   `json:"title,omitempty"`
}

type CastTerm struct {
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	Type string `json:"type,omitempty"`
}

// CastEvent is one [interval, code, data] triple, in parsed form.
type CastEvent struct {
	Interval float64
	Code     string
	Data     string
}

func (e CastEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{e.Interval, e.Code, e.Data})
}

func (e *CastEvent) UnmarshalJSON(raw []byte) error {
	var triple [3]any
	if err := json.Unmarshal(raw, &triple); err != nil {
		return err
	}
	e.Interval, _ = triple[0].(float64)
	e.Code, _ = triple[1].(string)
	e.Data, _ = triple[2].(string)
	return nil
}

// Recorder writes one session to a .cast file.
//
// A single recorder records ONE channel (a cast is one terminal session). The
// runner owns one recorder per recorded channel and routes frames to it.
type Recorder struct {
	mu     sync.Mutex
	file   *os.File
	w      *bufio.Writer
	now    func() time.Time
	start  time.Time
	closed bool

	// roundingAccumulator carries the millisecond-rounding error to the next
	// interval (Bresenham), per the spec's drift note.
	roundingAccumulator float64
}

// NewRecorder creates the .cast file and writes the header. cols/rows are the
// terminal size at record start; title is shown in asciinema players.
func NewRecorder(path string, cols, rows int, title string, now func() time.Time) (*Recorder, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// 0600: the recording may contain whatever the user typed. The temp file
	// must carry the same permission or the rename publishes a world-readable
	// trace for one window.
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	r := &Recorder{
		file:  f,
		w:     bufio.NewWriter(f),
		now:   now,
		start: now(),
	}
	header := CastHeader{
		Version:   3,
		Term:      CastTerm{Cols: cols, Rows: rows, Type: "xterm-256color"},
		Timestamp: r.start.Unix(),
		Title:     title,
	}
	line, err := json.Marshal(header)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(r.w, "%s\n", line); err != nil {
		_ = f.Close()
		return nil, err
	}
	return r, nil
}

// event appends one frame. Interval = now − previous event, rounded to
// milliseconds WITH error diffusion: the spec's Bresenham note — naive per-event
// rounding accumulates drift over a long session, and a compensation record
// whose timeline drifts is not a faithful trace.
func (r *Recorder) event(code, data string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	now := r.now()
	rawInterval := now.Sub(r.start).Seconds()
	r.start = now
	acc := r.roundingAccumulator + rawInterval*1000
	ms := float64(int64(acc + 0.5))
	r.roundingAccumulator = acc - ms
	interval := ms / 1000

	line, err := json.Marshal([]any{interval, code, data})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.w, "%s\n", line)
	return err
}

// Output records an "o" frame (what the terminal printed).
func (r *Recorder) Output(data string) error { return r.event("o", data) }

// Input records an "i" frame (what was sent). asciinema does NOT capture input
// by default; we do, explicitly, because the compensation record must be able to
// show the whole exchange.
func (r *Recorder) Input(data string) error { return r.event("i", data) }

// Resize records an "r" frame ("COLSxROWS").
func (r *Recorder) Resize(cols, rows uint16) error {
	return r.event("r", fmt.Sprintf("%dx%d", cols, rows))
}

// Exit records the terminal "x" frame and flushes/renames the file into place.
// After Close the recorder refuses further events (a cast ends with its exit).
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	now := r.now()
	rawInterval := now.Sub(r.start).Seconds()
	acc := r.roundingAccumulator + rawInterval*1000
	ms := float64(int64(acc + 0.5))
	interval := ms / 1000
	line, err := json.Marshal([]any{interval, "x", "0"})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.w, "%s\n", line); err != nil {
		return err
	}
	r.closed = true
	if err := r.w.Flush(); err != nil {
		return err
	}
	if err := r.file.Sync(); err != nil {
		return err
	}
	if err := r.file.Close(); err != nil {
		return err
	}
	return os.Rename(r.file.Name(), trimTmp(r.file.Name()))
}

func trimTmp(name string) string {
	const suffix = ".tmp"
	if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
		return name[:len(name)-len(suffix)]
	}
	return name
}

// ReadRecording parses a .cast file back into header + events. The acceptance
// test uses it to prove the ROUND TRIP: what was written is what a player reads.
func ReadRecording(path string) (CastHeader, []CastEvent, error) {
	var header CastHeader
	f, err := os.Open(path)
	if err != nil {
		return header, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	first := true
	var events []CastEvent
	for sc.Scan() {
		line := sc.Text()
		if line == "" || (len(line) > 0 && line[0] == '#') {
			continue // comments are legal per the spec (never the first line)
		}
		if first {
			first = false
			if err := json.Unmarshal([]byte(line), &header); err != nil {
				return header, nil, fmt.Errorf("bad header: %w", err)
			}
			if header.Version != 3 {
				return header, nil, fmt.Errorf("want asciicast v3, got v%d", header.Version)
			}
			continue
		}
		var ev CastEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return header, nil, fmt.Errorf("bad event line: %w (%s)", err, line)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return header, nil, err
	}
	if first {
		return header, nil, fmt.Errorf("empty recording")
	}
	return header, events, nil
}

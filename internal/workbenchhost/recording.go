package workbenchhost

// Runner-side recording hooks (W-T3). The recorder is per-channel; the runner
// routes channel frames into the active one and STOPS a channel's recording when
// that channel closes — the cast's exit event is the channel's death, exactly
// like a terminal session.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// recordingDir is where casts land. Under the app's user-data dir (the same
// root as connections.json), so the user finds recordings next to their data.
var recordingDirOverride string

func recordingDir() string {
	if recordingDirOverride != "" {
		return recordingDirOverride
	}
	return userDataDirOrDefault("recordings")
}

// recordingState is one channel's active recorder.
type recordingState struct {
	rec     *Recorder
	channel string
	path    string
}

// StartRecording opens a .cast for the channel and routes its frames into it.
// Recording an ALREADY-recorded channel is refused (one cast per channel at a
// time — two writers to one file would interleave frames and corrupt the trace).
func (r *Runner) StartRecording(channel, title string) (string, error) {
	r.recMu.Lock()
	if _, active := r.recordings[channel]; active {
		r.recMu.Unlock()
		return "", fmt.Errorf("channel %s is already being recorded", channel)
	}
	r.recMu.Unlock()

	if err := os.MkdirAll(recordingDir(), 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.cast", time.Now().Format("20060102-150405"), sanitizeName(channel))
	path := filepath.Join(recordingDir(), name)
	rec, err := NewRecorder(path, 80, 24, title, func() time.Time { return nowForRecording() })
	if err != nil {
		return "", err
	}
	r.recMu.Lock()
	r.recordings[channel] = &recordingState{rec: rec, channel: channel, path: path}
	r.recMu.Unlock()
	return path, nil
}

// StopRecording closes the cast (writes the exit event, renames into place) and
// forgets the channel.
func (r *Runner) StopRecording(channel string) (string, error) {
	r.recMu.Lock()
	st, ok := r.recordings[channel]
	if !ok {
		r.recMu.Unlock()
		return "", fmt.Errorf("channel %s is not being recorded", channel)
	}
	delete(r.recordings, channel)
	r.recMu.Unlock()
	if err := st.rec.Close(); err != nil {
		return "", err
	}
	return st.path, nil
}

// RecordingPath returns the active cast path for a channel, if any.
func (r *Runner) RecordingPath(channel string) string {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	if st, ok := r.recordings[channel]; ok {
		return st.path
	}
	return ""
}

// nowForRecording is the runner's wall clock for casts. A package-level seam,
// not a global in the recorder: the acceptance test overrides it through
// setRecordingClock (the recorder itself is already clock-injectable).
var nowForRecording = time.Now

func setRecordingClock(now func() time.Time) func() time.Time {
	old := nowForRecording
	nowForRecording = now
	return old
}

// userDataDirOrDefault resolves the recordings root without importing the host
// package: workbench tests set recordingDirOverride instead.
func userDataDirOrDefault(sub string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".tablepro-go", sub)
	}
	return filepath.Join(os.TempDir(), "tablepro-go", sub)
}

// recordChannelFrame routes one frame into the channel's recorder, if recording.
// Recording failures NEVER break the channel: a cast is a compensation, and a
// compensation that breaks the thing it compensates would be worse than none.
func (r *Runner) recordChannelFrame(channel, code, data string) {
	r.recMu.Lock()
	st, ok := r.recordings[channel]
	r.recMu.Unlock()
	if !ok {
		return
	}
	var err error
	switch code {
	case "o":
		err = st.rec.Output(data)
	case "i":
		err = st.rec.Input(data)
	}
	if err != nil {
		// Degrade loudly in the log, silently in the UI: the session continues.
		fmt.Printf("workbench: recording frame for %s: %v\n", channel, err)
	}
}

// sanitizeName strips path separators and control chars from a channel id so a
// hostile channel id cannot escape the recordings directory.
func sanitizeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "session"
	}
	return string(out)
}

// stopRecordingsFor closes a channel's cast when the channel closes — the exit
// event IS the session's end (the same "x" a real terminal session writes). No
// table move happens here: a recording is compensation, not an effect.
func (r *Runner) stopRecordingsFor(channel string) {
	_, _ = r.StopRecording(channel)
}

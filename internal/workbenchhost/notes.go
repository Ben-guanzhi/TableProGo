package workbenchhost

// notes.go — W-N1: the host's local note surface (the plan's "宿主本地文档面，
// 挂 nav.nodes").
//
// Why it is HOST-side and not a plugin: a note is not a capability over an
// external system, it is the operator's own scratch space next to the data. The
// host already owns local storage (shortcuts, recordings); a plugin would add a
// process boundary to reading a text file. The note surface therefore registers
// itself in nav.nodes with the HOST as provider ("host"), the same root-side
// position as connections.store.
//
// Storage discipline mirrors shortcuts: one JSON file, atomic write (tmp+rename),
// no secrets. Notes are Markdown text with a title and an updated stamp.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"tablepro-go/internal/ctxcore"
)

// Note is one document.
type Note struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Updated string `json:"updated"`
}

// noteStore is the file-backed note set.
type noteStore struct {
	mu   sync.Mutex
	path string
}

var notesOverride string

func notesPath() string {
	if notesOverride != "" {
		return notesOverride
	}
	return filepath.Join(userDataDirOrDefault("notes"), "notes.json")
}

func newNoteStore() *noteStore { return &noteStore{path: notesPath()} }

// load reads the note set; a missing file is an EMPTY set (normal first run).
func (s *noteStore) load() ([]Note, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var notes []Note
	if err := json.Unmarshal(raw, &notes); err != nil {
		return nil, fmt.Errorf("笔记库损坏（%s）: %w", s.path, err)
	}
	return notes, nil
}

func (s *noteStore) save(notes []Note) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(notes, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ListNotes returns the notes newest-updated first (the order a scratch pad wants).
func ListNotes() ([]Note, error) {
	s := newNoteStore()
	s.mu.Lock()
	defer s.mu.Unlock()
	notes, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].Updated > notes[j].Updated })
	return notes, nil
}

// SaveNote upserts by ID. An empty ID allocates one (the first save of a new
// note); a title-less note falls back to its first line so the list stays legible.
func SaveNote(n Note) (Note, error) {
	s := newNoteStore()
	s.mu.Lock()
	defer s.mu.Unlock()
	notes, err := s.load()
	if err != nil {
		return Note{}, err
	}
	n.Updated = time.Now().Format(time.RFC3339)
	if strings.TrimSpace(n.Title) == "" {
		n.Title = firstLine(n.Body)
	}
	if n.ID == "" {
		n.ID = fmt.Sprintf("note-%d", time.Now().UnixNano())
	}
	replaced := false
	for i := range notes {
		if notes[i].ID == n.ID {
			notes[i] = n
			replaced = true
			break
		}
	}
	if !replaced {
		notes = append(notes, n)
	}
	return n, s.save(notes)
}

// DeleteNote removes one note; deleting an absent id is an error so a double
// click is visible rather than silent.
func DeleteNote(id string) error {
	s := newNoteStore()
	s.mu.Lock()
	defer s.mu.Unlock()
	notes, err := s.load()
	if err != nil {
		return err
	}
	out := notes[:0]
	found := false
	for _, n := range notes {
		if n.ID == id {
			found = true
			continue
		}
		out = append(out, n)
	}
	if !found {
		return fmt.Errorf("笔记 %q 不存在", id)
	}
	return s.save(out)
}

// RegisterNotesNode puts the note surface in the nav tree with the HOST as the
// provider — the same root-side position as connections.store. It is tracked on
// the fiber so unload withdraws the tab (Cor 69), and it costs nothing when the
// operator never opens it.
func RegisterNotesNode(f *ctxcore.Fiber, store *ctxcore.Store) error {
	if f == nil {
		return fmt.Errorf("no fiber to register the notes node on")
	}
	tag := ctxcore.Tag("host/notes")
	return store.Track(f, "notes-node", func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyNavNodes, tag, "host", map[string]any{
			"label": "笔记", "icon": "note", "surface": "notes",
		}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyNavNodes)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableRemove(ctxcore.KeyNavNodes, tag); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyNavNodes)
			return nil
		}, nil
	})
}

// firstLine is the title fallback.
func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if len(t) > 60 {
				return t[:60]
			}
			return t
		}
	}
	return "未命名"
}

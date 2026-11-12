package workbenchhost

// shortcuts.go — W-T3's quick-command library.
//
// A shortcut is a NAMED command the operator runs against terminal channels.
// It lives in HOST storage (the plan's placement: "快捷指令库属宿主存储，同
// connections.store 层") — the plugin declares terminal capability, the host
// owns the command library. The file is JSON, written atomically, and carries
// no secrets: a shortcut is a command TEMPLATE, credentials ride the session.
//
// EXECUTION IS AN EMISSION: running a shortcut sends bytes to a channel. It
// goes through the SAME withhold/confirm pair as broadcast (AD-3), which is why
// the HTTP handler reuses BroadcastConfirmed's semantics — the shortcut is
// syntactic sugar over the broadcast, and the gate must not be bypassable by
// naming it differently.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Shortcut is one named command template.
type Shortcut struct {
	Name string `json:"name"`
	// Command is sent verbatim; the runner appends the newline.
	Command string `json:"command"`
	// Description is what the UI shows in the picker.
	Description string `json:"description,omitempty"`
	// Updated is bookkeeping (who changed what when — no author field here; the
	// workbench is single-user local).
	Updated string `json:"updated,omitempty"`
}

// shortcutStore is the in-memory cache over the JSON file.
type shortcutStore struct {
	mu    sync.Mutex
	path  string
	items []Shortcut
}

var shortcutsOverride string

func shortcutsPath() string {
	if shortcutsOverride != "" {
		return shortcutsOverride
	}
	return filepath.Join(userDataDirOrDefault("shortcuts"), "shortcuts.json")
}

// loadShortcuts reads the file; a missing file is an EMPTY library (the normal
// first-run state), a corrupt one is reported loudly.
func (s *shortcutStore) load() ([]Shortcut, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []Shortcut
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("快捷指令库损坏（%s）: %w", s.path, err)
	}
	return items, nil
}

func (s *shortcutStore) save(items []Shortcut) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ListShortcuts returns the library, name-sorted for a stable picker order.
func ListShortcuts() ([]Shortcut, error) {
	s := &shortcutStore{path: shortcutsPath()}
	items, err := s.load()
	if err != nil {
		return nil, err
	}
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Name < items[j-1].Name; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	return items, nil
}

// SaveShortcut upserts by name (the shortcut's identity is its name, the way a
// command alias is). Two shortcuts with one name is an UPSERT, not a refusal —
// the picker shows names, so a duplicate would be invisible.
func SaveShortcut(sc Shortcut) error {
	if sc.Name == "" {
		return fmt.Errorf("快捷指令名称不能为空")
	}
	if sc.Command == "" {
		return fmt.Errorf("快捷指令命令不能为空")
	}
	s := &shortcutStore{path: shortcutsPath()}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range items {
		if items[i].Name == sc.Name {
			sc.Updated = time.Now().Format(time.RFC3339)
			items[i] = sc
			replaced = true
			break
		}
	}
	if !replaced {
		sc.Updated = time.Now().Format(time.RFC3339)
		items = append(items, sc)
	}
	return s.save(items)
}

// DeleteShortcut removes by name; deleting an absent name is an error so a
// double-click delete is visible instead of silent.
func DeleteShortcut(name string) error {
	s := &shortcutStore{path: shortcutsPath()}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.load()
	if err != nil {
		return err
	}
	out := items[:0]
	found := false
	for _, it := range items {
		if it.Name == name {
			found = true
			continue
		}
		out = append(out, it)
	}
	if !found {
		return fmt.Errorf("快捷指令 %q 不存在", name)
	}
	return s.save(out)
}

// RunShortcut executes one named shortcut against the given channels through
// the broadcast gate: the caller passes the CONFIRMED flag through, so the
// withhold decision stays in ONE place (broadcast.go).
func (r *Runner) RunShortcut(name string, channelIDs []string, confirmed bool, maxBytes int) (BroadcastResult, error) {
	items, err := ListShortcuts()
	if err != nil {
		return BroadcastResult{}, err
	}
	var sc *Shortcut
	for i := range items {
		if items[i].Name == name {
			sc = &items[i]
			break
		}
	}
	if sc == nil {
		return BroadcastResult{}, fmt.Errorf("快捷指令 %q 不存在", name)
	}
	if confirmed {
		return r.Broadcast(channelIDs, sc.Command, maxBytes), nil
	}
	if r.WithholdBroadcast() {
		return BroadcastResult{Withheld: true,
			Results: map[string]string{}, Errors: map[string]string{}}, nil
	}
	return r.Broadcast(channelIDs, sc.Command, maxBytes), nil
}

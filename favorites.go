package main

// Query favorites: the statements a user keeps and replays. Stored in their
// own JSON file next to the connections, capped, and independent of the
// history (history is automatic and bounded; favorites are curated).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// FavoriteEntry is one saved statement.
type FavoriteEntry struct {
	ID       string `json:"id"`
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Name     string `json:"name"`
	SQL      string `json:"sql"`
	// Folder groups favorites in the menu (P2-3); "" is the ungrouped list, so
	// an existing favorites file keeps rendering exactly as before.
	Folder    string `json:"folder,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// maxFavorites caps the file the same way history caps itself: a list the UI
// renders in one dropdown has to stay a list, not a landfill.
const maxFavorites = 200

func favoritesFilePath() string { return userDataFilePath("favorites.json") }

func (a *App) loadFavorites() {
	entries, _, err := readJSONFile[FavoriteEntry](a.favoritesPath)
	if err != nil {
		log.Printf("Failed to load favorites: %v", err)
		return
	}
	a.favorites = entries
}

func (a *App) saveFavorites() error {
	if a.favoritesPath == "" {
		return nil // struct-literal App in tests: in-memory only
	}
	data, err := json.MarshalIndent(a.favorites, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.favoritesPath, data)
}

// ListFavorites returns the saved statements, newest first.
func (a *App) ListFavorites() []FavoriteEntry {
	a.favoritesMu.Lock()
	defer a.favoritesMu.Unlock()
	// Copy in reverse append order first, so entries saved in the same
	// millisecond end up newest first after the stable descending sort.
	out := make([]FavoriteEntry, 0, len(a.favorites))
	for i := len(a.favorites) - 1; i >= 0; i-- {
		out = append(out, a.favorites[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// AddFavorite saves a statement under a user-chosen name, optionally inside a
// folder (an empty folder means the ungrouped list).
func (a *App) AddFavorite(connID, database, name, sql, folder string) (FavoriteEntry, error) {
	entry := FavoriteEntry{}
	if name == "" || sql == "" {
		return entry, fmt.Errorf("收藏需要名称与语句")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return entry, fmt.Errorf("generate favorite id: %w", err)
	}
	entry = FavoriteEntry{
		ID:        hex.EncodeToString(id[:]),
		ConnID:    connID,
		Database:  database,
		Name:      name,
		SQL:       sql,
		Folder:    strings.TrimSpace(folder),
		CreatedAt: time.Now().UnixMilli(),
	}
	a.favoritesMu.Lock()
	defer a.favoritesMu.Unlock()
	a.favorites = append(a.favorites, entry)
	if len(a.favorites) > maxFavorites {
		// Oldest first out; the list is sorted by time on the way out anyway.
		sort.Slice(a.favorites, func(i, j int) bool { return a.favorites[i].CreatedAt < a.favorites[j].CreatedAt })
		a.favorites = a.favorites[len(a.favorites)-maxFavorites:]
	}
	if err := a.saveFavorites(); err != nil {
		return FavoriteEntry{}, err
	}
	return entry, nil
}

// DeleteFavorite removes one saved statement.
func (a *App) DeleteFavorite(id string) error {
	a.favoritesMu.Lock()
	defer a.favoritesMu.Unlock()
	kept := a.favorites[:0]
	found := false
	for _, f := range a.favorites {
		if f.ID == id {
			found = true
			continue
		}
		kept = append(kept, f)
	}
	if !found {
		return fmt.Errorf("收藏不存在或已被删除")
	}
	a.favorites = kept
	return a.saveFavorites()
}

// RenameFavoriteFolder renames one folder across every entry it holds (W1-b).
// The rename is by trimmed name match, the same spelling the menu groups by.
// A new name that is empty is refused — "delete the folder" is its own
// operation (UnfileFavoriteFolder), because a rename that silently ungroups
// would surprise the user who meant to retitle.
func (a *App) RenameFavoriteFolder(from, to string) (int, error) {
	fromName := strings.TrimSpace(from)
	toName := strings.TrimSpace(to)
	if fromName == "" {
		return 0, fmt.Errorf("原文件夹名不能为空")
	}
	if toName == "" {
		return 0, fmt.Errorf("新文件夹名不能为空")
	}
	if fromName == toName {
		return 0, nil // nothing to do, not an error
	}
	a.favoritesMu.Lock()
	defer a.favoritesMu.Unlock()
	moved := 0
	for i := range a.favorites {
		if strings.TrimSpace(a.favorites[i].Folder) == fromName {
			a.favorites[i].Folder = toName
			moved++
		}
	}
	if moved == 0 {
		return 0, fmt.Errorf("文件夹不存在: %s", fromName)
	}
	return moved, a.saveFavorites()
}

// UnfileFavoriteFolder removes one folder, moving its entries back to the
// ungrouped list (W1-b). Deleting the folder deletes nothing else — the same
// semantics the db-dev reference implementation pinned.
func (a *App) UnfileFavoriteFolder(folder string) (int, error) {
	name := strings.TrimSpace(folder)
	if name == "" {
		return 0, fmt.Errorf("文件夹名不能为空")
	}
	a.favoritesMu.Lock()
	defer a.favoritesMu.Unlock()
	moved := 0
	for i := range a.favorites {
		if strings.TrimSpace(a.favorites[i].Folder) == name {
			a.favorites[i].Folder = ""
			moved++
		}
	}
	if moved == 0 {
		return 0, fmt.Errorf("文件夹不存在: %s", name)
	}
	return moved, a.saveFavorites()
}

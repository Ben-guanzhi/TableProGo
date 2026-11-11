package main

import (
	"strings"
	"testing"
)

// Favorites are curated: a name and a statement are required, the list comes
// back newest first, and a delete really removes the entry.
func TestFavoritesCRUD(t *testing.T) {
	app := &App{}
	if _, err := app.AddFavorite("", "", "", "", ""); err == nil {
		t.Fatal("empty name and sql should be refused")
	}
	first, err := app.AddFavorite("c1", "shop", "月度订单", "SELECT * FROM orders", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	second, err := app.AddFavorite("c1", "", "活跃用户", "SELECT * FROM users", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	list := app.ListFavorites()
	if len(list) != 2 || list[0].Name != "活跃用户" {
		t.Fatalf("list = %+v, want newest first", list)
	}

	if err := app.DeleteFavorite(first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := app.DeleteFavorite(first.ID); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("double delete should name the problem, got %v", err)
	}
	if got := app.ListFavorites(); len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("after delete = %+v", got)
	}
}

// P2-3: a favorite keeps its folder so the menu can group it; an empty folder
// means ungrouped, and the trimmed value is what gets stored.
func TestFavoriteFolderRoundTrips(t *testing.T) {
	app := &App{}
	saved, err := app.AddFavorite("c1", "shop", "日报", "SELECT 1", "  报表  ")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if saved.Folder != "报表" {
		t.Fatalf("folder = %q, want trimmed 报表", saved.Folder)
	}
	list := app.ListFavorites()
	if len(list) != 1 || list[0].Folder != "报表" {
		t.Fatalf("list = %+v", list)
	}
}

// W1-b: renaming a folder moves every entry it holds; unfile dissolves it
// without deleting anything; both match folders by the trimmed spelling the
// menu groups by.
func TestFavoriteFolderRenameAndUnfile(t *testing.T) {
	app := &App{}
	if _, err := app.AddFavorite("c1", "shop", "日报", "SELECT 1", "报表"); err != nil {
		t.Fatalf("add 1: %v", err)
	}
	if _, err := app.AddFavorite("c1", "shop", "周报", "SELECT 2", " 报表 "); err != nil {
		t.Fatalf("add 2: %v", err)
	}
	if _, err := app.AddFavorite("c1", "", "游离", "SELECT 3", ""); err != nil {
		t.Fatalf("add 3: %v", err)
	}

	moved, err := app.RenameFavoriteFolder("报表", "经营")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if moved != 2 {
		t.Fatalf("rename moved %d, want 2", moved)
	}
	for _, f := range app.ListFavorites() {
		if f.Name == "日报" || f.Name == "周报" {
			if f.Folder != "经营" {
				t.Fatalf("%s folder = %q, want 经营", f.Name, f.Folder)
			}
		} else if f.Folder != "" {
			t.Fatalf("ungrouped entry grew a folder: %+v", f)
		}
	}

	// Same-name rename is a no-op, not an error; empty names are refused.
	if moved, err := app.RenameFavoriteFolder("经营", "经营"); err != nil || moved != 0 {
		t.Fatalf("same-name rename = (%d, %v), want (0, nil)", moved, err)
	}
	if _, err := app.RenameFavoriteFolder("", "x"); err == nil {
		t.Fatal("empty from should be refused")
	}
	if _, err := app.RenameFavoriteFolder("经营", ""); err == nil {
		t.Fatal("empty to should be refused")
	}
	if _, err := app.RenameFavoriteFolder("不存在", "x"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("unknown folder should say so, got %v", err)
	}

	moved, err = app.UnfileFavoriteFolder("经营")
	if err != nil {
		t.Fatalf("unfile: %v", err)
	}
	if moved != 2 {
		t.Fatalf("unfile moved %d, want 2", moved)
	}
	for _, f := range app.ListFavorites() {
		if f.Folder != "" {
			t.Fatalf("after unfile every entry is ungrouped, got %+v", f)
		}
	}
	if _, err := app.UnfileFavoriteFolder("经营"); err == nil {
		t.Fatal("unfile of a missing folder should fail")
	}
}

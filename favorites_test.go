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

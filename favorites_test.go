package main

import (
	"strings"
	"testing"
)

// Favorites are curated: a name and a statement are required, the list comes
// back newest first, and a delete really removes the entry.
func TestFavoritesCRUD(t *testing.T) {
	app := &App{}
	if _, err := app.AddFavorite("", "", "", ""); err == nil {
		t.Fatal("empty name and sql should be refused")
	}
	first, err := app.AddFavorite("c1", "shop", "月度订单", "SELECT * FROM orders")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	second, err := app.AddFavorite("c1", "", "活跃用户", "SELECT * FROM users")
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

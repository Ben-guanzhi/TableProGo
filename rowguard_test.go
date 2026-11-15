package main

// 乐观并发守卫的客户端侧（TableProGo）：UPDATE 的 WHERE 除了主键，还要钉住被改
// 列读到的旧值。这样才能发现"别人在我读完之后改了这一行"——否则主键仍然匹配，
// 本次更新会静默覆盖对方的改动。
//
// 三个不变量：
//   1. 守卫用 Original（读到的整行）里的值，不是 Edits 里的新值；
//   2. 主键列不重复进守卫（主键谓词已经钉住它）；
//   3. 没改的行不产生守卫，DELETE 保持只按主键（找到一行不是拒绝删除它的理由）。

import (
	"strings"
	"testing"
)

// A guarded UPDATE pins the edited column to the value the grid read.
func TestUpdateGuardsTheEditedColumn(t *testing.T) {
	statements, err := buildPlan("sqlite", `"t"`,
		RowChange{Original: []interface{}{1.0, "a", "10"}, Edits: []CellEdit{{Column: "name", Value: "A"}}},
	)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if len(statements) != 1 {
		t.Fatalf("statements = %v", statements)
	}
	want := `UPDATE "t" SET "name" = 'A' WHERE "id" = 1 AND "name" = 'a'`
	if statements[0] != want {
		t.Errorf("guarded update =\n%s\nwant\n%s", statements[0], want)
	}
}

// A NULL that was read is guarded with IS NULL; `= NULL` never matches and would
// turn a legitimate edit into a permanent conflict.
func TestUpdateGuardComparesNullAsNull(t *testing.T) {
	statements, err := buildPlan("sqlite", `"t"`,
		RowChange{Original: []interface{}{1.0, nil, "10"}, Edits: []CellEdit{{Column: "name", Value: "A"}}},
	)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if !strings.Contains(statements[0], `"name" IS NULL`) {
		t.Errorf("NULL guard = %s, want IS NULL", statements[0])
	}
	if strings.Contains(statements[0], "= NULL") {
		t.Errorf("guarded with '= NULL', which never matches: %s", statements[0])
	}
}

// The key column is already pinned by the key predicate; repeating it in the
// guard would only lengthen the statement the user reviews.
func TestUpdateGuardSkipsKeyColumns(t *testing.T) {
	// Editing the key itself: the WHERE must name "id" once.
	statements, err := buildPlan("sqlite", `"t"`,
		RowChange{Original: []interface{}{1.0, "a", "10"}, Edits: []CellEdit{{Column: "id", Value: 2}}},
	)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if got := strings.Count(statements[0], `"id"`); got != 2 {
		// once in SET, once in WHERE — never a third time in the guard
		t.Errorf("statement names \"id\" %d times: %s", got, statements[0])
	}
}

// Deleting a row someone edited concurrently is still a legitimate delete, so the
// DELETE predicate stays key-only.
func TestDeleteStaysKeyOnly(t *testing.T) {
	statements, err := buildPlan("sqlite", `"t"`,
		RowChange{Kind: "delete", Original: []interface{}{1.0, "a", "10"}},
	)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	want := `DELETE FROM "t" WHERE "id" = 1`
	if statements[0] != want {
		t.Errorf("delete =\n%s\nwant\n%s", statements[0], want)
	}
}

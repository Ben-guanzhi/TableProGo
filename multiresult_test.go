package main

// 多结果集合成（TableProGo）：首集保持主 result，其余进 Sets——单集路径
// 完全不变。这是"能不能在 UI 切换多 grid"的数据侧前提。

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
)

func contextDeadline() error { return errors.New("deadline") }

func TestMergeMultiResult(t *testing.T) {
	first := &dbcore.QueryResult{
		Columns: []dbcore.ColumnInfo{{Name: "id"}},
		Rows:    [][]dbcore.Value{{{Kind: dbcore.ValText, S: "1"}}},
	}
	second := &dbcore.QueryResult{
		Columns: []dbcore.ColumnInfo{{Name: "name"}},
		Rows:    [][]dbcore.Value{{{Kind: dbcore.ValText, S: "Alice"}}},
	}
	out := mergeMultiResult([]*dbcore.QueryResult{first, second}, nil)
	if out.Error != "" || len(out.Columns) != 1 || out.Columns[0].Name != "id" {
		t.Fatalf("first set not primary: %+v", out)
	}
	if len(out.Sets) != 1 || out.Sets[0].Columns[0].Name != "name" ||
		fmt.Sprint(out.Sets[0].Rows[0][0]) != "Alice" {
		t.Fatalf("second set wrong: %+v", out.Sets)
	}
	// Single set: no Sets (unchanged path).
	single := mergeMultiResult([]*dbcore.QueryResult{first}, nil)
	if single.Sets != nil {
		t.Fatalf("single set should not carry Sets: %+v", single.Sets)
	}
}

func TestMergeMultiResultError(t *testing.T) {
	out := mergeMultiResult(nil, contextDeadline())
	if out.Error == "" {
		t.Fatal("error must propagate when no sets")
	}
	// Partial sets with an error: keep what was read AND surface the error.
	// The kernel returns the sets it read before the failure; the primary grid
	// stays readable and the error is attached to the last set.
	first := &dbcore.QueryResult{Columns: []dbcore.ColumnInfo{{Name: "id"}}}
	out = mergeMultiResult([]*dbcore.QueryResult{first}, contextDeadline())
	// One set + error: the only grid carries both the columns and the error.
	if len(out.Columns) != 1 || out.Error == "" {
		t.Fatalf("single set + error wrong: %+v", out)
	}
	// Two sets + error: second set carries the error, first stays clean.
	second := &dbcore.QueryResult{Columns: []dbcore.ColumnInfo{{Name: "name"}}}
	out = mergeMultiResult([]*dbcore.QueryResult{first, second}, contextDeadline())
	if len(out.Sets) != 1 || out.Sets[0].Error == "" {
		t.Fatalf("mid-walk error must surface on the last set: %+v", out.Sets)
	}
}

package importfile

import (
	"fmt"
	"slices"
)

// ColumnMapSpec is one user-supplied file-column → table-column assignment.
// An empty Target means the source column is skipped.
type ColumnMapSpec struct {
	Source string
	Target string
}

// ValidateColumnMap checks an explicit (authoritative) mapping against the
// file's columns before anything is written:
//
//   - a Source that is not a file column is an error, so a typo cannot silently
//     drop a column;
//   - two specs cannot name the same Target, which would make the INSERT
//     ambiguous;
//   - skipping every source leaves nothing to import.
//
// An empty specs slice is not a mapping and validates to nil: the caller then
// falls back to auto-matching.
func ValidateColumnMap(specs []ColumnMapSpec, fileCols []string) error {
	if len(specs) == 0 {
		return nil
	}
	known := make(map[string]bool, len(fileCols))
	for _, c := range fileCols {
		known[c] = true
	}
	targets := make(map[string]bool)
	kept := 0
	for i, s := range specs {
		if !known[s.Source] {
			return fmt.Errorf("import: 映射的源列 %q 不在文件列中（第 %d 条映射）", s.Source, i+1)
		}
		if s.Target == "" {
			continue
		}
		if targets[s.Target] {
			return fmt.Errorf("import: 映射目标列 %q 被使用了多次（第 %d 条映射）", s.Target, i+1)
		}
		targets[s.Target] = true
		kept++
	}
	if kept == 0 {
		return fmt.Errorf("import: 所有源列都被跳过，没有可导入的列")
	}
	return nil
}

// BuildColumnMap applies an authoritative mapping to the file columns. Columns
// the mapping does not mention are skipped, which is what makes a non-empty
// mapping authoritative rather than a set of overrides. It returns the target
// column names in file order and the matching file indexes (so each row can be
// projected onto the kept columns).
func BuildColumnMap(specs []ColumnMapSpec, fileCols []string) (cols []string, keep []int) {
	bySource := make(map[string]ColumnMapSpec, len(specs))
	for _, s := range specs {
		bySource[s.Source] = s
	}
	for i, c := range fileCols {
		s, ok := bySource[c]
		if !ok || s.Target == "" {
			continue
		}
		cols = append(cols, s.Target)
		keep = append(keep, i)
	}
	return cols, keep
}

// MappedColumns renders an authoritative mapping as the reporting shape
// ImportResult.Mapped uses (one entry per file column, Table empty when the
// column is skipped).
func MappedColumns(specs []ColumnMapSpec, fileCols []string) []ColumnMap {
	bySource := make(map[string]ColumnMapSpec, len(specs))
	for _, s := range specs {
		bySource[s.Source] = s
	}
	out := make([]ColumnMap, 0, len(fileCols))
	for _, c := range fileCols {
		s := bySource[c]
		out = append(out, ColumnMap{File: c, Table: s.Target})
	}
	return out
}

// DropSources clears the mapping entries for the named source columns, so the
// auto-matched pairing can honour SkipColumns without recomputing the match.
func DropSources(mapping []ColumnMap, skip []string) {
	if len(skip) == 0 {
		return
	}
	for i := range mapping {
		if slices.Contains(skip, mapping[i].File) {
			mapping[i].Table = ""
		}
	}
}

// KeptIndexes returns the file indexes whose mapping has a target, in file
// order.
func KeptIndexes(mapping []ColumnMap) []int {
	var keep []int
	for i := range mapping {
		if mapping[i].Table != "" {
			keep = append(keep, i)
		}
	}
	return keep
}

// KeptTargets returns the target names, in the order they appear in mapping.
func KeptTargets(mapping []ColumnMap) []string {
	var cols []string
	for _, m := range mapping {
		if m.Table != "" {
			cols = append(cols, m.Table)
		}
	}
	return cols
}

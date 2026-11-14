package parity

import "sort"

// Entry represents a table and its row count in a data inventory.
type Entry struct {
	Table string
	Rows  int
}

// Diff represents a discrepancy where a table's row count has shrunk
// or a table has been removed entirely.
type Diff struct {
	Table string
	Old   int
	Now   int
}

// CompareInventory compares two inventory slices and returns a slice of Diffs
// for every table whose row count in now is lower than in old, and for every
// old table missing from now. The result is sorted by table name.
func CompareInventory(old, now []Entry) []Diff {
	nowMap := make(map[string]int)
	for _, e := range now {
		nowMap[e.Table] = e.Rows
	}

	var diffs []Diff
	for _, e := range old {
		nowRows, exists := nowMap[e.Table]
		if !exists {
			diffs = append(diffs, Diff{Table: e.Table, Old: e.Rows, Now: 0})
		} else if nowRows < e.Rows {
			diffs = append(diffs, Diff{Table: e.Table, Old: e.Rows, Now: nowRows})
		}
	}

	sort.Slice(diffs, func(i, j int) bool {
		return diffs[i].Table < diffs[j].Table
	})

	return diffs
}

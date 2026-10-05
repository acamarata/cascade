// Package violation (join_violation.go) is a seeded-violation fixture for
// the private-planning gate (Constitution C6): a path built from separate
// filepath.Join segments that names a private planning directory. It lives
// under testdata/, so it is never built or linted, and the gate's real-tree
// walk skips it; only the seeded test scans it.
package violation

import "path/filepath"

// PlanningInventory reads a private planning document by Join segments.
func PlanningInventory(root string) string {
	return filepath.Join(root, ".claude", "planning", "p1", "inventory.md")
}

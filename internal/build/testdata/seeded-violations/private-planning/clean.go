// Package violation (clean.go) must NOT trip the private-planning gate:
// product use of .claude (harness projection, hygiene patterns) stays
// legal. The comment below names .claude/planning and must never fire,
// because comments are not scanned.
//
//	.claude/planning/p1/phase
package violation

import "path/filepath"

// Allowed names product-legal .claude paths, and a Join whose second
// segment is a variable (a known limit of the scan, not a hit).
func Allowed(root, dir string) []string {
	return []string{
		filepath.Join(root, ".claude", "hygiene"),
		filepath.Join(root, ".claude", "CLAUDE.md"),
		filepath.Join(root, ".claude", dir),
		".claude",
	}
}

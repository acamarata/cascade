package migration

import (
	"fmt"
	"strings"
)

// Purpose: a stdlib-only line-level diff for one drift entry's DiffBody.
// Inputs: two strings (on-disk content, freshly generated content).
// Outputs: added/removed line counts and a unified-style body (one output
//
//	line per input line, prefixed '+', '-' or ' ').
//
// Constraints: no bare time/rand; deterministic and pure (Art.7.3). The
//
//	textbook O(n*m) LCS table is used rather than a third-party diff
//	dependency: inputs here are single instruction files (at most a few
//	hundred lines), so the quadratic cost never matters in practice, and a
//	stdlib-only implementation adds nothing for R-14 dependency review to
//	track.
//
// SPORT: internal/migration [ADD] (P1-E26-W10-S53-T3 sport_updates).

// diffOpKind is one diff line's disposition: unchanged, added, or removed.
type diffOpKind byte

const (
	opSame   diffOpKind = ' '
	opAdd    diffOpKind = '+'
	opRemove diffOpKind = '-'
)

// diffOp is one rendered diff line.
type diffOp struct {
	kind diffOpKind
	text string
}

// diffLines computes a line-based diff between oldText and newText.
func diffLines(oldText, newText string) (added, removed int, body string) {
	oldLines := splitKeepEmpty(oldText)
	newLines := splitKeepEmpty(newText)
	table := lcsTable(oldLines, newLines)
	ops := backtrackDiff(oldLines, newLines, table)

	var b strings.Builder
	for _, op := range ops {
		switch op.kind {
		case opAdd:
			added++
		case opRemove:
			removed++
		case opSame:
			// no count change; falls through to the rendered line below.
		}
		fmt.Fprintf(&b, "%c%s\n", byte(op.kind), op.text)
	}
	return added, removed, b.String()
}

// splitKeepEmpty splits s on newlines, ignoring one trailing newline (so a
// file ending in "\n" is not reported as having a spurious empty final
// line); an empty string yields a nil (zero-length) slice.
func splitKeepEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// lcsTable builds the standard longest-common-subsequence length table for
// a and b, suffix-anchored (table[i][j] is the LCS length of a[i:] and
// b[j:]) so backtrackDiff can walk it forward from (0,0).
func lcsTable(a, b []string) [][]int {
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				table[i][j] = table[i+1][j+1] + 1
			case table[i+1][j] >= table[i][j+1]:
				table[i][j] = table[i+1][j]
			default:
				table[i][j] = table[i][j+1]
			}
		}
	}
	return table
}

// backtrackDiff walks lcs forward from (0,0), emitting a same/remove/add
// op at every step; a tie between "remove next" and "add next" (equal LCS
// length either way) prefers remove, matching the common unified-diff
// convention of showing deletions before insertions at the same position.
func backtrackDiff(a, b []string, lcs [][]int) []diffOp {
	var ops []diffOp
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{opSame, a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{opRemove, a[i]})
			i++
		default:
			ops = append(ops, diffOp{opAdd, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{opRemove, a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{opAdd, b[j]})
	}
	return ops
}

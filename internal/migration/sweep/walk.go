// Purpose: read the caller-supplied tracked-file list, match every term
// against it with the boundary rule outputs.md and the packet's term-forms
// contract require, and resolve the module root when --root is not given.
// Split out of sweep.go (which owns the term model) purely to keep both
// files under the 300-line cap (Art.10.3).
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/migration/tripwire"
)

// readFilesList reads pathArg (a NUL-separated `git ls-files -z` dump, "-"
// for stdin, or a process-substitution path) and returns its non-empty
// entries. A missing/unreadable file or an entirely empty list is an
// error (exit 2) — the sweep never falls back to scanning nothing.
func readFilesList(pathArg string) ([]string, error) {
	var data []byte
	var err error
	if pathArg == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(pathArg) //nolint:gosec // caller-supplied tracked-file list path
	}
	if err != nil {
		return nil, fmt.Errorf("sweep: reading --files %s: %w", pathArg, err)
	}
	var out []string
	for _, part := range bytes.Split(data, []byte{0}) {
		if len(part) == 0 {
			continue
		}
		out = append(out, filepath.ToSlash(string(part)))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sweep: --files %s lists no entries", pathArg)
	}
	return out, nil
}

// isBoundaryChar reports whether b would extend a term match: the term
// may be preceded or followed by none of [A-Za-z0-9_-], so `cascade:x`, an
// `| x |` table cell and `x` in prose all match while `xy` and `x-y` do
// not. Go's RE2 engine has no lookaround, so this boundary is checked by
// hand rather than through \b (which disagrees with this rule on '-').
func isBoundaryChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return true
	default:
		return false
	}
}

// findTermMatches returns every non-overlapping, boundary-respecting
// occurrence of term's start offset within line (case-sensitive literal
// match).
func findTermMatches(line, term string) []int {
	var idxs []int
	start := 0
	for start <= len(line)-len(term) {
		i := strings.Index(line[start:], term)
		if i < 0 {
			break
		}
		pos := start + i
		before := pos == 0 || !isBoundaryChar(line[pos-1])
		afterPos := pos + len(term)
		after := afterPos >= len(line) || !isBoundaryChar(line[afterPos])
		if before && after {
			idxs = append(idxs, pos)
		}
		start = pos + 1
	}
	return idxs
}

// Sweeper scans Files (an already-filterTracked()'d list) under Root for
// every Terms entry, recording one Hit per (term, file, line) occurrence.
// It does no AST parsing and no semantic filtering; resolver.go and
// writer.go own classification and output.
type Sweeper struct {
	Root  string
	Terms []Term
	Files []string
}

// Scan reads every s.Files entry and text-matches every s.Terms entry
// against it, returning hits in deterministic (path, line, term) order. A
// file containing a NUL byte is skipped (treated as binary, never
// scanned); an otherwise-unreadable listed file is an error naming the
// path (exit 2) — the sweep trusts the caller's tracked-file list and
// never silently drops a file it cannot read.
func (s Sweeper) Scan() ([]Hit, error) {
	var hits []Hit
	for _, rel := range s.Files {
		full := filepath.Join(s.Root, filepath.FromSlash(rel))
		data, err := os.ReadFile(full) //nolint:gosec // walking the caller-supplied tracked file list
		if err != nil {
			return nil, fmt.Errorf("sweep: reading %s: %w", rel, err)
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		fileHits, err := scanContent(data, rel, s.Terms)
		if err != nil {
			return nil, err
		}
		hits = append(hits, fileHits...)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		if hits[i].Line != hits[j].Line {
			return hits[i].Line < hits[j].Line
		}
		return hits[i].Term.Term < hits[j].Term.Term
	})
	return hits, nil
}

// scanContent line-scans data (one already-read file's bytes) for every
// term's boundary-respecting literal matches.
func scanContent(data []byte, rel string, terms []Term) ([]Hit, error) {
	var hits []Hit
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		for _, t := range terms {
			for range findTermMatches(text, t.Term) {
				hits = append(hits, Hit{Term: t, Path: rel, Line: line})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sweep: scanning %s: %w", rel, err)
	}
	return hits, nil
}

// repoRoot walks up from the working directory for go.mod — the same
// idiom internal/build's own gates use for their module root. Unlike
// every internal/*/gen tool's git-based repoRoot() (individually
// os/exec-allowlisted), this package shells out to nothing in
// non-test code (TestArchOsExecAllowlist).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("sweep: resolving working directory: %w", err)
	}
	if root, ok := tripwire.ModuleRoot(dir); ok {
		return root, nil
	}
	return "", fmt.Errorf("sweep: no cascade go.mod found walking up from %s", dir)
}

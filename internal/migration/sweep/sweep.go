// Package main implements the v1-legacy-pointer sweep: a local
// engineering tool (never a shipped `cascade` subcommand) that scans the
// tracked v2 source tree for textual references to retired v1 CLI
// commands, config keys, and design-provenance markers, classifies each
// (term, path) pair against the committed term file's own replacement
// declarations, and emits a decommission-list inventory plus a
// machine-readable unresolved-pointer file.
//
// Purpose: prevent a v1 dependency or behavior from being silently
// dropped rather than explicitly superseded (06-FORGE-SPEC §1's
// lose-nothing invariant), and prevent a legitimate read-only
// design-provenance citation of the v1 archive from being flagged as an
// unresolved pointer. Every classification input is public: the term set,
// the provenance refs and the resolver logic all live in this package and
// its committed testdata, never in private planning.
// Inputs: internal/migration/sweep/testdata/v1-terms.json (the term set,
// with each term's own replacement/anchor/state), testdata/
// provenance-refs.json (the accepted provenance citations), and the
// caller-supplied --files list (a `git ls-files -z` dump of the tracked
// tree).
// Outputs: docs/migration/v1-pointer-inventory.md and
// docs/migration/unresolved.json (unless --dry-run), plus a process exit
// code: 0 success, 2 missing/malformed input, 3 UNRESOLVED rows present
// under --fail-on-unresolved.
// Constraints: purely textual (no AST parsing); never deletes a v1
// artifact; never invents a pointer list — every row traces to a real hit
// in the caller-supplied tracked-file list, never a filesystem walk (the
// sweep may not exec git itself and cannot see tracking state on its
// own). Resolves its own repo root by walking up for go.mod, never git
// (os/exec closed to this package's non-test files, TestArchOsExecAllowlist).
// Writes only through internal/output.Writer, never os.Stdout/os.Stderr
// directly (not output-gate-exempt; Run() is the sole output.NewDefault
// caller). This file owns the term model, its validation, and
// the walk-root/exclusion rules; walk.go owns the file-list/matching
// mechanics and cli.go owns flag parsing and main().
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Term is one entry of v1-terms.json: a retired v1 surface name (or a
// leftover design-provenance marker) to scan for, and this ticket's own
// declaration of how it resolves — never a pointer into private planning.
// Class is one of "command", "config_key" or "marker". Replacement is the
// slash-separated, module-root-relative path of the tracked regular
// file(s) that implement the v2 behaviour (empty when nothing has been
// declared yet); Anchor is a literal (a cobra Use string, a func or const
// name) that must appear in every replacement file once State is "done";
// State is "done", "planned" or "" (empty iff Replacement is empty).
type Term struct {
	Term        string   `json:"term"`
	Class       string   `json:"class"`
	Replacement []string `json:"replacement"`
	Anchor      string   `json:"anchor"`
	State       string   `json:"state"`
	Note        string   `json:"note,omitempty"`
}

// validClasses is the closed set of Term.Class values; a term file naming
// any other class is malformed input (exit 2).
var validClasses = map[string]bool{"command": true, "config_key": true, "marker": true}

// validStates is the closed set of Term.State values.
var validStates = map[string]bool{"": true, "done": true, "planned": true}

// validReplacementPath refuses an absolute or rooted path, a volume-qualified
// path, a ".." component, or any path routed through .claude or testdata — a
// replacement is always a real, tracked, non-fixture v2 source file.
//
// filepath.IsAbs alone is not enough: on windows "/etc/passwd", `\x` and
// "C:x" are all non-absolute, so rooted and volume forms are refused
// explicitly, and components are split on both separators on every OS.
// Each component must then pass validComponent.
func validReplacementPath(p string) bool {
	if p == "" || filepath.IsAbs(p) || filepath.VolumeName(p) != "" ||
		strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		(len(p) >= 2 && p[1] == ':') {
		return false
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if !validComponent(part) {
			return false
		}
	}
	return true
}

// componentChars is the closed alphabet of a replacement-path component.
// Anything outside it is refused before any other rule runs: ':' (NTFS
// alternate streams), '~' (8.3 short names), spaces, '$' and every
// non-ASCII rune, so no Unicode folding or normalization question arises.
var componentChars = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// protectedComponents are the directory names a replacement may never pass
// through, compared case-insensitively after trailing dots and spaces are
// trimmed (case-insensitive filesystems and windows name normalization
// would otherwise reach them through ".CLAUDE" or ".claude.").
var protectedComponents = []string{"..", ".claude", "testdata"}

// reservedDeviceNames are the windows device names that open a device, not
// a file, whatever their case or extension.
var reservedDeviceNames = []string{
	"CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
	"COM0", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"LPT0", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
}

// validComponent is fail-closed: a component passes only when it is made of
// componentChars, is not only dots, names no protected directory after
// folding and trimming, and does not name a reserved device before its
// first '.'.
func validComponent(part string) bool {
	if !componentChars.MatchString(part) {
		return false
	}
	trimmed := strings.TrimRight(part, ". ")
	if trimmed == "" {
		return false
	}
	for _, name := range protectedComponents {
		if part == name || strings.EqualFold(trimmed, name) {
			return false
		}
	}
	stem, _, _ := strings.Cut(part, ".")
	stem = strings.TrimRight(stem, " ")
	for _, name := range reservedDeviceNames {
		if strings.EqualFold(stem, name) {
			return false
		}
	}
	return true
}

// LoadTermFile parses path's {"v1_source": str, "terms": [...]} shape.
// Fails closed: a missing file, malformed JSON, empty term list, empty
// term text, unknown class, duplicate term, a state/replacement shape
// mismatch, a "done" term without an anchor, a marker declaring a
// replacement, or an invalid replacement path is an error — never a
// silently empty or partially-trusted sweep.
func LoadTermFile(path string) ([]Term, error) {
	data, err := os.ReadFile(path) //nolint:gosec // repo-relative committed testdata path
	if err != nil {
		return nil, fmt.Errorf("sweep: loading term file %s: %w", path, err)
	}
	var parsed struct {
		V1Source string `json:"v1_source"`
		Terms    []Term `json:"terms"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("sweep: parsing term file %s: %w", path, err)
	}
	if len(parsed.Terms) == 0 {
		return nil, fmt.Errorf("sweep: %s declares zero terms (fail closed)", path)
	}
	if err := validateTerms(path, parsed.Terms); err != nil {
		return nil, err
	}
	return parsed.Terms, nil
}

// validateTerms enforces every per-entry and cross-entry shape rule
// LoadTermFile's doc comment lists, split out so LoadTermFile stays under
// the funlen cap.
func validateTerms(path string, terms []Term) error {
	seen := map[string]bool{}
	for i, t := range terms {
		if strings.TrimSpace(t.Term) == "" {
			return fmt.Errorf("sweep: %s entry %d has an empty term", path, i)
		}
		if !validClasses[t.Class] {
			return fmt.Errorf("sweep: %s entry %d (%q) has unknown class %q", path, i, t.Term, t.Class)
		}
		if seen[t.Term] {
			return fmt.Errorf("sweep: %s has duplicate term %q", path, t.Term)
		}
		seen[t.Term] = true
		if !validStates[t.State] {
			return fmt.Errorf("sweep: %s entry %q has unknown state %q", path, t.Term, t.State)
		}
		if (t.State == "") != (len(t.Replacement) == 0) {
			return fmt.Errorf("sweep: %s entry %q: state %q and replacement %v disagree", path, t.Term, t.State, t.Replacement)
		}
		if t.State == "done" && t.Anchor == "" {
			return fmt.Errorf("sweep: %s entry %q: state done requires a non-empty anchor", path, t.Term)
		}
		if t.Class == "marker" && len(t.Replacement) > 0 {
			return fmt.Errorf("sweep: %s entry %q: a marker term may not declare a replacement", path, t.Term)
		}
		for _, rp := range t.Replacement {
			if !validReplacementPath(rp) {
				return fmt.Errorf("sweep: %s entry %q: invalid replacement path %q", path, t.Term, rp)
			}
		}
	}
	return nil
}

// Hit is one textual match of a Term inside a scanned file.
type Hit struct {
	Term Term
	Path string // relative to root, slash-separated
	Line int
}

// dirRoots are the v2 source directories the sweep will consider,
// relative to --root; a tracked file elsewhere (e.g. CHANGELOG.md, an
// xcodegen output, anything untracked) can never enter the inventory.
var dirRoots = []string{"cmd", "internal", "pkg", "providers", "plugins", "docs", "apps", ".github/wiki"}

// fileRoots are individual top-level tracked files the sweep also
// considers, outside of any dirRoots entry.
var fileRoots = map[string]bool{"README.md": true}

// sweepOutputs are the sweep's own committed outputs, excluded so a run
// never scans (and double-counts) its own prior output.
var sweepOutputs = map[string]bool{
	"docs/migration/v1-pointer-inventory.md": true,
	"docs/migration/unresolved.json":         true,
}

// underRoot reports whether rel (slash-separated, root-relative) falls
// under a declared walk root.
func underRoot(rel string) bool {
	if fileRoots[rel] {
		return true
	}
	for _, d := range dirRoots {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// excluded reports whether relPath should be skipped even though it is
// under a walk root: any path component named "testdata" or ".claude",
// the sweep's own output files, or a file named CHANGELOG.md anywhere
// (an append-only release record — rewriting history is not
// decommissioning, and its v1 mentions are provenance by definition).
func excluded(relPath string) bool {
	if sweepOutputs[relPath] {
		return true
	}
	parts := strings.Split(relPath, "/")
	if parts[len(parts)-1] == "CHANGELOG.md" {
		return true
	}
	for _, part := range parts {
		if part == "testdata" || part == ".claude" {
			return true
		}
	}
	return false
}

// filterTracked narrows rawFiles (as --files supplied them, already
// slash-separated) to the walked set: under a declared root and not
// excluded(). The sweep never consults the filesystem to decide this —
// only the caller-supplied tracked list.
func filterTracked(rawFiles []string) []string {
	var out []string
	for _, f := range rawFiles {
		f = filepath.ToSlash(f)
		if underRoot(f) && !excluded(f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

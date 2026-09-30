// Purpose: classify each sweep Term against its own committed
// declaration: PROVENANCE (an accepted read-only v1-archive citation at a
// specific path), DONE (every declared replacement file exists and holds
// the anchor literal), OPEN (state "planned", not yet stale) or
// UNRESOLVED (no declared replacement).
// Inputs: the Term's own Replacement/Anchor/State fields and the real v2
// replacement files under root — never a planning-corpus lookup.
// Outputs: a Disposition per term (shared by every hit of that term,
// regardless of which path it was found at — the term file, not the hit
// site, decides state).
// Constraints: a "done" term whose replacement is missing or lacks its
// anchor is an input error (exit 2), never silently downgraded to OPEN or
// UNRESOLVED; a "planned" term whose replacement is already fully present
// with its anchor is stale (exit 2) — the term file must be advanced to
// "done" instead of left behind.
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Disposition is one term's classification against its own declaration.
type Disposition string

const (
	DispositionDone       Disposition = "DONE"
	DispositionOpen       Disposition = "OPEN"
	DispositionUnresolved Disposition = "UNRESOLVED"
	DispositionProvenance Disposition = "PROVENANCE"
)

// ProvenanceRef is one accepted (path, term) citation of the v1 archive as
// design provenance, never a dropped behaviour: a hit whose (path, term)
// is listed here classifies PROVENANCE, decommission-safe "no", and is
// excluded from unresolved.json. A listed pair with no matching hit in the
// walked tree is a stale entry (run() exits 2 on it).
type ProvenanceRef struct {
	Path   string `json:"path"`
	Term   string `json:"term"`
	Reason string `json:"reason"`
}

// LoadProvenanceRefs parses path's {"refs": [...]} shape. An empty refs
// list is not itself an error (a tree may legitimately cite nothing), but
// a malformed file or a ref missing path/term/reason is (exit 2).
func LoadProvenanceRefs(path string) ([]ProvenanceRef, error) {
	data, err := os.ReadFile(path) //nolint:gosec // repo-relative committed testdata path
	if err != nil {
		return nil, fmt.Errorf("sweep: loading provenance refs %s: %w", path, err)
	}
	var parsed struct {
		Refs []ProvenanceRef `json:"refs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("sweep: parsing provenance refs %s: %w", path, err)
	}
	for i, r := range parsed.Refs {
		if r.Path == "" || r.Term == "" {
			return nil, fmt.Errorf("sweep: %s entry %d missing path or term", path, i)
		}
		if strings.TrimSpace(r.Reason) == "" {
			return nil, fmt.Errorf("sweep: %s entry %d (%s, %s) missing a reason", path, i, r.Path, r.Term)
		}
	}
	return parsed.Refs, nil
}

// Resolver classifies a Term against the real replacement files under
// Root. It hardcodes no pointer list of its own and reads no planning
// input: every fact it needs is either on the Term itself or on disk at a
// path the Term names.
type Resolver struct {
	Root string
}

// fileContainsAnchor reports whether root/rel exists and, when anchor is
// non-empty, contains it as a literal substring. A missing or unreadable
// file is returned as an error, never a false result, so a "done" term
// with a vanished replacement fails closed rather than silently
// reclassifying.
func fileContainsAnchor(root, rel, anchor string) (bool, error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(full) //nolint:gosec // replacement path validated by LoadTermFile
	if err != nil {
		return false, fmt.Errorf("sweep: reading replacement %s: %w", rel, err)
	}
	if anchor == "" {
		return true, nil
	}
	return strings.Contains(string(data), anchor), nil
}

// Resolve classifies term by its own State. Never consults which path
// produced the hit — a term's disposition is a property of the term
// file's declaration, not of any one occurrence.
func (r *Resolver) Resolve(term Term) (Disposition, error) {
	switch term.State {
	case "":
		return DispositionUnresolved, nil
	case "done":
		return r.resolveDone(term)
	case "planned":
		return r.resolvePlanned(term)
	default:
		return "", fmt.Errorf("sweep: term %q has unknown state %q", term.Term, term.State)
	}
}

// resolveDone requires every declared replacement to exist and hold the
// anchor literal; either failure is an input error (exit 2), never a
// silent OPEN/UNRESOLVED downgrade.
func (r *Resolver) resolveDone(term Term) (Disposition, error) {
	for _, rel := range term.Replacement {
		ok, err := fileContainsAnchor(r.Root, rel, term.Anchor)
		if err != nil {
			return "", fmt.Errorf("sweep: term %q state done: %w", term.Term, err)
		}
		if !ok {
			return "", fmt.Errorf("sweep: term %q state done but replacement %s does not contain anchor %q",
				term.Term, rel, term.Anchor)
		}
	}
	return DispositionDone, nil
}

// resolvePlanned reports OPEN, unless every declared replacement is
// already present and (when an anchor is declared) already holds it — a
// stale planned term is an input error (exit 2): the term file must be
// advanced to "done" instead of left behind.
func (r *Resolver) resolvePlanned(term Term) (Disposition, error) {
	allPresent := true
	for _, rel := range term.Replacement {
		ok, err := fileContainsAnchor(r.Root, rel, term.Anchor)
		if err != nil || !ok {
			allPresent = false
			break
		}
	}
	if allPresent {
		return "", fmt.Errorf("sweep: term %q state planned but every replacement already exists (stale; advance to done)", term.Term)
	}
	return DispositionOpen, nil
}

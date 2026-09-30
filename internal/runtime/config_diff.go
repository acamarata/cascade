package runtime

// Purpose: ConfigWriter.ApplyDiff — the plugin-owned, all-or-nothing
// diff-apply seam behind `cascade nself handshake` (P1-E25-W5-S103-T1).
// Unlike Set (one key, always written when valid), ApplyDiff resolves a
// batch of dotted-path/literal entries against ONE current tree, skips
// anything the operator has overridden, refuses outright on any authority
// violation (config_diff_vet.go) or CompareSecurity loosening, and writes
// the whole batch (or nothing) in a single atomic write.
// Inputs: a ConfigDiff{Owner, Entries[]{Path, Literal}}; Literal is a TOML
// literal string, validated by canonicalLiteral (config_literal.go).
// Outputs: a DiffResult naming every entry Applied/Unchanged/Skipped, or a
// typed error with the disk untouched.
// Constraints: fail closed and all-or-nothing. Only canonical re-encodings
// are written, never a caller's text. Ownership is recorded in the same
// write as the contract's shape, one inline table keyed by full dotted
// path: [plugins.<owner>] managed = {"<path>" = "<canonical literal>"}.
// After composing, the candidate must differ from the current file only at
// the applied paths and the managed record, and CompareSecurity between
// the two (file against file, effectiveOfTree) must be empty.
// SPORT: internal/runtime config_diff.go (ADD) — P1-E25-W5-S103-T1.

import (
	"reflect"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DiffEntry is one proposed dotted-path/literal write in a ConfigDiff.
type DiffEntry struct {
	Path    string
	Literal string
}

// ConfigDiff is ApplyDiff's whole request: Owner names the plugin table
// ownership is recorded under ([plugins.<Owner>].managed).
type ConfigDiff struct {
	Owner   string
	Entries []DiffEntry
}

// DiffOutcome is one entry's classification in a DiffResult.
type DiffOutcome struct {
	Path   string
	Value  interface{}
	Reason string
}

// DiffResult is ApplyDiff's report: every entry, bucketed.
type DiffResult struct {
	Applied   []DiffOutcome
	Unchanged []DiffOutcome
	Skipped   []DiffOutcome
}

// effectiveOfTree assembles a *Config from tree exactly as Load does for
// the fields CompareSecurity reads (parseConfigSections for the typed
// Elevation section, extraSections for Extra), with no env overrides (file
// against file), and returns extractEffectiveConfig of it.
func effectiveOfTree(tree map[string]interface{}) (EffectiveConfig, error) {
	sec, err := parseConfigSections(tree, func(string, ...interface{}) {})
	if err != nil {
		return EffectiveConfig{}, err
	}
	cfg := &Config{Elevation: sec.elevation, Extra: extraSections(tree)}
	return extractEffectiveConfig(cfg), nil
}

// treeGet reads the value at dotted in tree; found is false when any
// intermediate segment is missing or is not itself a table.
func treeGet(tree map[string]interface{}, dotted string) (interface{}, bool) {
	segments := strings.Split(dotted, ".")
	m := tree
	for _, seg := range segments[:len(segments)-1] {
		next, ok := m[seg].(map[string]interface{})
		if !ok {
			return nil, false
		}
		m = next
	}
	v, ok := m[segments[len(segments)-1]]
	return v, ok
}

// ApplyDiff vets diff, classifies each entry against the current file,
// and writes every accepted change plus the updated managed record in one
// atomic write. Any refusal writes nothing.
func (w *ConfigWriter) ApplyDiff(diff ConfigDiff) (DiffResult, error) {
	entries, err := vetDiff(diff)
	if err != nil {
		return DiffResult{}, err
	}
	return w.applyVetted(diff.Owner, entries)
}

// applyVetted is ApplyDiff after vetting: compose, gate, write.
func (w *ConfigWriter) applyVetted(owner string, entries []vettedEntry) (DiffResult, error) {
	src, err := readOptionalFile(w.Path)
	if err != nil {
		return DiffResult{}, err
	}
	current, err := decodeForValidate(src)
	if err != nil {
		return DiffResult{}, err
	}
	if err := boundDiffTable(current, owner); err != nil {
		return DiffResult{}, err
	}
	edited, result, err := composeDiff(src, current, owner, entries)
	if err != nil {
		return DiffResult{}, err
	}
	if len(result.Applied) == 0 {
		return result, nil // nothing changed, nothing to write
	}
	if err := gateCandidate(current, edited, owner, result.Applied); err != nil {
		return DiffResult{}, err
	}
	if err := writeBytesAtomic(w.Path, edited); err != nil {
		return DiffResult{}, err
	}
	return result, nil
}

// gateCandidate decodes and validates the composed file, refuses any
// change outside the applied paths and the managed record, and refuses
// any CompareSecurity loosening between the current and composed files.
func gateCandidate(current map[string]interface{}, edited []byte, owner string, applied []DiffOutcome) error {
	candidate, err := decodeForValidate(edited)
	if err != nil {
		return err
	}
	if err := boundDiffTable(candidate, owner); err != nil {
		return err
	}
	if err := Validate(candidate); err != nil {
		return err
	}
	if err := onlyIntendedChanges(current, candidate, owner, applied); err != nil {
		return err
	}
	currentEff, err := effectiveOfTree(current)
	if err != nil {
		return err
	}
	candidateEff, err := effectiveOfTree(candidate)
	if err != nil {
		return err
	}
	if loosened := CompareSecurity(currentEff, candidateEff); len(loosened) > 0 {
		return cascade.Newf(cascade.KindPolicyDenied,
			"runtime: ApplyDiff: refusing a loosening write: %s", loosenedKeys(loosened))
	}
	return nil
}

// onlyIntendedChanges compares every leaf of the two trees and refuses a
// changed leaf that is neither an applied path (or under one) nor part of
// [plugins.<owner>].managed.
func onlyIntendedChanges(current, candidate map[string]interface{}, owner string, applied []DiffOutcome) error {
	before, after := map[string]interface{}{}, map[string]interface{}{}
	flattenTree(current, "", before)
	flattenTree(candidate, "", after)
	allowed := []string{"plugins." + owner + "." + managedKey}
	for _, a := range applied {
		allowed = append(allowed, a.Path)
	}
	for _, key := range unionKeys(before, after) {
		if reflect.DeepEqual(before[key], after[key]) || underAny(key, allowed) {
			continue
		}
		return cascade.Newf(cascade.KindPolicyDenied,
			"runtime: ApplyDiff: the composed file changes %s, which the diff did not name", key)
	}
	return nil
}

func unionKeys(a, b map[string]interface{}) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, dup := a[k]; !dup {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func underAny(key string, roots []string) bool {
	for _, r := range roots {
		if key == r || strings.HasPrefix(key, r+".") {
			return true
		}
	}
	return false
}

// loosenedKeys renders paths' keys, sorted, comma-joined.
func loosenedKeys(paths []LooseningPath) string {
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// boundDiffTable refuses oversized owner state before classification or writing.
func boundDiffTable(tree map[string]interface{}, owner string) error {
	table, _ := treeGet(tree, "plugins."+owner)
	if typed, ok := table.(map[string]interface{}); ok {
		if err := checkPluginTableSize(owner, typed); err != nil {
			return cascade.Wrap(cascade.KindPolicyDenied, err, "runtime: ApplyDiff: plugin table bound")
		}
	}
	return nil
}

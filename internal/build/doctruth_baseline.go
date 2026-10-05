package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// baselineRelPath is the baseline's fixed location, relative to repoRoot.
const baselineRelPath = "internal/build/testdata/doctruth-baseline.json"

// baselineEntry is one committed baseline row.
type baselineEntry struct {
	Key    string `json:"key"`
	File   string `json:"file"`
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

var docRuleSet = map[string]bool{
	string(DocRuleLink): true, string(DocRuleAnchor): true, string(DocRulePath): true,
	string(DocRuleLine): true, string(DocRuleSymbol): true, string(DocRuleTest): true,
	string(DocRuleClaim): true, string(DocRuleIndex): true, string(DocRuleDirective): true,
}

// loadBaseline reads and validates the committed baseline. A missing file,
// malformed JSON, or an entry with an unknown rule or a key already seen
// is an error.
func loadBaseline(repoRoot string) ([]baselineEntry, error) {
	data, err := readFileFn(pathJoin(repoRoot, baselineRelPath))
	if err != nil {
		return nil, wrapReadErr("doctruth: reading baseline", baselineRelPath, err)
	}
	var entries []baselineEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "doctruth: malformed baseline")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !docRuleSet[e.Rule] {
			return nil, cascade.Newf(cascade.KindIntegrity, "doctruth: baseline entry %q has unknown rule %q", e.Key, e.Rule)
		}
		if seen[e.Key] {
			return nil, cascade.Newf(cascade.KindIntegrity, "doctruth: baseline has duplicate key %q", e.Key)
		}
		seen[e.Key] = true
	}
	return entries, nil
}

// applyBaseline computes New and Fixed for one CheckDocTruth run. New =
// findings not baselined (CI) or every finding (Release), restricted to
// inFileScope. Fixed = baseline keys, restricted to inFileScope, whose key
// no longer appears among findings.
func applyBaseline(findings []DocFinding, baseline []baselineEntry, mode DocTruthMode, inFileScope func(string) bool) (newFindings []DocFinding, fixed []string) {
	baselineKeys := map[string]bool{}
	for _, e := range baseline {
		baselineKeys[e.Key] = true
	}
	findingKeys := map[string]bool{}
	for _, f := range findings {
		findingKeys[f.Key] = true
		if !inFileScope(f.File) {
			continue
		}
		if mode == DocTruthCI && baselineKeys[f.Key] {
			continue
		}
		newFindings = append(newFindings, f)
	}
	for _, e := range baseline {
		if !inFileScope(e.File) {
			continue
		}
		if !findingKeys[e.Key] {
			fixed = append(fixed, e.Key)
		}
	}
	sort.Strings(fixed)
	return newFindings, fixed
}

func entriesFromFindings(findings []DocFinding) []baselineEntry {
	out := make([]baselineEntry, 0, len(findings))
	for _, f := range findings {
		out = append(out, baselineEntry{Key: f.Key, File: f.File, Rule: string(f.Rule), Detail: f.Detail})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func marshalBaseline(entries []baselineEntry) []byte {
	if entries == nil {
		entries = []baselineEntry{}
	}
	data, _ := json.MarshalIndent(entries, "", "  ")
	return append(data, '\n')
}

// InitDocTruthBaseline computes every current finding over the whole scope
// and writes the baseline for the first time. It refuses (KindConflict)
// when the file already exists — this is the one path allowed to CREATE
// the baseline, and it must never be used to reset one.
func InitDocTruthBaseline(repoRoot string) (int, error) {
	if _, err := readFileFn(pathJoin(repoRoot, baselineRelPath)); err == nil {
		return 0, cascade.Newf(cascade.KindConflict, "doctruth: baseline already exists at %s", baselineRelPath)
	}
	_, findings, err := computeFindings(repoRoot)
	if err != nil {
		return 0, err
	}
	entries := entriesFromFindings(findings)
	if err := writeBaselineFile(repoRoot, entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}

// PruneDocTruthBaseline rewrites the baseline to the intersection of its
// committed keys and the current findings' keys. It can only shrink the
// file; it never adds a key that was not already present.
func PruneDocTruthBaseline(repoRoot string) (kept, dropped int, err error) {
	existing, err := loadBaseline(repoRoot)
	if err != nil {
		return 0, 0, err
	}
	_, findings, err := computeFindings(repoRoot)
	if err != nil {
		return 0, 0, err
	}
	findingKeys := map[string]bool{}
	for _, f := range findings {
		findingKeys[f.Key] = true
	}
	var out []baselineEntry
	for _, e := range existing {
		if findingKeys[e.Key] {
			out = append(out, e)
		}
	}
	if err := writeBaselineFile(repoRoot, out); err != nil {
		return 0, 0, err
	}
	return len(out), len(existing) - len(out), nil
}

func writeBaselineFile(repoRoot string, entries []baselineEntry) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	path := pathJoin(repoRoot, baselineRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "doctruth: creating baseline directory")
	}
	if err := runtime.WriteFileAtomic(path, marshalBaseline(entries), 0o644); err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "doctruth: writing baseline")
	}
	return nil
}

// GuardDocTruthBaseline compares the baseline committed at ref against the
// working copy and reports every key present now that was absent at ref
// (an "added" key — the one thing a ratchet must never allow).
func GuardDocTruthBaseline(repoRoot, ref string) (added []string, err error) {
	oldData, err := gitShowFn(repoRoot, ref, baselineRelPath)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "doctruth: git show %s:%s", ref, baselineRelPath)
	}
	var oldEntries []baselineEntry
	if err := json.Unmarshal(oldData, &oldEntries); err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, err, "doctruth: malformed baseline at %s", ref)
	}
	oldKeys := map[string]bool{}
	for _, e := range oldEntries {
		oldKeys[e.Key] = true
	}
	cur, err := loadBaseline(repoRoot)
	if err != nil {
		return nil, err
	}
	for _, e := range cur {
		if !oldKeys[e.Key] {
			added = append(added, e.Key)
		}
	}
	sort.Strings(added)
	return added, nil
}

package runtime

// Purpose: composeDiff — ApplyDiff's in-memory edit step
// (P1-E25-W5-S103-T1): classify every vetted entry against the current
// tree and the owner's managed record, apply the accepted ones with the
// structure-preserving line editor, and rewrite the managed record.
// Inputs: the current bytes and decoded tree, the owner, vetted entries.
// Outputs: the edited bytes and the per-entry classification.
// Constraints: per entry, absent => applied; equal => unchanged; differs
// and equals the owner's managed value => applied (its own earlier
// write); anything else => skipped "user-set" (a user edit always wins).
// The managed record is one inline table keyed by the full dotted path,
// so two paths can never share a record (the "__" encoding it replaces
// mapped a.b__c and a.b.c to one key).
// SPORT: internal/runtime config_diff.go family (ADD) — P1-E25-W5-S103-T1.

import (
	"reflect"
)

// Classification buckets for one entry.
const (
	diffApplied = iota
	diffUnchanged
	diffSkipped
)

// composeDiff classifies entries and returns the edited bytes.
func composeDiff(src []byte, tree map[string]interface{}, owner string, entries []vettedEntry) ([]byte, DiffResult, error) {
	managed := managedRecords(tree, owner)
	updates := map[string]string{}
	edited := src
	var result DiffResult
	for _, e := range entries {
		bucket, outcome := classifyEntry(tree, managed, e)
		switch bucket {
		case diffUnchanged:
			result.Unchanged = append(result.Unchanged, outcome)
			continue
		case diffSkipped:
			result.Skipped = append(result.Skipped, outcome)
			continue
		}
		var err error
		if edited, err = SetKeyLine(edited, e.Path, e.Canonical); err != nil {
			return nil, DiffResult{}, err
		}
		updates[e.Path] = e.Canonical
		result.Applied = append(result.Applied, outcome)
	}
	if len(updates) == 0 {
		return edited, result, nil
	}
	record, err := managedRecordLiteral(managed, updates)
	if err != nil {
		return nil, DiffResult{}, err
	}
	edited, err = SetKeyLine(edited, "plugins."+owner+"."+managedKey, record)
	if err != nil {
		return nil, DiffResult{}, err
	}
	return edited, result, nil
}

// classifyEntry buckets one entry against the current tree.
func classifyEntry(tree map[string]interface{}, managed map[string]string, e vettedEntry) (int, DiffOutcome) {
	current, found := treeGet(tree, e.Path)
	switch {
	case !found:
		return diffApplied, DiffOutcome{Path: e.Path, Value: e.Value, Reason: "absent"}
	case reflect.DeepEqual(current, e.Value):
		return diffUnchanged, DiffOutcome{Path: e.Path, Value: current, Reason: "unchanged"}
	case managedEquals(managed, e.Path, current):
		return diffApplied, DiffOutcome{Path: e.Path, Value: e.Value, Reason: "owner-managed"}
	}
	return diffSkipped, DiffOutcome{Path: e.Path, Value: current, Reason: "user-set"}
}

// managedRecords reads [plugins.<owner>].managed as path -> recorded
// literal. Only string-valued records count; anything else in the table
// is not a record ApplyDiff wrote and is dropped when the table is next
// rewritten.
func managedRecords(tree map[string]interface{}, owner string) map[string]string {
	out := map[string]string{}
	raw, ok := treeGet(tree, "plugins."+owner+"."+managedKey)
	if !ok {
		return out
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return out
	}
	for path, v := range table {
		if s, isString := v.(string); isString {
			out[path] = s
		}
	}
	return out
}

// managedEquals reports whether the owner's recorded literal for path
// decodes to current. A record that no longer passes canonicalLiteral
// never matches, so a damaged record can only make ApplyDiff skip.
func managedEquals(managed map[string]string, path string, current interface{}) bool {
	literal, ok := managed[path]
	if !ok {
		return false
	}
	value, _, err := canonicalLiteral(literal)
	return err == nil && reflect.DeepEqual(value, current)
}

// managedRecordLiteral merges updates into the existing records and
// renders the whole record as one canonical inline table.
func managedRecordLiteral(existing, updates map[string]string) (string, error) {
	table := make(map[string]interface{}, len(existing)+len(updates))
	for path, literal := range existing {
		table[path] = literal
	}
	for path, literal := range updates {
		table[path] = literal
	}
	return encodeInlineTable(table)
}

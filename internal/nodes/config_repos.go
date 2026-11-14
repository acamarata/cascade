package nodes

import (
	"slices"

	"github.com/acamarata/cascade/pkg/cascade"
)

// parseReposSection parses [nodes.repos] (node id -> repository registry ids)
// into sec.Repos, split out of parseSection for the 50-line cap. Absent
// leaves Repos nil. A malformed table, key or list is refused whole: the
// caller returns a zero Section, never a partial targeting table. Each list
// is sorted and de-duplicated; an empty array is kept as an empty list.
func parseReposSection(raw map[string]interface{}, sec *Section) error {
	v, ok := raw["repos"]
	if !ok {
		return nil
	}
	table, ok := v.(map[string]interface{})
	if !ok {
		return cascade.New(cascade.KindInvalidInput, "nodes: [nodes.repos] must be a table of node id to repository id lists")
	}
	repos := make(map[string][]string, len(table))
	for key, val := range table {
		list, err := parseRepoList(key, val)
		if err != nil {
			return err
		}
		repos[key] = list
	}
	sec.Repos = repos
	return nil
}

// parseRepoList validates one [nodes.repos] entry and returns it sorted and
// de-duplicated.
func parseRepoList(key string, val interface{}) ([]string, error) {
	if key == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "nodes: [nodes.repos] has an empty node id key")
	}
	items, ok := val.([]interface{})
	if !ok {
		return nil, cascade.Newf(cascade.KindInvalidInput, "nodes: [nodes.repos].%s must be an array of strings", key)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, cascade.Newf(cascade.KindInvalidInput, "nodes: [nodes.repos].%s entries must be non-empty strings", key)
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

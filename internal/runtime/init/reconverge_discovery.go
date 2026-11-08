package init

// Purpose: the additive discovery reconverge (P1-NODE-12d). A second
//   init run upserts the [roots], [nodes] and [policy] answers and the
//   provider names onto what the machine already holds.
// Inputs: a DiscoveryStore over the live configuration and the desired
//   DiscoveryState.
// Outputs: a DiscoveryDelta naming each write; empty on identical input.
// Constraints: additive only. A root, node or provider already present
//   is never removed or written again, and the policy preset is written
//   only when it differs. The store exposes no remove verb, so a replay
//   cannot drop anything even by mistake.
// SPORT: internal/runtime/init discovery reconverge (ADD) — P1-NODE-12d.

import (
	"context"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DiscoveryState is the discovery configuration, as held or as desired.
type DiscoveryState struct {
	// Roots are the watched root paths.
	Roots []string
	// Nodes are the paired hosts.
	Nodes []string
	// Providers are the installed provider names.
	Providers []string
	// Policy is the policy preset; empty leaves it unset or unchanged.
	Policy string
}

// DiscoveryStore reads and additively writes the discovery state.
type DiscoveryStore interface {
	// Discovery returns what the machine holds now.
	Discovery(ctx context.Context) (DiscoveryState, error)
	// AddRoot adds one watched root.
	AddRoot(ctx context.Context, path string) error
	// AddNode pairs or records one node.
	AddNode(ctx context.Context, host string) error
	// AddProvider records one provider name.
	AddProvider(ctx context.Context, name string) error
	// SetPolicy writes the policy preset.
	SetPolicy(ctx context.Context, preset string) error
}

// DiscoveryDelta names every write a reconverge made.
type DiscoveryDelta struct {
	Roots     []string `json:"roots,omitempty"`
	Nodes     []string `json:"nodes,omitempty"`
	Providers []string `json:"providers,omitempty"`
	Policy    string   `json:"policy,omitempty"`
}

// Empty reports whether the reconverge changed nothing.
func (d DiscoveryDelta) Empty() bool {
	return len(d.Roots) == 0 && len(d.Nodes) == 0 && len(d.Providers) == 0 && d.Policy == ""
}

// ReconvergeDiscovery upserts desired onto store and returns the delta.
//
// Running it twice with the same desired state makes no write the second
// time and returns an empty delta. A desired value for the policy preset
// is validated before the first write.
func ReconvergeDiscovery(ctx context.Context, store DiscoveryStore, desired DiscoveryState) (DiscoveryDelta, error) {
	var delta DiscoveryDelta
	if store == nil {
		return delta, cascade.New(cascade.KindInvalidInput, "cascade init: the discovery reconverge needs a store")
	}
	policyName := ""
	if desired.Policy != "" {
		name, err := resolvePolicyPreset(desired.Policy)
		if err != nil {
			return delta, err
		}
		policyName = name
	}
	have, err := store.Discovery(ctx)
	if err != nil {
		return delta, err
	}
	if delta.Roots, err = addMissing(ctx, have.Roots, desired.Roots, false, store.AddRoot); err != nil {
		return delta, err
	}
	if delta.Nodes, err = addMissing(ctx, have.Nodes, desired.Nodes, true, store.AddNode); err != nil {
		return delta, err
	}
	if delta.Providers, err = addMissing(ctx, have.Providers, desired.Providers, false, store.AddProvider); err != nil {
		return delta, err
	}
	if policyName != "" && policyName != have.Policy {
		if err := store.SetPolicy(ctx, policyName); err != nil {
			return delta, err
		}
		delta.Policy = policyName
	}
	return delta, nil
}

// addMissing calls add for each desired entry have lacks, in sorted
// order, and returns the entries it added. Host names compare
// case-insensitively (fold); paths and provider names compare exactly.
func addMissing(ctx context.Context, have, desired []string, fold bool, add func(context.Context, string) error) ([]string, error) {
	seen := append([]string(nil), have...)
	want := append([]string(nil), desired...)
	sort.Strings(want)
	var added []string
	for _, v := range want {
		if v = strings.TrimSpace(v); v == "" || hasEntry(seen, v, fold) {
			continue
		}
		if err := add(ctx, v); err != nil {
			return added, err
		}
		seen = append(seen, v)
		added = append(added, v)
	}
	return added, nil
}

// hasEntry reports whether list carries v.
func hasEntry(list []string, v string, fold bool) bool {
	for _, e := range list {
		if e == v || (fold && strings.EqualFold(e, v)) {
			return true
		}
	}
	return false
}

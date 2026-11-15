package init

// Purpose: the --reconverge run's discovery half. After the config,
//   provider, plugin and harness converge, the [roots], [nodes] and
//   [policy] answers of the setup file are upserted onto the live
//   discovery state through ReconvergeDiscovery.
// Inputs: Options.Spec, Options.ReconvergeOptions.Providers and
//   Deps.Discovery.
// Outputs: Report.Discovery, the additive delta; empty on a replay.
// Constraints: the discovery tables are checked before anything is
//   converged, so a run that names them without a store is refused with
//   nothing changed. A --check run reads the store and plans every write
//   it would make, writing nothing. Hosts and paths only: a pairing code
//   never reaches the delta.

import (
	"context"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrDiscoveryStoreMissing refuses a --reconverge whose setup file names
// [roots], [nodes] or [policy] on a build that wired no discovery store.
var ErrDiscoveryStoreMissing = cascade.New(cascade.KindInvalidInput,
	"cascade init: the setup file sets [roots], [nodes] or [policy], but this build has no discovery store to reconverge them")

// runReconverge is Run's --reconverge branch: the config converge, then
// the additive discovery reconverge.
func (w *Wizard) runReconverge(ctx context.Context) (Report, error) {
	desired, wanted := w.discoveryDesired()
	if wanted && w.deps.Discovery == nil {
		return Report{Diff: w.diff}, ErrDiscoveryStoreMissing
	}
	merged, rerr := w.Reconverge(ctx, w.opts.ReconvergeOptions)
	report := Report{Converged: &merged}
	if rerr != nil && !errors.Is(rerr, ErrReconvergeConflicts) {
		report.Diff = w.diff
		return report, rerr
	}
	if wanted {
		delta, err := w.reconvergeDiscovery(ctx, desired)
		report.Discovery = &delta
		if err != nil {
			report.Diff = w.diff
			return report, err
		}
	}
	report.Diff = w.diff
	return report, rerr
}

// discoveryDesired is the discovery state the setup file asks for, and
// whether it names any of [roots], [nodes] or [policy]. Provider names
// ride along so the store records them, but on their own they need no
// store: the provider converge already owns them.
func (w *Wizard) discoveryDesired() (DiscoveryState, bool) {
	spec := w.opts.Spec
	if spec == nil {
		return DiscoveryState{}, false
	}
	desired := DiscoveryState{
		Roots:     append([]string(nil), spec.Roots.Paths...),
		Nodes:     append([]string(nil), spec.Nodes.Pair...),
		Providers: append([]string(nil), w.opts.ReconvergeOptions.Providers...),
		Policy:    spec.Policy.Preset,
	}
	wanted := len(desired.Roots) > 0 || len(desired.Nodes) > 0 || desired.Policy != ""
	return desired, wanted
}

// reconvergeDiscovery upserts desired onto the discovery store and
// renders the delta. A non-writing run goes through planningStore, so
// the delta is what a real run would write and nothing is written.
func (w *Wizard) reconvergeDiscovery(ctx context.Context, desired DiscoveryState) (DiscoveryDelta, error) {
	w.say("== discovery")
	store := w.deps.Discovery
	if !w.writing() {
		store = planningStore{DiscoveryStore: store, w: w}
	}
	delta, err := ReconvergeDiscovery(ctx, store, desired)
	if err != nil {
		return delta, err
	}
	if delta.Empty() {
		w.say("   discovery: nothing to add")
		return delta, nil
	}
	if w.writing() {
		w.say("   added roots %v, nodes %v, providers %v, policy %q",
			delta.Roots, delta.Nodes, delta.Providers, delta.Policy)
	}
	return delta, nil
}

// planningStore reads through to the live store and turns every write
// into a planned line, for --check.
type planningStore struct {
	DiscoveryStore
	w *Wizard
}

// AddRoot plans adding a watched root.
func (p planningStore) AddRoot(_ context.Context, path string) error {
	p.w.plan("add root %s", path)
	return nil
}

// AddNode plans pairing or recording a node.
func (p planningStore) AddNode(_ context.Context, host string) error {
	p.w.plan("add node %s", host)
	return nil
}

// AddProvider plans recording a provider name.
func (p planningStore) AddProvider(_ context.Context, name string) error {
	p.w.plan("record provider %s", name)
	return nil
}

// SetPolicy plans writing the policy preset.
func (p planningStore) SetPolicy(_ context.Context, preset string) error {
	p.w.plan("set policy preset %s", preset)
	return nil
}

package init

// Purpose: the reconverge RUN (P1-E16-W4-S35-T7) — reading the machine's
//   current state, deciding with reconverge.go's rules, and applying it.
// Inputs: a CurrentState seam over the effective config, the provider
//   registry, the plugin catalog and the harness drift check.
// Outputs: a Convergence, and exit-3 semantics when a user edit held.
// Constraints: a reconverge that could not READ the current state refuses
//   rather than converging against an assumed one. Converging against a
//   machine nobody looked at is how a setup run reverts a config it never
//   saw.
// SPORT: internal/runtime/init reconverge (ADD) — P1-E16-W4-S35-T7.

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CurrentState reads what is already configured on this machine.
//
// One seam rather than four, because a reconverge needs all of it or none
// of it: a run that could read the providers but not the config would
// converge half the machine and report success for the whole of it.
type CurrentState interface {
	// Config returns the effective configuration, with provenance.
	Config(ctx context.Context) (ConfigView, error)
	// Providers returns the installed provider names.
	Providers(ctx context.Context) ([]string, error)
	// Plugins returns the enabled plugins and, separately, the ones the
	// operator has explicitly toggled — which `disable` may not touch.
	Plugins(ctx context.Context) (enabled, explicitlyToggled []string, err error)
	// HarnessFiles returns each generated instruction file's drift state
	// for cwd.
	HarnessFiles(ctx context.Context, cwd string) ([]HarnessFile, error)
}

// ErrReconvergeUnavailable refuses when this build has no way to read the
// machine's current state.
//
// A refusal, not a fresh run: a fresh run over a configured machine
// overwrites the configuration the operator asked to converge, which is
// the one outcome neither of them wanted.
var ErrReconvergeUnavailable = cascade.New(cascade.KindInternal,
	"cascade init --reconverge: this build cannot read the machine's current configuration, "+
		"so it will not converge against an assumed one")

// ErrReconvergeConflicts is the exit-3 outcome: the run converged what it
// could and left the operator's own edits in place.
//
// A distinct ANSWER rather than a failure. Nothing went wrong — the
// operator changed something and this run did not revert it, which is the
// behaviour they should want.
var ErrReconvergeConflicts = cascade.New(cascade.KindConflict,
	"cascade init --reconverge: converged, with your own edits left in place")

// ReconvergeOptions are the second run's inputs.
type ReconvergeOptions struct {
	// Desired is the configuration this run would set, by dotted key.
	Desired map[string]any
	// ForceSections names sections whose user edits may be overwritten
	// (--force-section).
	ForceSections []string
	// Providers names the providers the setup file asks for.
	Providers []string
	// EnablePlugins and DisablePlugins are the catalog's desired state.
	EnablePlugins  []string
	DisablePlugins []string
}

// Reconverge converges an already-configured machine.
//
// It reads everything FIRST and decides second, so a read that fails
// cannot leave the machine half-converged: nothing is applied until every
// question about the current state has an answer.
func (w *Wizard) Reconverge(ctx context.Context, opts ReconvergeOptions) (Convergence, error) {
	if w.deps.Current == nil {
		return Convergence{}, ErrReconvergeUnavailable
	}
	view, err := w.deps.Current.Config(ctx)
	if err != nil {
		return Convergence{}, err
	}
	installed, err := w.deps.Current.Providers(ctx)
	if err != nil {
		return Convergence{}, err
	}
	enabled, toggled, err := w.deps.Current.Plugins(ctx)
	if err != nil {
		return Convergence{}, err
	}
	files, err := w.deps.Current.HarnessFiles(ctx, w.deps.Cwd)
	if err != nil {
		return Convergence{}, err
	}

	merged := MergeConfig(view, opts.Desired, opts.ForceSections)
	providers := ConvergeProviders(installed, opts.Providers)
	plugins := ConvergePlugins(enabled, opts.EnablePlugins, opts.DisablePlugins, toggled)
	harnesses := ConvergeHarnesses(files)

	w.reportConvergence(merged, providers, plugins, harnesses)
	if !w.writing() {
		return merged, nil
	}
	if err := w.applyConvergence(ctx, merged, providers, plugins, harnesses); err != nil {
		return merged, err
	}
	if !merged.Clean() {
		return merged, ErrReconvergeConflicts
	}
	return merged, nil
}

// reportConvergence prints what the run decided, before it does any of it.
//
// Before, on purpose: an operator watching a terminal that then fails
// part-way knows exactly how far it got, and a --check run's output is
// identical to the real one's minus the doing.
func (w *Wizard) reportConvergence(
	merged Convergence, providers ProviderConvergence,
	plugins PluginConvergence, harnesses HarnessConvergence,
) {
	w.say("== reconverge")
	for _, key := range merged.Applied {
		w.plan("set %s", key)
	}
	for _, key := range merged.Unknown {
		w.say("   cannot set %s; this build's configuration has no such key", key)
	}
	for _, c := range merged.Conflicts {
		w.say("   kept your %s = %v (this run wanted %v; --force-section %s to override)",
			c.Key, c.OnDisk, c.Desired, c.Section)
	}
	for _, name := range providers.Add {
		w.plan("add provider %s", name)
	}
	for _, name := range providers.Reverify {
		w.plan("re-verify provider %s", name)
	}
	for _, name := range plugins.Enable {
		w.plan("enable plugin %s", name)
	}
	for _, name := range plugins.Disable {
		w.plan("disable plugin %s", name)
	}
	for _, name := range plugins.Kept {
		w.say("   left plugin %s enabled; you turned it on yourself", name)
	}
	for _, path := range harnesses.Regenerate {
		w.plan("regenerate %s", path)
	}
	for _, path := range harnesses.Reported {
		w.say("   left %s alone; its managed block has been edited", path)
	}
	// "Already converged" is a stronger claim than "no conflicts": a run
	// with work to apply is not converged yet, and saying both in one
	// report is the kind of contradiction an operator stops reading past.
	if len(merged.Applied) == 0 && merged.Clean() && len(merged.Unknown) == 0 &&
		len(providers.Add) == 0 && len(plugins.Enable) == 0 &&
		len(plugins.Disable) == 0 && len(harnesses.Regenerate) == 0 {
		w.say("   already converged")
	}
}

// applyConvergence performs what reportConvergence described.
//
// Config writes and provider work go through the subcommands that own
// them, for the same reason step 5 does: `cascade config set` owns the
// config file's format and `cascade provider add` owns the credential
// path. A second implementation of either here would drift from it.
func (w *Wizard) applyConvergence(
	ctx context.Context, merged Convergence,
	providers ProviderConvergence, plugins PluginConvergence, harnesses HarnessConvergence,
) error {
	for _, key := range merged.Applied {
		if err := w.deps.Sub.Run(ctx, "config", "set", key, renderValue(merged.desiredFor(key))); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "cascade init: `cascade config set %s`", key)
		}
	}
	for _, name := range providers.Add {
		if err := w.deps.Sub.Run(ctx, "provider", "add", name, "--oauth"); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "cascade init: `cascade provider add %s`", name)
		}
	}
	for _, name := range providers.Reverify {
		// RE-VERIFY, never re-add: the provider is already in the
		// registry with a credential this run has no business touching.
		// A failure is REPORTED rather than fatal — a provider that has
		// stopped answering is worth telling the operator about, and is
		// not a reason to fail a converge that otherwise succeeded.
		if err := w.deps.Sub.Run(ctx, "provider", "test", name); err != nil {
			w.say("   provider %s did not verify: %v", name, err)
		}
	}
	// The plugin convergence is APPLIED, not only reported. It was
	// computed, printed and dropped, so `--enable-plugin`/`--disable-plugin`
	// printed a plan and performed none of it (R-14.277).
	//
	// A per-name failure is REPORTED rather than fatal, matching the
	// provider re-verify above: a plugin that is built in refuses the
	// toggle with that fact, and an operator is better served by being
	// told than by a converge that otherwise succeeded failing over it.
	w.applyPluginToggles(ctx, plugins)
	if len(harnesses.Regenerate) > 0 {
		// ONE sync, not one per file: `context harness sync` regenerates
		// every stale file in the tree, so calling it per path would run
		// the same whole-tree operation once for each file it was
		// already going to fix.
		if err := w.deps.Sub.Run(ctx, "context", "harness", "sync"); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: `cascade context harness sync`")
		}
	}
	return nil
}

// applyPluginToggles runs the enable/disable the convergence decided.
//
// Through `cascade plugin enable|disable`, for the same reason config and
// provider work go through their own subcommands: that command owns what a
// plugin's enabled flag means, including the refusal for a builtin, and a
// second implementation here would be the surface that disagrees with it.
//
// Nothing here is fatal. A toggle that fails leaves the named plugin as it
// was and says so; the alternative is a converge that wrote the config,
// added the providers and regenerated the harness files, then reported
// total failure because one plugin would not turn off.
func (w *Wizard) applyPluginToggles(ctx context.Context, plugins PluginConvergence) {
	for _, name := range plugins.Enable {
		if err := w.deps.Sub.Run(ctx, "plugin", "enable", name); err != nil {
			w.say("   plugin %s did not enable: %v", name, err)
		}
	}
	for _, name := range plugins.Disable {
		if err := w.deps.Sub.Run(ctx, "plugin", "disable", name); err != nil {
			w.say("   plugin %s did not disable: %v", name, err)
		}
	}
}

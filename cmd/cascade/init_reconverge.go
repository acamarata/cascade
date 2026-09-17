package main

// Purpose: the production CurrentState (P1-E16-W4-S35-T7) — what
//   `cascade init --reconverge` reads before it decides anything.
// Inputs: the effective config, the provider registry, the builtin plugin
//   registry, and internal/context's own drift check.
// Outputs: the four answers a convergence needs.
// Constraints: Art.1 — each reader is the real source. A reader that
//   cannot answer returns ITS error and the reconverge refuses; nothing
//   here substitutes an empty answer, because converging against an
//   assumed state is how a setup run reverts a config it never saw.
// SPORT: cmd/cascade init reconverge (ADD) — P1-E16-W4-S35-T7.

import (
	"context"
	"os"
	"strings"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	cascadeinit "github.com/acamarata/cascade/internal/runtime/init"
	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// initCurrentState reads the machine as it is now.
type initCurrentState struct {
	paths runtime.PathProvider
}

var _ cascadeinit.CurrentState = initCurrentState{}

// Config loads the effective configuration, provenance included.
//
// Provenance is the point: *runtime.Config already records, per key,
// whether a value came from the shipped default or from the user's own
// config.toml, which IS the three-way comparison a converge needs.
// Re-deriving it from an ancestor file would be a second answer to a
// question already answered.
func (c initCurrentState) Config(ctx context.Context) (cascadeinit.ConfigView, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: c.paths.ConfigPath()})
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// Providers lists the providers already in the registry.
func (c initCurrentState) Providers(ctx context.Context) ([]string, error) {
	store, err := openProviderStorage(ctx, providerDepsFor(c.paths))
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	records, err := store.Registry.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(records))
	for _, rec := range records {
		names = append(names, rec.Name)
	}
	return names, nil
}

// Plugins reports which plugins are on, and which the operator toggled
// themselves.
//
// The second list is what keeps a setup file's `disable` from reverting a
// deliberate choice. It comes from the config's own provenance: a plugin
// whose enabled flag resolves from SourceFile was written by whoever owns
// that file; one still at its default was not.
func (c initCurrentState) Plugins(ctx context.Context) (enabled, toggled []string, err error) {
	reg := &plugins.BuiltinRegistry{}
	if loadErr := reg.Load(); loadErr != nil {
		return nil, nil, loadErr
	}
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: c.paths.ConfigPath()})
	if err != nil {
		return nil, nil, err
	}
	for _, r := range reg.List() {
		id := r.Manifest.ID
		enabled = append(enabled, id)
		if cfg.Source("plugins."+id+".enabled") == runtime.SourceFile {
			toggled = append(toggled, id)
		}
	}
	return enabled, toggled, nil
}

// HarnessFiles reports each generated instruction file's drift state for
// cwd, through internal/context's own check.
//
// Stale and hand-edited come from ONE source, because they are two facts
// about the same comparison: the drift check recomputes the managed
// block's digest, and both fall out of it. Deriving them separately would
// let them disagree about one file.
func (c initCurrentState) HarnessFiles(ctx context.Context, cwd string) ([]cascadeinit.HarnessFile, error) {
	result, err := cascadecontext.Sync(ctx, cwd, os.UserHomeDir, true)
	if err != nil {
		return nil, err
	}
	out := make([]cascadeinit.HarnessFile, 0, len(result.Drift))
	for _, d := range result.Drift {
		out = append(out, cascadeinit.HarnessFile{
			Path: d.Path, Stale: d.Stale, Modified: driftIsHandEdit(d.Reason),
		})
	}
	return out, nil
}

// driftIsHandEdit reads the drift check's own reason vocabulary.
//
// Reads it rather than re-deciding: the reason strings belong to
// internal/context's DriftCheck. A file "missing on disk" is stale and
// NOT hand-edited — there is nothing to have edited — while one whose
// managed block was changed is both.
func driftIsHandEdit(reason string) bool {
	return strings.Contains(strings.ToLower(reason), "hand-edited")
}

// ErrReconvergeNeedsAConfig refuses a reconverge with nothing to converge
// TOWARD.
//
// A reconverge's desired state comes from a setup file. Without one there
// is no "desired" to compare against, and running anyway would compare
// the machine to the shipped defaults — which would try to revert every
// deliberate choice the operator has ever made.
var ErrReconvergeNeedsAConfig = cascade.New(cascade.KindInvalidInput,
	"cascade init --reconverge: needs --config (or "+initconfig.EnvConfig+") naming the setup file "+
		"to converge toward; without one there is nothing to converge to but the shipped defaults")

// reconvergeOptionsFrom turns a resolved spec into the desired state.
func reconvergeOptionsFrom(spec *initconfig.Spec, force []string) (cascadeinit.ReconvergeOptions, error) {
	if spec == nil || (!spec.ProvidersSet && !spec.PluginsSet && spec.Profile == "") {
		return cascadeinit.ReconvergeOptions{}, ErrReconvergeNeedsAConfig
	}
	opts := cascadeinit.ReconvergeOptions{
		ForceSections: force,
		EnablePlugins: spec.Plugins,
		Desired:       map[string]any{},
	}
	if spec.Profile != "" {
		opts.Desired["runtime.profile"] = spec.Profile
	}
	// Telemetry is deliberately NOT in the desired map. Step 7 records
	// the operator's answer on the journal, and the journal is deleted
	// when a run completes — this build persists no telemetry key to
	// config.toml, so there is nothing for a converge to compare
	// against. Emitting one would hand `cascade config set` a key its
	// schema refuses. Tracked as the gap it is rather than papered over
	// with a key that does not exist (R-14.257).
	for _, p := range spec.Providers {
		opts.Providers = append(opts.Providers, p.Name)
	}
	return opts, nil
}

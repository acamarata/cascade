// Purpose: bind the nSelf handshake to the guarded runtime config writer.
// Inputs: plugin-owned config entries; Outputs: atomic diff outcomes and
// the value screen the handshake runs before proposing an entry.
// Constraints: owner pinned to cascade-nself, paths checked before reading config.
// SPORT: P1-PLG-01 config-diff-apply.

package plugins

import (
	"context"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	nselfplugin "github.com/acamarata/cascade/plugins/nself"
)

func init() {
	if err := nselfplugin.SetConfigApplier(nselfConfigApplier{}); err != nil {
		panic("internal/plugins: wire cascade-nself config applier: " + err.Error())
	}
	if err := nselfplugin.SetLiteralScreener(nselfConfigApplier{}); err != nil {
		panic("internal/plugins: wire cascade-nself literal screener: " + err.Error())
	}
}

// ScreenLiteral implements nselfplugin.LiteralScreener with the value
// checks runtime.ConfigWriter Set and ApplyDiff run (one validator), so
// the handshake withholds, rather than proposes, anything ApplyDiff would
// refuse: a known-prefix secret split across whitespace included.
func (nselfConfigApplier) ScreenLiteral(path, literal string) error {
	return runtime.ScreenConfigLiteral(path, literal)
}

// nselfConfigOwner is the only owner the bound applier ever passes to
// ApplyDiff. It equals the cascade-nself manifest id
// (TestNselfApplierPinsOwner reads the record it produces).
const nselfConfigOwner = "cascade-nself"

// nselfConfigApplier adapts internal/runtime's ConfigWriter.ApplyDiff
// (P1-E25-W5-S103-T1) to the plugin's local ConfigApplier seam. Authority
// never comes from the caller (R-14.322): the owner argument is ignored and
// Owner is pinned to nselfConfigOwner, and every entry must pass
// admitNselfConfigPath or the WHOLE diff is refused before config.toml is
// read. Runtime paths are resolved per call, never cached at init time: a
// path fixed at boot would go stale under a later CASCADE_HOME/
// CASCADE_CONFIG change.
type nselfConfigApplier struct{}

// ApplyDiff implements nselfplugin.ConfigApplier over the real
// runtime.ConfigWriter.
func (nselfConfigApplier) ApplyDiff(_ context.Context, _ string, entries []nselfplugin.ConfigEntry) (nselfplugin.ConfigResult, error) {
	diff := runtime.ConfigDiff{Owner: nselfConfigOwner, Entries: make([]runtime.DiffEntry, 0, len(entries))}
	for _, e := range entries {
		if err := admitNselfConfigPath(e.Path); err != nil {
			return nselfplugin.ConfigResult{}, err
		}
		diff.Entries = append(diff.Entries, runtime.DiffEntry{Path: e.Path, Literal: e.Literal})
	}
	paths, err := runtime.NewDefaultPathProvider()
	if err != nil {
		return nselfplugin.ConfigResult{}, cascade.Wrap(cascade.KindInternal, err, "plugin: cascade-nself resolve config path")
	}
	result, err := (&runtime.ConfigWriter{Path: paths.ConfigPath()}).ApplyDiff(diff)
	if err != nil {
		return nselfplugin.ConfigResult{}, err
	}
	return nselfplugin.ConfigResult{
		Applied:   toConfigOutcomes(result.Applied),
		Unchanged: toConfigOutcomes(result.Unchanged),
		Skipped:   toConfigOutcomes(result.Skipped),
	}, nil
}

// admitNselfConfigPath is the fail-closed path allowlist: runtime.profile,
// or plugins.cascade-nself.<key...> where <key> is not "managed" (the
// ownership record only ApplyDiff writes). Anything else, a malformed
// path included, is KindPolicyDenied naming the path.
func admitNselfConfigPath(path string) error {
	segments, err := runtime.SplitDottedPath(path)
	if err == nil {
		if path == "runtime.profile" {
			return nil
		}
		if len(segments) >= 3 && segments[0] == "plugins" && segments[1] == nselfConfigOwner && segments[2] != "managed" {
			return nil
		}
	}
	return cascade.Newf(cascade.KindPolicyDenied,
		"plugin: cascade-nself may write only plugins.%s.* and runtime.profile; refusing the whole diff at %q",
		nselfConfigOwner, path)
}

// toConfigOutcomes converts runtime.DiffOutcome to the plugin's local
// ConfigOutcome shape (path + reason only — the plugin never needs the
// decoded Go value).
func toConfigOutcomes(outcomes []runtime.DiffOutcome) []nselfplugin.ConfigOutcome {
	out := make([]nselfplugin.ConfigOutcome, 0, len(outcomes))
	for _, o := range outcomes {
		out = append(out, nselfplugin.ConfigOutcome{Path: o.Path, Reason: o.Reason})
	}
	return out
}

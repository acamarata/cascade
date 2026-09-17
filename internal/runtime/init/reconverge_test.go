package init

// Purpose: the reconverge rules (P1-E16-W4-S35-T7, R-14.52) — what a
//   second run may change, and what it must report instead.
// Constraints: every case that asserts "applied" has a sibling asserting
//   "conflicted", because a merge that always applied and a merge that
//   always conflicted each pass half of this on their own.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// fakeConfigView is a ConfigView over fixed values and sources. It stands
// in for *runtime.Config so a test can state "the user edited this key"
// directly rather than by writing a config.toml and loading it — the
// loader's own tests cover that this is what SourceFile means.
type fakeConfigView struct {
	values  map[string]any
	sources map[string]runtime.ConfigSource
}

func (f fakeConfigView) Source(key string) runtime.ConfigSource {
	if s, ok := f.sources[key]; ok {
		return s
	}
	return runtime.SourceDefault
}

func (f fakeConfigView) EffectiveEntries() []runtime.EffectiveEntry {
	out := make([]runtime.EffectiveEntry, 0, len(f.values))
	for k, v := range f.values {
		out = append(out, runtime.EffectiveEntry{Key: k, Value: v, Source: f.Source(k)})
	}
	return out
}

// fakeCurrent is a CurrentState over fixed answers.
type fakeCurrent struct {
	view      ConfigView
	viewErr   error
	providers []string
	enabled   []string
	toggled   []string
	files     []HarnessFile
}

func (f fakeCurrent) Config(context.Context) (ConfigView, error)  { return f.view, f.viewErr }
func (f fakeCurrent) Providers(context.Context) ([]string, error) { return f.providers, nil }
func (f fakeCurrent) Plugins(context.Context) ([]string, []string, error) {
	return f.enabled, f.toggled, nil
}
func (f fakeCurrent) HarnessFiles(context.Context, string) ([]HarnessFile, error) {
	return f.files, nil
}

// TestAUserEditAlwaysWins is the rule the whole merge exists for: a setup
// run that silently reverted somebody's config change is worse than one
// that refuses.
func TestAUserEditAlwaysWins(t *testing.T) {
	view := fakeConfigView{
		values:  map[string]any{"retrieval.fusion": "off", "logging.level": "info"},
		sources: map[string]runtime.ConfigSource{"retrieval.fusion": runtime.SourceFile},
	}

	got := MergeConfig(view, map[string]any{
		"retrieval.fusion": "on",    // the user edited this
		"logging.level":    "debug", // still the shipped default
	}, nil)

	if len(got.Applied) != 1 || got.Applied[0] != "logging.level" {
		t.Errorf("applied %v, want only the untouched key", got.Applied)
	}
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want the edited key", got.Conflicts)
	}
	c := got.Conflicts[0]
	if c.Key != "retrieval.fusion" || c.OnDisk != "off" || c.Desired != "on" {
		t.Errorf("conflict = %+v, want both values named", c)
	}
	if c.Section != "retrieval" {
		t.Errorf("section = %q, want the key's first segment", c.Section)
	}
	if got.Clean() {
		t.Error("Clean() = true with a conflict recorded")
	}
}

// TestForceSectionOverridesOneSectionOnly: the escape hatch is scoped, so
// forcing one section cannot quietly revert an edit in another.
func TestForceSectionOverridesOneSectionOnly(t *testing.T) {
	view := fakeConfigView{
		values: map[string]any{"retrieval.fusion": "off", "logging.level": "info"},
		sources: map[string]runtime.ConfigSource{
			"retrieval.fusion": runtime.SourceFile,
			"logging.level":    runtime.SourceFile,
		},
	}

	got := MergeConfig(view, map[string]any{
		"retrieval.fusion": "on",
		"logging.level":    "debug",
	}, []string{"Retrieval"})

	if len(got.Applied) != 1 || got.Applied[0] != "retrieval.fusion" {
		t.Errorf("applied %v, want only the forced section's key", got.Applied)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0].Key != "logging.level" {
		t.Errorf("conflicts = %+v, want the unforced section's edit held", got.Conflicts)
	}
	if len(got.Forced) != 1 || got.Forced[0] != "retrieval" {
		t.Errorf("forced = %v, want the section normalized", got.Forced)
	}
}

// TestAnAlreadyConvergedKeyIsNeitherAppliedNorAConflict. Without this,
// every second run reports every key as a conflict and exits 3 forever.
func TestAnAlreadyConvergedKeyIsNeitherAppliedNorAConflict(t *testing.T) {
	view := fakeConfigView{
		values:  map[string]any{"logging.level": "debug"},
		sources: map[string]runtime.ConfigSource{"logging.level": runtime.SourceFile},
	}

	got := MergeConfig(view, map[string]any{"logging.level": "debug"}, nil)
	if len(got.Applied) != 0 || len(got.Conflicts) != 0 {
		t.Errorf("a key already at its desired value produced %+v", got)
	}
	if !got.Clean() {
		t.Error("Clean() = false on a fully converged config")
	}
}

// TestValuesAreComparedByShapeNotByType: config values arrive from TOML
// as any, so an int64 from the file and an int from a desired map are the
// same value written twice. Reporting them as different would raise a
// conflict on a key nobody changed.
func TestValuesAreComparedByShapeNotByType(t *testing.T) {
	view := fakeConfigView{
		values:  map[string]any{"limits.max": int64(8), "plugins.enabled": []any{"a", "b"}},
		sources: map[string]runtime.ConfigSource{"limits.max": runtime.SourceFile},
	}

	got := MergeConfig(view, map[string]any{
		"limits.max":      8,
		"plugins.enabled": []string{"a", "b"},
	}, nil)
	if len(got.Conflicts) != 0 || len(got.Applied) != 0 {
		t.Errorf("equal values across types produced %+v", got)
	}
}

// TestMergeToleratesANilView: a reconverge over a config that could not
// be loaded applies nothing rather than panicking.
func TestMergeToleratesANilView(t *testing.T) {
	got := MergeConfig(nil, map[string]any{"a.b": 1}, nil)
	if len(got.Applied) != 0 || len(got.Conflicts) != 0 {
		t.Errorf("a nil view produced %+v", got)
	}
}

// TestProvidersAreConvergedAndNeverRemoved: a provider the operator added
// by hand is not garbage to be collected because this setup file does not
// mention it.
func TestProvidersAreConvergedAndNeverRemoved(t *testing.T) {
	got := ConvergeProviders(
		[]string{"anthropic", "added-by-hand"},
		[]string{"anthropic", "openai"},
	)
	if len(got.Add) != 1 || got.Add[0] != "openai" {
		t.Errorf("add = %v, want only the provider not yet installed", got.Add)
	}
	if len(got.Reverify) != 1 || got.Reverify[0] != "anthropic" {
		t.Errorf("reverify = %v, want the installed provider re-verified, not re-added", got.Reverify)
	}
	// There is deliberately no Remove field. This asserts the shape, so
	// that adding one later has to come past this test.
	if strings.Contains(renderValue(got), "added-by-hand") {
		t.Errorf("the convergence mentions a provider it must leave alone: %+v", got)
	}
}

// TestDisableAppliesOnlyToNeverToggledPlugins: a plugin somebody turned
// on by hand is not turned off by a setup file that happens to list it.
func TestDisableAppliesOnlyToNeverToggledPlugins(t *testing.T) {
	got := ConvergePlugins(
		[]string{"pbd", "cascade-pa", "cascade-claude"}, // currently on
		[]string{"cascade-codex"},                       // desired enable
		[]string{"cascade-pa", "pbd"},                   // desired disable
		[]string{"cascade-pa"},                          // the operator turned this one on themselves
	)
	if len(got.Enable) != 1 || got.Enable[0] != "cascade-codex" {
		t.Errorf("enable = %v", got.Enable)
	}
	if len(got.Disable) != 1 || got.Disable[0] != "pbd" {
		t.Errorf("disable = %v, want only the never-toggled entry", got.Disable)
	}
	if len(got.Kept) != 1 || got.Kept[0] != "cascade-pa" {
		t.Errorf("kept = %v, want the operator's own choice reported rather than reverted", got.Kept)
	}
}

// TestEnablingAnAlreadyEnabledPluginIsANoOp keeps a second run from
// reporting work it did not do.
func TestEnablingAnAlreadyEnabledPluginIsANoOp(t *testing.T) {
	got := ConvergePlugins([]string{"pbd"}, []string{"pbd"}, nil, nil)
	if len(got.Enable) != 0 || len(got.Disable) != 0 || len(got.Kept) != 0 {
		t.Errorf("a converged catalog produced %+v", got)
	}
}

// TestOnlyStaleAndUnmodifiedFilesAreRegenerated is Z/S-53.T3's
// no-silent-overwrite rule: a file the operator edited is reported, never
// rewritten.
func TestOnlyStaleAndUnmodifiedFilesAreRegenerated(t *testing.T) {
	got := ConvergeHarnesses([]HarnessFile{
		{Path: "/p/.claude/CLAUDE.md", Stale: true, Modified: false},
		{Path: "/p/AGENTS.md", Stale: true, Modified: true},
		{Path: "/p/fresh.md", Stale: false, Modified: false},
		{Path: "/p/edited-but-current.md", Stale: false, Modified: true},
	})
	if len(got.Regenerate) != 1 || got.Regenerate[0] != "/p/.claude/CLAUDE.md" {
		t.Errorf("regenerate = %v, want only the stale unmodified file", got.Regenerate)
	}
	if len(got.Reported) != 1 || got.Reported[0] != "/p/AGENTS.md" {
		t.Errorf("reported = %v, want the stale hand-edited file named and left alone", got.Reported)
	}
}

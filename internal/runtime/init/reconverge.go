package init

// Purpose: the idempotent second run's RULES (P1-E16-W4-S35-T7,
//   R-14.52) — what a reconverge may change on an already-configured
//   machine, and what it must report instead.
// Inputs: the effective config's own provenance, the installed providers
//   and plugins, and the harness drift report.
// Outputs: a Convergence describing what was applied and what conflicted.
// Constraints: a reconverge CONVERGES, it never regenerates. A user's
//   edit always wins over a desired value, and the conflict is reported
//   rather than resolved silently — a setup run that quietly reverted
//   somebody's config change is worse than one that refused.
// SPORT: internal/runtime/init reconverge (ADD) — P1-E16-W4-S35-T7.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/runtime"
)

// ConfigView is the effective config a reconverge reads. Its two methods
// are transcribed from *runtime.Config, pinned below.
//
// Source is why no ancestor FILE is needed: the config loader already
// records, per key, whether a value came from the shipped default or
// from the user's own config.toml. "Still the default" against "the user
// wrote this" IS the three-way comparison, already computed by the thing
// that loaded the file.
type ConfigView interface {
	Source(key string) runtime.ConfigSource
	EffectiveEntries() []runtime.EffectiveEntry
}

var _ ConfigView = (*runtime.Config)(nil)

// Conflict is one key a reconverge would have changed and did not.
type Conflict struct {
	// Key is the dotted config key.
	Key string `json:"key"`
	// Section is Key's first segment, which --force-section names.
	Section string `json:"section"`
	// OnDisk is the value the user's config.toml holds.
	OnDisk any `json:"on_disk"`
	// Desired is the value this run would have written.
	Desired any `json:"desired"`
}

// Convergence is a reconverge's result.
type Convergence struct {
	// Applied names the keys this run set, in key order.
	Applied []string `json:"applied,omitempty"`
	// Conflicts are the keys a user edit held, in key order.
	Conflicts []Conflict `json:"conflicts,omitempty"`
	// Forced names the sections --force-section overrode.
	Forced []string `json:"forced,omitempty"`
	// Unknown names desired keys the effective configuration does not
	// have. They are reported, never attempted: `cascade config set`
	// refuses a key the schema does not know, so trying would fail the
	// whole converge over a key this build cannot set anyway.
	Unknown []string `json:"unknown,omitempty"`
	// desired carries the value each applied key should be set to, so
	// the applier need not be handed the desired map a second time and
	// risk being handed a different one.
	desired map[string]any
}

// desiredFor returns the value an applied key should be set to.
func (c Convergence) desiredFor(key string) any { return c.desired[key] }

// Clean reports whether every desired value was applied.
func (c Convergence) Clean() bool { return len(c.Conflicts) == 0 }

// MergeConfig decides, per key, whether a reconverge may write it.
//
// The rule in one line: a value the user has not touched may be set; a
// value they edited is theirs. `Source(key) == SourceFile` is the signal
// that they edited it — the loader already knows, because that is what
// produced the effective value.
//
// A key whose on-disk value already EQUALS the desired one is neither
// applied nor a conflict. Converged is converged; reporting it as a
// conflict would make every second run look like a problem.
func MergeConfig(view ConfigView, desired map[string]any, forceSections []string) Convergence {
	out := Convergence{Forced: normalizedSections(forceSections), desired: desired}
	if view == nil {
		return out
	}
	forced := map[string]bool{}
	for _, s := range out.Forced {
		forced[s] = true
	}
	onDisk := map[string]any{}
	for _, e := range view.EffectiveEntries() {
		onDisk[e.Key] = e.Value
	}

	for _, key := range sortedKeysOf(desired) {
		want := desired[key]
		if _, known := onDisk[key]; !known {
			// A key the effective config does not enumerate is not one
			// this build can set. Attempting it would hand `cascade
			// config set` a key its schema refuses, failing the whole
			// converge over something that was never settable.
			out.Unknown = append(out.Unknown, key)
			continue
		}
		if equalValues(onDisk[key], want) {
			continue
		}
		section := sectionOf(key)
		if view.Source(key) == runtime.SourceFile && !forced[section] {
			out.Conflicts = append(out.Conflicts, Conflict{
				Key: key, Section: section, OnDisk: onDisk[key], Desired: want,
			})
			continue
		}
		out.Applied = append(out.Applied, key)
	}
	return out
}

// sectionOf returns a dotted key's first segment.
func sectionOf(key string) string {
	if i := strings.Index(key, "."); i >= 0 {
		return key[:i]
	}
	return key
}

// normalizedSections trims, lowercases, dedupes and sorts
// --force-section's values.
func normalizedSections(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// sortedKeysOf returns m's keys in order, so a convergence report reads
// the same on every run.
func sortedKeysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// equalValues compares two config values.
//
// By their rendered form rather than with reflect.DeepEqual: config
// values arrive from TOML as any, and an int64 1 from the file and an int
// 1 from a desired map are the same value written twice. Treating them as
// different would report a conflict on a key nobody changed.
func equalValues(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return renderValue(a) == renderValue(b)
}

// renderValue renders a config value for comparison and for a conflict
// report.
func renderValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		return strings.Join(t, ",")
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, renderValue(e))
		}
		return strings.Join(parts, ",")
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

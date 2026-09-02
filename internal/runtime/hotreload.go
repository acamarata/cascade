package runtime

// Purpose: HotReloader — the daemon-side (and daemonless-CLI-side) engine
//
//	behind `cascade config reload` / SIGHUP / the fsnotify watcher
//	(hotreload_watch.go): re-reads config.toml, validates it, enforces
//	cold-key restart-required semantics, enforces the W1 unconditional
//	tightening-only loosening gate (hotreload_security.go's
//	CompareSecurity) plus the separate absolute [elevation] guard, and —
//	only when every check passes — atomically swaps in the new snapshot,
//	applies the one section this tree has a real hot-reload consumer for
//	(logging, via LogProvider.SetLevel/Reconfigure), and emits/persists
//	the outcome.
//
// Inputs: the resolved config.toml path, an injected Clock, an
//
//	EventPublisher, an AuditRecorder, and (optional) a *LogProvider to
//	drive logging's real hot-apply seam.
//
// Outputs: a ReloadOutcome describing what happened; the emitted event
//
//	name/payload; a persisted audit record; and, on acceptance, the new
//	*Config visible to every future Current() call.
//
// Constraints: Current() and Reload never mutate a shared *Config in
//
//	place — every transition is an atomic.Pointer swap to a brand-new
//	value, so a concurrent reader never observes a partially-updated
//	config (proven under -race in hotreload_test.go). Art.1: sections
//	this tree has no real consumer for (everything except logging) are
//	updated in the in-memory snapshot only — HotReloader never claims to
//	have applied a live behavioral change for them; ReloadOutcome.
//	AppliedLive lists exactly the sections genuinely pushed to a
//	consumer.
//
// SPORT: runtime/hot-reload-engine (ADD, placeholder per T-8 sport_updates).

import (
	"context"
	"reflect"
	"sync/atomic"
)

// HotReloader is the daemon-side config hot-reload engine. Zero value is
// not usable; construct with NewHotReloader.
type HotReloader struct {
	path      string
	loadOpts  LoadOptions
	current   atomic.Pointer[Config]
	clock     Clock
	events    EventPublisher
	audit     AuditRecorder
	logs      *LogProvider
	watchStop func()
}

// NewHotReloader builds a HotReloader over an already-loaded initial
// config. events/audit may be DiscardEventPublisher{}/nil respectively (a
// nil AuditRecorder is treated as best-effort-absent, matching
// StoreAuditRecorder's own nil-store no-op — see hotreload_events.go).
// logs may be nil when no logging consumer is wired (tests, or a
// daemonless CLI invocation).
func NewHotReloader(path string, loadOpts LoadOptions, initial *Config, clock Clock, events EventPublisher, audit AuditRecorder, logs *LogProvider) *HotReloader {
	if events == nil {
		events = DiscardEventPublisher{}
	}
	hr := &HotReloader{path: path, loadOpts: loadOpts, clock: clock, events: events, audit: audit, logs: logs}
	hr.current.Store(initial)
	return hr
}

// Current returns the currently active, immutable config snapshot. Safe
// for concurrent use with Reload — it is always either the pre-reload or
// the post-reload value, never a torn intermediate.
func (hr *HotReloader) Current() *Config {
	return hr.current.Load()
}

// ReloadOutcome reports what one Reload call did.
type ReloadOutcome struct {
	Accepted       bool
	Rejected       bool
	RestartKeys    []string
	LooseningPaths []LooseningPath
	AppliedLive    []string
	Err            error
}

// eventReloadAccepted / eventReloadRejected / eventRestartRequired name
// the C/S-04.T3 event-bus events this engine emits (08 §3: "invalid file
// -> keep running config, emit config.reload.rejected"; "cold-key change
// -> daemon emits config.restart.required"; every applied reload ->
// audit journal).
const (
	eventReloadAccepted   = "config.reload.accepted"
	eventReloadRejected   = "config.reload.rejected"
	eventRestartRequired  = "config.restart.required"
	auditKindReloadAccept = "reload_accepted"
	auditKindReloadReject = "reload_rejected"
)

// Reload re-reads and re-validates config.toml from disk and applies the
// full W1 hot-reload decision chain, in the order the ticket contract
// specifies: whole-file Validate, cold-key detection, then the
// tightening-only classifier (with [elevation] hard-gated independently
// of CompareSecurity's own verdict for it).
func (hr *HotReloader) Reload(ctx context.Context) ReloadOutcome {
	old := hr.Current()
	proposed, err := Load(ctx, hr.loadOpts)
	if err != nil {
		hr.reject(ctx, err.Error(), nil)
		return ReloadOutcome{Rejected: true, Err: err}
	}

	if elevationChanged(old, proposed) {
		hr.reject(ctx, "elevation section is never hot-reloadable in either direction", nil)
		return ReloadOutcome{Rejected: true}
	}

	restartKeys := coldKeyDiff(old, proposed)
	candidate := freezeColdSections(old, proposed)

	paths := CompareSecurity(extractEffectiveConfig(old), extractEffectiveConfig(candidate))
	if len(paths) > 0 {
		hr.reject(ctx, "proposed config loosens a guarded family (W1 unconditional deny)", paths)
		return ReloadOutcome{Rejected: true, LooseningPaths: paths}
	}

	applied := hr.applyLive(old, candidate)
	hr.current.Store(candidate)

	fields := map[string]interface{}{"restart_required_keys": restartKeys, "applied_live": applied}
	hr.events.Publish(ctx, eventReloadAccepted, fields)
	_ = hr.recordAudit(ctx, auditKindReloadAccept, fields)
	if len(restartKeys) > 0 {
		hr.events.Publish(ctx, eventRestartRequired, map[string]interface{}{"keys": restartKeys})
	}

	return ReloadOutcome{Accepted: true, RestartKeys: restartKeys, AppliedLive: applied}
}

// reject publishes config.reload.rejected and persists the rejection
// record, without touching the current snapshot.
func (hr *HotReloader) reject(ctx context.Context, reason string, paths []LooseningPath) {
	fields := map[string]interface{}{"reason": reason}
	if len(paths) > 0 {
		fields["loosening_paths"] = paths
	}
	hr.events.Publish(ctx, eventReloadRejected, fields)
	_ = hr.recordAudit(ctx, auditKindReloadReject, fields)
}

func (hr *HotReloader) recordAudit(ctx context.Context, kind string, fields map[string]interface{}) error {
	if hr.audit == nil {
		return nil
	}
	return hr.audit.Record(ctx, kind, fields)
}

// applyLive pushes the one section this tree has a genuine runtime
// consumer for — [logging], via LogProvider.SetLevel/Reconfigure — and
// reports which sections were actually pushed live. Every other section
// takes effect only in the sense that Current() now returns the new
// value; HotReloader never reports a section here it did not really call
// a consumer for (Art.1).
func (hr *HotReloader) applyLive(old, candidate *Config) []string {
	if hr.logs == nil || loggingEqual(old.Logging, candidate.Logging) {
		return nil
	}
	if level, err := parseLogLevel(candidate.Logging.Level); err == nil {
		hr.logs.SetLevel(level)
	}
	if candidate.Logging.Rotation.Enabled() {
		hr.logs.Reconfigure(*candidate.Logging.Rotation.MaxSizeMB, *candidate.Logging.Rotation.MaxFiles)
	} else {
		hr.logs.Reconfigure(0, 0)
	}
	return []string{"logging"}
}

// loggingEqual compares two loggingSection values by content rather than
// Go's default struct equality, which would compare loggingRotation's
// *int fields by pointer identity — always "different" across two
// separate Load() calls even when both point at equal ints, which would
// make applyLive treat logging as changed on every single reload.
func loggingEqual(a, b loggingSection) bool {
	if a.Level != b.Level || a.Format != b.Format {
		return false
	}
	return intPtrEqual(a.Rotation.MaxSizeMB, b.Rotation.MaxSizeMB) &&
		intPtrEqual(a.Rotation.MaxFiles, b.Rotation.MaxFiles)
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// elevationChanged reports whether proposed's [elevation] differs from
// old's in any field. This is the hard, CompareSecurity-independent gate
// (08 §[elevation]: "changing allow_remote or helper_pubkey requires an
// existing valid attestation"; a plain hot-reload is never that
// attestation, regardless of direction).
func elevationChanged(old, proposed *Config) bool {
	if old == nil || proposed == nil {
		return false
	}
	return old.Elevation != proposed.Elevation
}

// coldSections are the sections this ticket's cold-key detection diffs,
// per the ticket contract's own parenthetical ("diff the cold sections
// (runtime/daemon/storage)"). [elevation] is excluded here because it has
// its own stronger absolute-reject rule (elevationChanged), and
// [secrets].keychain_backend is excluded here because it is covered by
// CompareSecurity's guarded-family check instead (any change treated as
// loosening — see hotreload_security.go).
var coldSections = []string{"daemon", "storage"}

// coldKeyDiff returns every dotted key that changed within the cold
// sections (runtime.profile, plus every leaf under [daemon] and
// [storage]).
func coldKeyDiff(old, proposed *Config) []string {
	var keys []string
	if old.Runtime.Profile != proposed.Runtime.Profile {
		keys = append(keys, "runtime.profile")
	}
	for _, section := range coldSections {
		oldFlat := map[string]interface{}{}
		newFlat := map[string]interface{}{}
		flattenTree(sectionAt(old.Extra, section), section, oldFlat)
		flattenTree(sectionAt(proposed.Extra, section), section, newFlat)
		keys = append(keys, diffFlatKeys(oldFlat, newFlat)...)
	}
	return keys
}

// diffFlatKeys returns every dotted key present in either flat map whose
// value differs (added, removed, or changed).
func diffFlatKeys(a, b map[string]interface{}) []string {
	var out []string
	seen := map[string]bool{}
	for k, v := range a {
		seen[k] = true
		if !reflect.DeepEqual(v, b[k]) {
			out = append(out, k)
		}
	}
	for k, v := range b {
		if seen[k] {
			continue
		}
		if !reflect.DeepEqual(v, a[k]) {
			out = append(out, k)
		}
	}
	return out
}

// freezeColdSections returns a copy of proposed with its cold sections
// (runtime, daemon, storage) forced back to old's values, so a config.toml
// edit that touches both a hot and a cold key still gets its hot part
// applied ("apply hot keys only", 08 §3) while the cold part waits for a
// real process restart.
func freezeColdSections(old, proposed *Config) *Config {
	frozen := *proposed
	frozen.Runtime = old.Runtime
	newExtra := map[string]interface{}{}
	for k, v := range proposed.Extra {
		newExtra[k] = v
	}
	for _, section := range coldSections {
		if v, ok := old.Extra[section]; ok {
			newExtra[section] = v
		} else {
			delete(newExtra, section)
		}
	}
	frozen.Extra = newExtra
	return &frozen
}

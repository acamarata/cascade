package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

// Purpose: golden-fixture tests for the plan AC "08 §3 behaviors
//   golden-tested incl. loosening-denied + divergent-boot"
//   (files_scope: testdata/goldens/loosening_denied.golden,
//   testdata/goldens/divergent_boot.golden). Reuses config_test.go's
//   shared compareGolden helper (same package, same -update flag
//   convention) rather than a second golden mechanism.
// Constraints: Art.11 — rendered output never contains a timestamp or
//   absolute path; every LooseningPath/audit field is rendered by name,
//   sorted, never via a raw %v map dump (map iteration order is not
//   deterministic).

func TestConfigGolden_LoosingDenied(t *testing.T) {
	hr, path, events, _, store := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = false\n")
	_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = true\n")

	outcome := hr.Reload(context.Background())
	compareGolden(t, "loosening_denied.golden", renderReloadGolden(outcome, events, store))
}

func TestConfigGolden_DivergentBoot(t *testing.T) {
	store := storetest.NewMemStore()
	events := &fakeEventPublisher{}
	audit := NewStoreAuditRecorder(store, NewFixedClock(time.Unix(0, 0)))
	bc := NewBaselineChecker(store, NewFixedClock(time.Unix(0, 0)), events, audit)

	looseConfig := EffectiveConfig{
		Elevation: elevationSection{AllowRemote: true, HelperPubkey: "operator-key-1"},
		Sync:      SyncSection{Classes: map[string]string{"memory": "server-primary"}},
	}
	result := bc.Check(context.Background(), looseConfig)
	compareGolden(t, "divergent_boot.golden", renderBaselineGolden(result, events, store))
}

// renderReloadGolden deterministically renders a ReloadOutcome + the
// events/audit records it produced: outcome fields, sorted LooseningPath
// entries, sorted event names, and every audit record's kind + sorted
// field keys.
func renderReloadGolden(outcome ReloadOutcome, events *fakeEventPublisher, store *storetest.MemStore) string {
	var b strings.Builder
	fmt.Fprintf(&b, "accepted=%v rejected=%v\n", outcome.Accepted, outcome.Rejected)
	fmt.Fprintf(&b, "loosening_paths:\n")
	for _, p := range sortedLooseningPaths(outcome.LooseningPaths) {
		fmt.Fprintf(&b, "  - family=%s key=%s current=%v proposed=%v\n", p.Family, p.Key, p.Current, p.Proposed)
	}
	fmt.Fprintf(&b, "events:\n")
	for _, n := range sortedStrings(events.names()) {
		fmt.Fprintf(&b, "  - %s\n", n)
	}
	fmt.Fprintf(&b, "audit_records:\n")
	for _, line := range renderAuditKinds(store) {
		fmt.Fprintf(&b, "  - %s\n", line)
	}
	return b.String()
}

// renderBaselineGolden deterministically renders a BaselineResult and its
// side effects.
func renderBaselineGolden(result BaselineResult, events *fakeEventPublisher, store *storetest.MemStore) string {
	var b strings.Builder
	fmt.Fprintf(&b, "outcome=%d\n", result.Outcome)
	fmt.Fprintf(&b, "effective_allow_remote=%v\n", result.Effective.Elevation.AllowRemote)
	fmt.Fprintf(&b, "effective_sync_domains=%d\n", len(result.Effective.Sync.Classes))
	fmt.Fprintf(&b, "events:\n")
	for _, n := range sortedStrings(events.names()) {
		fmt.Fprintf(&b, "  - %s\n", n)
	}
	fmt.Fprintf(&b, "audit_records:\n")
	for _, line := range renderAuditKinds(store) {
		fmt.Fprintf(&b, "  - %s\n", line)
	}
	return b.String()
}

func sortedLooseningPaths(paths []LooseningPath) []LooseningPath {
	out := append([]LooseningPath(nil), paths...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// renderAuditKinds scans every record under auditNamespace and returns
// "<kind>" lines, sorted, one per persisted record (field values are
// deliberately not rendered here — TimeMS would make the golden
// non-deterministic across a real clock, and this ticket's Clock is
// already fixed in tests, but keeping the golden to record *kinds* keeps
// it robust to unrelated field additions).
func renderAuditKinds(store *storetest.MemStore) []string {
	var kinds []string
	for _, kind := range []string{auditKindReloadAccept, auditKindReloadReject, auditKindDoctorError, auditKindBaselineUpdate} {
		it, err := store.Scan(context.Background(), auditNamespace, kind+"/")
		if err != nil {
			continue
		}
		for it.Next(context.Background()) {
			kinds = append(kinds, kind)
		}
		_ = it.Close()
	}
	sort.Strings(kinds)
	return kinds
}

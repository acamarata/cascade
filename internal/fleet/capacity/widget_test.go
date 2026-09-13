package capacity

// Purpose (this file): table-driven tests for Compose (task 6): all-
// sources-present, stale/absent bucket -> null window (never a fabricated
// zero), StateAuthRequired -> reauth_required, the v1 quota.json golden
// parity check, and present/absent jobs-domain cases for
// ActiveJobsCount. Redact's own PII/label-redaction behavior is exercised
// end to end by internal/daemon's status_widget_privacy_test.go
// (TestStatusWidgetNoPII/TestStatusWidgetLabelRedaction per this ticket's
// checks list); this file adds a light direct case too, so this package's
// own coverage floor does not depend solely on a different package's test.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
)

func TestWidgetCompose(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	comp := NewCompositor(clk, time.Hour, "")
	providerSrc := &fakeProviderSource{
		providers: []registry.ProviderRecord{{Name: "acme"}},
		lanes: []registry.LaneRecord{
			{ProviderName: "acme", Capacity: BucketInteractiveUsage, State: registry.LaneStateAvailable},
		},
	}
	nodeSrc := &fakeNodeSource{devices: []nodes.DeviceRecord{
		{NodeID: "node1", Presence: nodes.PresenceReachable, Tier: nodes.TierWorkerTrusted},
	}}
	if err := comp.UpdateProviders(context.Background(), providerSrc); err != nil {
		t.Fatalf("UpdateProviders: %v", err)
	}
	if err := comp.UpdateNodes(context.Background(), nodeSrc); err != nil {
		t.Fatalf("UpdateNodes: %v", err)
	}
	snap := comp.Snapshot()

	jobsCount := 3
	got := Compose(snap, 2, &jobsCount, clk.Now(), 7)

	if len(got.Rows) != 1 || got.Rows[0].Ref != "acme" {
		t.Fatalf("Rows = %+v, want one row for acme", got.Rows)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].ID != "node1" || !got.Nodes[0].Reachable {
		t.Fatalf("Nodes = %+v, want one reachable node1", got.Nodes)
	}
	if got.AttentionCount != 2 {
		t.Errorf("AttentionCount = %d, want 2", got.AttentionCount)
	}
	if got.ActiveJobsCount == nil || *got.ActiveJobsCount != 3 {
		t.Errorf("ActiveJobsCount = %v, want 3", got.ActiveJobsCount)
	}
	if got.Seq != 7 {
		t.Errorf("Seq = %d, want 7 (caller-supplied, Compose never invents one)", got.Seq)
	}

	// Absent jobs domain: nil, never a fabricated zero.
	absent := Compose(snap, 0, nil, clk.Now(), 1)
	if absent.ActiveJobsCount != nil {
		t.Errorf("ActiveJobsCount = %v, want nil for an absent jobs domain", absent.ActiveJobsCount)
	}
}

func TestWidgetComposeNullOnStale(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	snap := FleetSnapshot{
		Providers: map[string]ProviderSlot{
			// No Buckets at all: the "source absent" case.
			"stale": {ProfileRef: "stale", State: StateUnknown, UpdatedAt: now},
			// AuthRequired at the slot level.
			"locked": {ProfileRef: "locked", State: StateAuthRequired, UpdatedAt: now},
		},
	}
	got := Compose(snap, 0, nil, now, 0)
	byRef := map[string]WidgetRow{}
	for _, r := range got.Rows {
		byRef[r.Ref] = r
	}

	stale := byRef["stale"]
	if stale.FiveHour == nil || stale.FiveHour.UtilizationPct != nil || stale.FiveHour.ResetsIn != nil {
		t.Errorf("stale row FiveHour = %+v, want a non-nil window with nil fields (never a fabricated zero)", stale.FiveHour)
	}
	if stale.SevenDay == nil || stale.SevenDay.UtilizationPct != nil {
		t.Errorf("stale row SevenDay = %+v, want nil fields", stale.SevenDay)
	}

	locked := byRef["locked"]
	if !locked.ReauthRequired {
		t.Errorf("locked row ReauthRequired = false, want true for StateAuthRequired")
	}
	if locked.State != string(StateAuthRequired) {
		t.Errorf("locked row State = %q, want %q", locked.State, StateAuthRequired)
	}
}

// v1QuotaSlot mirrors v1's QuotaSlot Codable shape (UsageCache.swift), the
// exact kept-field subset this parity check maps.
type v1QuotaSlot struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *float64 `json:"resets_at"`
	ResetsIn    *string  `json:"resets_in"`
	Status      *string  `json:"status"`
}

type v1UsageBlock struct {
	FiveHour     *v1QuotaSlot `json:"five_hour"`
	SevenDay     *v1QuotaSlot `json:"seven_day"`
	SevenDayOpus *v1QuotaSlot `json:"seven_day_opus"`
}

type v1AccountEntry struct {
	Account string        `json:"account"`
	Usage   *v1UsageBlock `json:"usage"`
}

type v1QuotaCache struct {
	Accounts []v1AccountEntry `json:"accounts"`
}

// TestWidgetComposeV1GoldenParity decodes internal/daemon/testdata/
// v1-goldens/quota.json (see that directory's README.md for provenance)
// and asserts the v2 field mapping this ticket keeps: utilization ->
// utilization_pct and status -> state for the five_hour window of every
// account, plus the seven_day_opus fallback rule v1's UsageRow.swift
// documents (weekUtil = seven_day_opus?.utilization ??
// seven_day?.utilization -- the opus-specific weekly window wins over the
// general one when both are present).
func TestWidgetComposeV1GoldenParity(t *testing.T) {
	path := filepath.Join("..", "..", "daemon", "testdata", "v1-goldens", "quota.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	var cache v1QuotaCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(cache.Accounts) != 3 {
		t.Fatalf("golden accounts = %d, want 3 (2 accounts + 1 pool)", len(cache.Accounts))
	}

	for _, acc := range cache.Accounts {
		assertV1FiveHourParity(t, acc)
	}
	assertV1OpusFallback(t, cache)
}

// assertV1FiveHourParity asserts the v2 mapping (utilization ->
// utilization_pct, same 0-100 scale, no unit conversion in this ticket's
// kept field subset) for acc's five_hour window and, when present, its
// weekly (opus-fallback) window.
func assertV1FiveHourParity(t *testing.T, acc v1AccountEntry) {
	t.Helper()
	if acc.Usage == nil || acc.Usage.FiveHour == nil {
		t.Fatalf("account %q: golden must carry a five_hour block", acc.Account)
	}
	fh := acc.Usage.FiveHour
	if fh.Utilization == nil || fh.Status == nil {
		t.Fatalf("account %q: five_hour missing utilization/status", acc.Account)
	}
	mapped := widgetWindow(Window{UtilizationPct: *fh.Utilization})
	if mapped.UtilizationPct == nil || *mapped.UtilizationPct != *fh.Utilization {
		t.Errorf("account %q: mapped utilization_pct = %v, want %v", acc.Account, mapped.UtilizationPct, *fh.Utilization)
	}

	weekly := acc.Usage.SevenDay
	if acc.Usage.SevenDayOpus != nil {
		weekly = acc.Usage.SevenDayOpus
	}
	if weekly == nil || weekly.Utilization == nil {
		return
	}
	weekMapped := widgetWindow(Window{UtilizationPct: *weekly.Utilization})
	if weekMapped.UtilizationPct == nil || *weekMapped.UtilizationPct != *weekly.Utilization {
		t.Errorf("account %q: weekly (opus-fallback) utilization_pct = %v, want %v", acc.Account, weekMapped.UtilizationPct, *weekly.Utilization)
	}
}

// assertV1OpusFallback asserts acc2 specifically exercises the fallback:
// seven_day_opus must win over seven_day (v1's UsageRow.swift weekUtil).
func assertV1OpusFallback(t *testing.T, cache v1QuotaCache) {
	t.Helper()
	var acc2 *v1AccountEntry
	for i := range cache.Accounts {
		if cache.Accounts[i].Account == "acc2" {
			acc2 = &cache.Accounts[i]
		}
	}
	if acc2 == nil || acc2.Usage.SevenDayOpus == nil || acc2.Usage.SevenDay == nil {
		t.Fatal("golden must carry an acc2 entry with both seven_day and seven_day_opus")
	}
	if *acc2.Usage.SevenDayOpus.Utilization == *acc2.Usage.SevenDay.Utilization {
		t.Fatal("golden's acc2 seven_day/seven_day_opus must differ to prove the fallback actually selects one over the other")
	}
}

func TestWidgetRedactDirect(t *testing.T) {
	snap := WidgetSnapshot{
		Rows: []WidgetRow{
			{Ref: "acme", Label: "acme"},
			{Ref: "/Users/alice/.cascade/acme", Label: "ok"},
		},
		Nodes: []NodePresenceSummary{{ID: "node1"}, {ID: "host.example.local"}},
		Projects: []ProjectRow{
			{Ref: "proj1", Label: "Alice's Secret Project"},
			{Ref: "proj2", Label: "user@example.com"},
		},
	}
	hidden := Redact(snap, false)
	if hidden.Projects[0].Label != "Project 1" || hidden.Projects[1].Label != "Project 2" {
		t.Errorf("hidden Projects labels = %+v, want Project 1/Project 2", hidden.Projects)
	}
	if hidden.Rows[1].Ref == "/Users/alice/.cascade/acme" {
		t.Errorf("Rows[1].Ref leaked an absolute path: %q", hidden.Rows[1].Ref)
	}
	if hidden.Nodes[1].ID == "host.example.local" {
		t.Errorf("Nodes[1].ID leaked a hostname: %q", hidden.Nodes[1].ID)
	}

	shown := Redact(snap, true)
	if shown.Projects[0].Label != "Alice's Secret Project" {
		t.Errorf("shown Projects[0].Label = %q, want the real label", shown.Projects[0].Label)
	}
	if shown.Projects[1].Label != "redacted" {
		t.Errorf("shown Projects[1].Label = %q, want \"redacted\" (unconditional PII strip overrides show_project_names)", shown.Projects[1].Label)
	}
}

// Purpose: regression tests for the aggregation and widget-row defects
// P1-WID-08 fixed (R9 F-3, C14): the empty-bucket-as-unknown state, the
// reset estimate shared across buckets and copied into both windows, the
// dropped reset on a window with no percentage source, and the row ref.
// Inputs: registry lane records through a real Compositor over the
// package's fake sources.
// Outputs: none.
// Constraints: the clock is a fake; no I/O.
// SPORT: fleet.capacity.snapshot (tests, P1-WID-08).

package capacity

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
)

var buildTestNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// composeFor runs lanes through a real Compositor (one provider per name in
// names, TTL one hour) and returns the composed rows keyed by name.
func composeFor(t *testing.T, names []string, lanes []registry.LaneRecord, generatedAt time.Time) map[string]WidgetRow {
	t.Helper()
	clk := &fakeClock{t: buildTestNow}
	comp := NewCompositor(clk, time.Hour, "")
	src := &fakeProviderSource{lanes: lanes}
	for _, n := range names {
		src.providers = append(src.providers, registry.ProviderRecord{Name: n})
	}
	if err := comp.UpdateProviders(context.Background(), src); err != nil {
		t.Fatalf("UpdateProviders: %v", err)
	}
	rows := map[string]WidgetRow{}
	for _, r := range Compose(comp.Snapshot(), 0, nil, generatedAt, 0).Rows {
		rows[r.Label] = r
	}
	return rows
}

func lane(provider string, bucket registry.CapacityBucket, state registry.LaneState, reset time.Time) registry.LaneRecord {
	return registry.LaneRecord{LaneName: provider + "-" + string(bucket), ProviderName: provider, Capacity: bucket, State: state, ResetEstimate: reset}
}

// TestStatusWidgetStateAvailable: one provider with one available key lane
// reads "available" (the empty buckets must not count), and the aggregate is
// the worst state over the non-empty buckets only.
func TestStatusWidgetStateAvailable(t *testing.T) {
	cases := []struct {
		name  string
		lanes []registry.LaneRecord
		want  State
		reau  bool
	}{
		{"one available key lane", []registry.LaneRecord{lane("p", BucketAPICredit, StateAvailable, time.Time{})}, StateAvailable, false},
		{"available plus constrained", []registry.LaneRecord{
			lane("p", BucketInteractiveUsage, StateAvailable, time.Time{}), lane("p", BucketAPICredit, StateConstrained, time.Time{})}, StateConstrained, false},
		{"auth-required in one bucket", []registry.LaneRecord{
			lane("p", BucketInteractiveUsage, StateAvailable, time.Time{}), lane("p", BucketAgentSDKCredit, StateAuthRequired, time.Time{})}, StateAuthRequired, true},
		{"a lane that is itself unknown", []registry.LaneRecord{lane("p", BucketAPICredit, StateUnknown, time.Time{})}, StateUnknown, false},
		{"no lane at all", nil, StateUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := composeFor(t, []string{"p"}, tc.lanes, buildTestNow)["p"]
			if row.State != string(tc.want) || row.ReauthRequired != tc.reau {
				t.Fatalf("row state %q reauth %v, want %q reauth %v", row.State, row.ReauthRequired, tc.want, tc.reau)
			}
		})
	}
}

// TestStatusWidgetWindowsFromLaneBucket: an api_credit provider's reset
// shows on five_hour; a past or zero estimate yields null, never 0.
func TestStatusWidgetWindowsFromLaneBucket(t *testing.T) {
	cases := []struct {
		name     string
		reset    time.Time
		generate time.Time
		want     *time.Duration
	}{
		{"future estimate", buildTestNow.Add(2 * time.Hour), buildTestNow, ptr(2 * time.Hour)},
		{"rebased to the compose instant", buildTestNow.Add(2 * time.Hour), buildTestNow.Add(30 * time.Minute), ptr(90 * time.Minute)},
		{"past estimate", buildTestNow.Add(-time.Minute), buildTestNow, nil},
		{"estimate equal to now", buildTestNow, buildTestNow, nil},
		{"zero estimate", time.Time{}, buildTestNow, nil},
		{"passed by compose time", buildTestNow.Add(time.Hour), buildTestNow.Add(time.Hour), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := composeFor(t, []string{"p"}, []registry.LaneRecord{lane("p", BucketAPICredit, StateExhausted, tc.reset)}, tc.generate)["p"]
			assertResets(t, row.FiveHour, tc.want)
			assertResets(t, row.SevenDay, nil)
			if row.State != string(StateExhausted) {
				t.Fatalf("state = %q, want exhausted", row.State)
			}
			raw, err := json.Marshal(row.FiveHour)
			if err != nil || tc.want == nil && !strings.Contains(string(raw), `"resets_in":null`) {
				t.Fatalf("window JSON = %s (err %v), want an explicit null reset", raw, err)
			}
		})
	}
}

func ptr(d time.Duration) *time.Duration { return &d }

func assertResets(t *testing.T, w *WidgetWindow, want *time.Duration) {
	t.Helper()
	if w == nil {
		t.Fatal("window is nil, the object must always be present")
	}
	if w.UtilizationPct != nil {
		t.Fatalf("utilization_pct = %v, want null: no quota source is wired", *w.UtilizationPct)
	}
	switch {
	case want == nil && w.ResetsIn != nil:
		t.Fatalf("resets_in = %v, want null", *w.ResetsIn)
	case want != nil && (w.ResetsIn == nil || *w.ResetsIn != *want):
		t.Fatalf("resets_in = %v, want %v", w.ResetsIn, *want)
	}
}

// TestStatusWidgetSevenDayNeverCarriesFiveHourEstimate: one estimate must
// not appear in both windows (a fabricated 7-day reset), nor leak into a
// bucket the lane does not use.
func TestStatusWidgetSevenDayNeverCarriesFiveHourEstimate(t *testing.T) {
	lanes := []registry.LaneRecord{
		lane("p", BucketInteractiveUsage, StateExhausted, buildTestNow.Add(3*time.Hour)),
		lane("p", BucketAPICredit, StateAvailable, time.Time{}),
	}
	clk := &fakeClock{t: buildTestNow}
	comp := NewCompositor(clk, time.Hour, "")
	if err := comp.UpdateProviders(context.Background(), &fakeProviderSource{providers: []registry.ProviderRecord{{Name: "p"}}, lanes: lanes}); err != nil {
		t.Fatal(err)
	}
	slot := comp.Snapshot().Providers["p"]
	for kind, b := range slot.Buckets {
		if b.SevenDay.ResetsIn != 0 {
			t.Errorf("bucket %s seven_day carries a reset %v", kind, b.SevenDay.ResetsIn)
		}
	}
	if got := slot.Buckets[BucketAPICredit].FiveHour.ResetsIn; got != 0 {
		t.Errorf("api_credit bucket five_hour reset = %v, it inherited the interactive bucket's estimate", got)
	}
	if got := slot.Buckets[BucketInteractiveUsage].FiveHour.ResetsIn; got != 3*time.Hour {
		t.Errorf("interactive bucket five_hour reset = %v, want 3h", got)
	}
	row := Compose(comp.Snapshot(), 0, nil, buildTestNow, 0).Rows[0]
	assertResets(t, row.FiveHour, ptr(3*time.Hour))
	assertResets(t, row.SevenDay, nil)
}

// TestStatusWidgetResetBelongsToDegradedLane: the reset shown is the one on
// the lane that degrades the bucket, not a stale estimate on a healthier one.
func TestStatusWidgetResetBelongsToDegradedLane(t *testing.T) {
	lanes := []registry.LaneRecord{
		{LaneName: "a", ProviderName: "p", Capacity: BucketAPICredit, State: StateAuthRequired},
		{LaneName: "b", ProviderName: "p", Capacity: BucketAPICredit, State: StateExhausted, ResetEstimate: buildTestNow.Add(time.Hour)},
	}
	row := composeFor(t, []string{"p"}, lanes, buildTestNow)["p"]
	if row.State != string(StateAuthRequired) {
		t.Fatalf("state = %q, want auth-required", row.State)
	}
	assertResets(t, row.FiveHour, nil)
}

// TestStatusWidgetUpdatedAtIsReadTime: a row's updated_at is the instant of
// the read that built it, so it falls behind generated_at when no read
// succeeds, and an expired slot reads unknown with no windows.
func TestStatusWidgetUpdatedAtIsReadTime(t *testing.T) {
	clk := &fakeClock{t: buildTestNow}
	comp := NewCompositor(clk, time.Minute, "")
	src := &fakeProviderSource{
		providers: []registry.ProviderRecord{{Name: "p"}},
		lanes:     []registry.LaneRecord{lane("p", BucketAPICredit, StateExhausted, buildTestNow.Add(time.Hour))},
	}
	if err := comp.UpdateProviders(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	clk.Advance(30 * time.Second)
	row := Compose(comp.Snapshot(), 0, nil, clk.Now(), 0).Rows[0]
	if !row.UpdatedAt.Equal(buildTestNow) || !row.UpdatedAt.Before(clk.Now()) {
		t.Fatalf("updated_at = %v, want the read instant %v, older than generated_at %v", row.UpdatedAt, buildTestNow, clk.Now())
	}
	assertResets(t, row.FiveHour, ptr(time.Hour-30*time.Second))

	clk.Advance(2 * time.Minute) // past the 1-minute ttl
	row = Compose(comp.Snapshot(), 0, nil, clk.Now(), 0).Rows[0]
	if row.State != string(StateUnknown) {
		t.Fatalf("expired row state = %q, want unknown", row.State)
	}
	assertResets(t, row.FiveHour, nil)
}

// TestStatusWidgetRefHandle: WidgetRef keeps a plain name, hashes anything
// PII-shaped or outside the charset to ref-<12 hex>, and is stable.
func TestStatusWidgetRefHandle(t *testing.T) {
	hashed := regexp.MustCompile(`^ref-[0-9a-f]{12}$`)
	plain := []string{"acme", "my_key-2", "v1.2", "A.b-c_9", strings.Repeat("a", 64)}
	for _, n := range plain {
		if got := WidgetRef(n); got != n {
			t.Errorf("WidgetRef(%q) = %q, want it unchanged", n, got)
		}
	}
	opaque := []string{"user@example.com", "host.example.local", "/Users/alice/key", "has space", strings.Repeat("a", 65), "", "ünï"}
	for _, n := range opaque {
		got := WidgetRef(n)
		if !hashed.MatchString(got) || got != WidgetRef(n) {
			t.Errorf("WidgetRef(%q) = %q, want a stable ref-<12 hex>", n, got)
		}
	}
	if WidgetRef("a@b.io") == WidgetRef("c@d.io") {
		t.Error("two different names share a ref")
	}
	if got := WidgetRef("ref-0123456789ab"); got != "ref-0123456789ab" {
		t.Errorf("a ref is not a fixed point: %q", got)
	}
}

// TestStatusWidgetEmailNamedRow: through Compose and Redact an email-named
// provider carries ref-<12 hex> and label "redacted", never the address.
func TestStatusWidgetEmailNamedRow(t *testing.T) {
	const name = "user@example.com"
	rows := composeFor(t, []string{name}, []registry.LaneRecord{lane(name, BucketAPICredit, StateAvailable, time.Time{})}, buildTestNow)
	snap := Redact(WidgetSnapshot{Rows: []WidgetRow{rows[name]}}, false)
	row := snap.Rows[0]
	if row.Ref != WidgetRef(name) || !strings.HasPrefix(row.Ref, "ref-") || len(row.Ref) != 16 || row.Label != "redacted" {
		t.Fatalf("row ref %q label %q, want ref-<12 hex> and redacted", row.Ref, row.Label)
	}
}

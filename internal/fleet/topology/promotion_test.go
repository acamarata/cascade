package topology

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakePosteriors is a test PosteriorSource keyed by lane id.
type fakePosteriors struct {
	byLane map[LaneID][]Posterior
	err    error
}

func (f *fakePosteriors) LanePosteriors(_ context.Context, lane LaneID) ([]Posterior, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byLane[lane], nil
}

// recordingAuditor is a test PromotionAuditor that can be told to fail.
type recordingAuditor struct {
	events []PromotionEvent
	fail   error
}

func (a *recordingAuditor) RecordPromotion(_ context.Context, ev PromotionEvent) error {
	if a.fail != nil {
		return a.fail
	}
	a.events = append(a.events, ev)
	return nil
}

// isolateHome gives each test a fresh HOME, USERPROFILE and CASCADE_HOME.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade"))
}

// seedPromotionLane stores one live unranked lane named id.
func seedPromotionLane(t *testing.T, s *Store, id LaneID) {
	t.Helper()
	seedAccount(t, s, "acct-x")
	seedDomain(t, s, "acct-x:api", "acct-x")
	seedProfile(t, s, "acct-x:provider-x")
	lane := Lane{ID: id, RuntimeProfileRef: "acct-x:provider-x", QuotaDomainRef: "acct-x:api", ModelID: "model-a",
		Health: LaneHealthAvailable, LaneClass: LaneClassUnranked, BaseShadowPrice: 1.0, DiscoveredAt: newTestClock().Now()}
	if err := s.UpsertLane(context.Background(), lane); err != nil {
		t.Fatalf("seed lane %q: %v", id, err)
	}
}

// laneColumnsRaw reads lane_class and base_shadow_price straight from SQL,
// so a row whose other columns no longer decode can still be checked.
func laneColumnsRaw(t *testing.T, s *Store, id LaneID) (LaneClass, float64) {
	t.Helper()
	var class string
	var price float64
	row := s.db.QueryRowContext(context.Background(), `SELECT lane_class, base_shadow_price FROM `+tableLane+` WHERE id = ?`, string(id))
	if err := row.Scan(&class, &price); err != nil {
		t.Fatalf("read lane %q columns: %v", id, err)
	}
	return LaneClass(class), price
}

// chainHolds reports whether target itself (pointer identity) is in err's chain.
func chainHolds(err error, target *cascade.Error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if ce, ok := e.(*cascade.Error); ok && ce == target {
			return true
		}
	}
	return false
}

func assertSentinel(t *testing.T, err error, target *cascade.Error, msg string) {
	t.Helper()
	if err == nil || !chainHolds(err, target) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("err = %v, want sentinel %q with message containing %q", err, target.Msg, msg)
	}
}

func TestUnrankedPromotionThresholds(t *testing.T) {
	isolateHome(t)
	cases := []struct {
		samples int
		mean    float64
		ok      bool
		reason  string
	}{
		{50, 0.80, true, ""},
		{51, 0.95, true, ""},
		{50, 1.0, true, ""},
		{49, 0.80, false, "fewer than 50 samples"},
		{50, 0.7999, false, "below 0.80"},
		{-1, 0.9, false, "negative sample count"},
		{60, -0.1, false, "outside [0, 1]"},
		{60, 1.01, false, "outside [0, 1]"},
		{60, math.NaN(), false, "not a finite number"},
		{60, math.Inf(1), false, "not a finite number"},
	}
	for _, c := range cases {
		ok, reason := PromotionEligible(c.samples, c.mean)
		if ok != c.ok || (!ok && (reason == "" || !strings.Contains(reason, c.reason))) || (ok && reason != "") {
			t.Errorf("PromotionEligible(%d, %v) = (%v, %q), want (%v, ~%q)", c.samples, c.mean, ok, reason, c.ok, c.reason)
		}
	}
	checkThresholdPromotionPass(t)
}

// checkThresholdPromotionPass runs the store half of
// TestUnrankedPromotionThresholds: failed passes write nothing, a passing
// pass promotes only the lane that meets the thresholds.
func checkThresholdPromotionPass(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s := newTestStore(t)
	seedPromotionLane(t, s, "lane-pass")
	seedPromotionLane(t, s, "lane-short")
	audit := &recordingAuditor{}
	src := &fakePosteriors{byLane: map[LaneID][]Posterior{
		"lane-pass":  {{TaskClass: TaskClassClassify, Samples: 50, Mean: 0.80}},
		"lane-short": {{TaskClass: TaskClassClassify, Samples: 49, Mean: 0.99}, {TaskClass: TaskClassExtract, Samples: 80, Mean: 0.7999}},
	}}

	// A nil source and a failing source both write nothing.
	if _, err := s.ApplyPromotions(ctx, nil, newTestClock(), audit); err == nil {
		t.Fatal("nil source: want an error")
	}
	srcErr := cascade.New(cascade.KindUnavailable, "posterior store offline")
	if _, err := s.ApplyPromotions(ctx, &fakePosteriors{err: srcErr}, newTestClock(), audit); !errors.Is(err, srcErr) || !chainHolds(err, srcErr) {
		t.Fatalf("source error: got %v, want the source error returned", err)
	}
	if c, p := laneColumnsRaw(t, s, "lane-pass"); c != LaneClassUnranked || p != 1.0 || len(audit.events) != 0 {
		t.Fatalf("failed passes wrote: class=%s price=%v events=%d", c, p, len(audit.events))
	}

	rep, err := s.ApplyPromotions(ctx, src, newTestClock(), audit)
	if err != nil {
		t.Fatalf("ApplyPromotions: %v", err)
	}
	if !reflect.DeepEqual(rep.Promoted, []LaneID{"lane-pass"}) {
		t.Fatalf("Promoted = %v, want [lane-pass]", rep.Promoted)
	}
	want, _ := BaseShadowPrice(LaneClassWorkerDedicated)
	if c, p := laneColumnsRaw(t, s, "lane-pass"); c != LaneClassWorkerDedicated || p != want {
		t.Fatalf("lane-pass = (%s, %v), want (worker-dedicated, %v)", c, p, want)
	}
	if c, p := laneColumnsRaw(t, s, "lane-short"); c != LaneClassUnranked || p != 1.0 {
		t.Fatalf("lane-short moved to (%s, %v)", c, p)
	}
	if len(audit.events) != 1 || audit.events[0].Kind != PromotionEventPromoted || audit.events[0].Lane != "lane-pass" {
		t.Fatalf("audit events = %+v, want one promotion of lane-pass", audit.events)
	}
	again, err := s.ApplyPromotions(ctx, src, newTestClock(), audit)
	if err != nil || again.RowsChanged != 0 {
		t.Fatalf("repeat pass: rows=%d err=%v, want 0 rows", again.RowsChanged, err)
	}
}

func TestUnrankedEligibleTaskClasses(t *testing.T) {
	isolateHome(t)
	want := []TaskClass{TaskClassClassify, TaskClassExtract, TaskClassSummarize, TaskClassSegment}
	if got := EligibleTaskClasses(LaneClassUnranked); !reflect.DeepEqual(got, want) {
		t.Fatalf("EligibleTaskClasses(unranked) = %v, want %v", got, want)
	}
	if got := UnrankedTaskClasses(); !reflect.DeepEqual(got, want) {
		t.Fatalf("UnrankedTaskClasses = %v, want %v", got, want)
	}
	for _, c := range []LaneClass{LaneClassWorkerDedicated, LaneClassExecutive, "not-a-class", ""} {
		if got := EligibleTaskClasses(c); got != nil {
			t.Errorf("EligibleTaskClasses(%q) = %v, want nil", c, got)
		}
	}

	// A passing posterior on a non-eligible task class never promotes;
	// the same evidence on an eligible class does.
	ctx := context.Background()
	s := newTestStore(t)
	seedPromotionLane(t, s, "lane-code")
	seedPromotionLane(t, s, "lane-seg")
	src := &fakePosteriors{byLane: map[LaneID][]Posterior{
		"lane-code": {{TaskClass: "codegen", Samples: 500, Mean: 0.99, FamilyHint: LaneClassWorkerFast}},
		"lane-seg":  {{TaskClass: TaskClassSegment, Samples: 500, Mean: 0.99, FamilyHint: LaneClassWorkerFast}},
	}}
	if _, err := s.ApplyPromotions(ctx, src, newTestClock(), &recordingAuditor{}); err != nil {
		t.Fatalf("ApplyPromotions: %v", err)
	}
	if c, _ := laneColumnsRaw(t, s, "lane-code"); c != LaneClassUnranked {
		t.Fatalf("lane-code promoted to %s on a non-eligible task class", c)
	}
	price, _ := BaseShadowPrice(LaneClassWorkerFast)
	if c, p := laneColumnsRaw(t, s, "lane-seg"); c != LaneClassWorkerFast || p != price {
		t.Fatalf("lane-seg = (%s, %v), want (worker-fast, %v)", c, p, price)
	}
}

func TestPromotionTieBreakLexicographic(t *testing.T) {
	isolateHome(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "promotion_families.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var cases []struct {
		Name  string      `json:"name"`
		Hints []LaneClass `json:"hints"`
		Want  LaneClass   `json:"want"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(cases) < 6 {
		t.Fatalf("golden has %d cases, want at least 6", len(cases))
	}
	for _, c := range cases {
		if got := PromotionFamily(c.Hints); got != c.Want {
			t.Errorf("%s: PromotionFamily(%v) = %s, want %s", c.Name, c.Hints, got, c.Want)
		}
	}

	// End to end: a tie between two passing hints lands on the smaller class.
	ctx := context.Background()
	s := newTestStore(t)
	seedPromotionLane(t, s, "lane-tie")
	src := &fakePosteriors{byLane: map[LaneID][]Posterior{"lane-tie": {
		{TaskClass: TaskClassSummarize, Samples: 70, Mean: 0.9, FamilyHint: LaneClassWorkerFast},
		{TaskClass: TaskClassExtract, Samples: 70, Mean: 0.9, FamilyHint: LaneClassAPIFree},
		{TaskClass: TaskClassClassify, Samples: 10, Mean: 0.9, FamilyHint: LaneClassWorkerFast},
	}}}
	if _, err := s.ApplyPromotions(ctx, src, newTestClock(), &recordingAuditor{}); err != nil {
		t.Fatalf("ApplyPromotions: %v", err)
	}
	price, _ := BaseShadowPrice(LaneClassAPIFree)
	if c, p := laneColumnsRaw(t, s, "lane-tie"); c != LaneClassAPIFree || p != price {
		t.Fatalf("lane-tie = (%s, %v), want (api-free, %v)", c, p, price)
	}
}

// stepClock is a settable Clock for the reversal tests.
type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time { return c.t }

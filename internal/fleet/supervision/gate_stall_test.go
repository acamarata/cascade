package supervision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

var gateBase = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

// gateFake counts every GateStallActions call and fails the step named
// in failStep ("lastdenial", "failjob", "releases", "nilrelease",
// "cancel", "retryhint").
type gateFake struct {
	failStep                               string
	denial                                 GateDenial
	lastDenial, failJob, releases, release int
	retryHint                              int
}

func (f *gateFake) actions() GateStallActions {
	boom := cascade.New(cascade.KindUnavailable, "boom")
	return GateStallActions{
		LastDenial: func(_ context.Context, _ string) (GateDenial, error) {
			f.lastDenial++
			if f.failStep == "lastdenial" {
				return GateDenial{}, boom
			}
			return f.denial, nil
		},
		FailJob: func(context.Context, string, GateDenial) error {
			f.failJob++
			if f.failStep == "failjob" {
				return boom
			}
			return nil
		},
		Releases: func(context.Context, string) ([]nodes.ScopeRelease, error) {
			f.releases++
			rel := func() error {
				f.release++
				if f.failStep == "cancel" {
					return boom
				}
				return nil
			}
			switch f.failStep {
			case "releases":
				return nil, boom
			case "nilrelease":
				return []nodes.ScopeRelease{rel, nil}, nil
			}
			return []nodes.ScopeRelease{rel}, nil
		},
		RetryHint: func(int) (time.Duration, error) {
			f.retryHint++
			if f.failStep == "retryhint" {
				return 0, boom
			}
			return 45 * time.Minute, nil
		},
	}
}

func newGateFake(step string) *gateFake {
	return &gateFake{failStep: step, denial: GateDenial{Attempt: 3, Reason: "tests failed: x\ny", DeniedAt: gateBase}}
}

// observeGate sends n gate-denied signals for job in session, ten
// minutes apart from start.
func observeGate(t *testing.T, d *Detector, session, job string, start time.Time, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * 10 * time.Minute).UnixMilli()
		sig := StallSignal{Kind: SignalGateDenied, SessionID: session, JobID: job, At: at}
		if err := d.Observe(context.Background(), sig); err != nil {
			t.Logf("Observe %s/%s #%d: %v (ladder outcome, not the hook)", session, job, i+1, err)
		}
	}
}

func listKind(t *testing.T, s *Store, ref ScopeRef, kind Kind) []AttentionItem {
	t.Helper()
	items, err := s.ListInScopes(context.Background(), []ScopeRef{ref}, Filter{KindFilter: &kind, IncludeAcked: true})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	return items
}

func globalScope() ScopeRef { return ScopeRef{Kind: scope.ScopeKindGlobal, ID: "global"} }

func wantOneError(t *testing.T, s *Store, src string) {
	t.Helper()
	items := listKind(t, s, sessionScope("S1"), KindError)
	if len(items) != 1 {
		t.Fatalf("error items = %v, want exactly one %q", items, src)
	}
	it := items[0]
	if it.SourceRef != src || it.Priority != 0 || it.ScopeRef != sessionScope("S1") {
		t.Fatalf("error item = %+v, want {KindError %q {session S1} 0}", it, src)
	}
}

func TestGateStallActionsUnsetPushesCriticalItem(t *testing.T) {
	d, s, _ := newTestDetector(t, gateBase, fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
	observeGate(t, d, "S1", "J1", gateBase, 2)
	if items := listKind(t, s, sessionScope("S1"), KindError); len(items) != 0 {
		t.Fatalf("after two denials: error items = %v, want none", items)
	}
	observeGate(t, d, "S1", "J1", gateBase.Add(20*time.Minute), 1)
	wantOneError(t, s, "gate-stall-unwired:J1")
}

func TestGateStallActionFailuresFailClosed(t *testing.T) {
	cases := []struct{ fail, step string }{
		{"lastdenial", "lastdenial"}, {"failjob", "failjob"}, {"releases", "releases"},
		{"nilrelease", "releases"}, {"cancel", "cancel"}, {"retryhint", "retryhint"},
	}
	for _, tc := range cases {
		t.Run(tc.fail, func(t *testing.T) {
			d, s, _ := newTestDetector(t, gateBase, fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
			f := newGateFake(tc.fail)
			if err := d.WithGateStallActions(f.actions()); err != nil {
				t.Fatalf("WithGateStallActions: %v", err)
			}
			observeGate(t, d, "S1", "J1", gateBase, 3)
			wantOneError(t, s, "gate-stall-"+tc.step+"-failed:J1")
			if n := len(listKind(t, s, sessionScope("S1"), KindStall)) + len(listKind(t, s, globalScope(), KindStall)); n != 0 {
				t.Fatalf("KindStall items = %d, want 0", n)
			}
			checkGateCounters(t, tc.fail, f)
		})
	}
}

// checkGateCounters asserts no step after the failing one ran.
func checkGateCounters(t *testing.T, fail string, f *gateFake) {
	t.Helper()
	order := []string{"lastdenial", "failjob", "releases", "cancel", "retryhint"}
	got := []int{f.lastDenial, f.failJob, f.releases, f.release, f.retryHint}
	stop := map[string]int{"lastdenial": 0, "failjob": 1, "releases": 2, "nilrelease": 2, "cancel": 3, "retryhint": 4}[fail]
	for i, n := range got {
		want := 0
		if i <= stop {
			want = 1
		}
		if n != want {
			t.Errorf("%s: %s ran %d times, want %d (counters %v)", fail, order[i], n, want, got)
		}
	}
}

func TestWithGateStallActionsNilFieldInvalid(t *testing.T) {
	d, s, _ := newTestDetector(t, gateBase, fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
	for _, field := range []string{"LastDenial", "FailJob", "Releases", "RetryHint"} {
		a := newGateFake("").actions()
		switch field {
		case "LastDenial":
			a.LastDenial = nil
		case "FailJob":
			a.FailJob = nil
		case "Releases":
			a.Releases = nil
		case "RetryHint":
			a.RetryHint = nil
		}
		err := d.WithGateStallActions(a)
		if err == nil {
			t.Fatalf("%s nil: err = nil, want ErrGateStallActionsInvalid", field)
		}
		if !wrapsIdentity(err, ErrGateStallActionsInvalid) {
			t.Errorf("%s nil: err %v does not wrap ErrGateStallActionsInvalid by identity", field, err)
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), field) {
			t.Errorf("%s nil: err %v, want KindInvalidInput naming the field", field, err)
		}
	}
	observeGate(t, d, "S1", "J1", gateBase, 3)
	wantOneError(t, s, "gate-stall-unwired:J1")
}

// wrapsIdentity walks err's chain comparing pointers, since
// cascade's errors.Is matches any error of the same Kind.
func wrapsIdentity(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}

func TestGateRetryHintInAttentionItem(t *testing.T) {
	d, s, _ := newTestDetector(t, gateBase, fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
	f := newGateFake("")
	if err := d.WithGateStallActions(f.actions()); err != nil {
		t.Fatalf("WithGateStallActions: %v", err)
	}
	observeGate(t, d, "S1", "J1", gateBase, 3)
	items := listKind(t, s, sessionScope("S1"), KindStall)
	want := "gate-stall:J1 attempt 3/3; next retry not before 2026-11-05T10:45:00Z; reason: tests failed: x\ny"
	if len(items) != 1 || items[0].SourceRef != want || items[0].Priority != 1 || items[0].ScopeRef != sessionScope("S1") {
		t.Fatalf("stall items = %+v, want one {KindStall {session S1} 1 %q}", items, want)
	}
	if f.failJob != 1 || f.release != 1 {
		t.Fatalf("FailJob ran %d, release ran %d, want 1 and 1", f.failJob, f.release)
	}
}

func TestThirdScopedStallPushesGlobalItem(t *testing.T) {
	d, s, _ := newTestDetector(t, gateBase, fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
	f := newGateFake("")
	if err := d.WithGateStallActions(f.actions()); err != nil {
		t.Fatalf("WithGateStallActions: %v", err)
	}
	observeGate(t, d, "S1", "J1", gateBase, 3)
	observeGate(t, d, "S1", "J2", gateBase, 3)
	if items := listKind(t, s, globalScope(), KindStall); len(items) != 0 {
		t.Fatalf("after J2: global items = %v, want none", items)
	}
	observeGate(t, d, "S1", "J3", gateBase, 3)
	wantGlobal := func(stage string) {
		items := listKind(t, s, globalScope(), KindStall)
		if len(items) != 1 || items[0].SourceRef != "stall-scope:session:S1:3" || items[0].Priority != 0 {
			t.Fatalf("%s: global items = %+v, want one {KindStall global 0 stall-scope:session:S1:3}", stage, items)
		}
	}
	wantGlobal("after J3")
	observeGate(t, d, "S2", "J4", gateBase, 3)
	wantGlobal("after J4 in S2")
}

func TestGateStallRunsOncePerEpisode(t *testing.T) {
	d, s, _ := newTestDetector(t, gateBase, fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
	f := newGateFake("")
	if err := d.WithGateStallActions(f.actions()); err != nil {
		t.Fatalf("WithGateStallActions: %v", err)
	}
	observeGate(t, d, "S1", "J1", gateBase, 3)
	before := len(listKind(t, s, sessionScope("S1"), KindStall)) + len(listKind(t, s, sessionScope("S1"), KindError))
	observeGate(t, d, "S1", "J1", gateBase.Add(25*time.Minute), 1)
	after := len(listKind(t, s, sessionScope("S1"), KindStall)) + len(listKind(t, s, sessionScope("S1"), KindError))
	if f.failJob != 1 || f.release != 1 {
		t.Fatalf("FailJob ran %d, release ran %d, want 1 and 1 in total", f.failJob, f.release)
	}
	if after != before {
		t.Fatalf("items %d -> %d after the fourth denial, want no new item", before, after)
	}
}

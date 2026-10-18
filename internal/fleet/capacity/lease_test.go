// Purpose: Fleet.Lease's tier and sensitivity outcomes over a real database:
// the jump triggers, the exhausted walk, the reserved tier-0, the local-only
// constraint, the data-class denial and node placement carrying the request's
// joined tier. Every refusal also proves zero rows.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// setState sets one snapshot lane's state.
func (f *fx) setState(lane conductor.LaneID, s State) {
	slot := f.snap.Lanes[lane]
	slot.State = s
	f.snap.Lanes[lane] = slot
}

// decisionOf returns the recorded decision for a lease's decision id.
func (f *fx) decisionOf(id string) DecisionRecord {
	f.t.Helper()
	for _, d := range f.decisions.recorded {
		if d.ID == id {
			return d
		}
	}
	f.t.Fatalf("no decision %q recorded", id)
	return DecisionRecord{}
}

func TestFleetLeaseJumpTriggers(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*fx, *ResourceRequest)
		attempt int
		want    JumpReasonCode
	}{
		{"risk_class high with a low tier-0 score", func(f *fx, r *ResourceRequest) {
			r.RiskClass = jobs.RiskClassHigh
			f.scores.scores[TierZero] = 0.5
		}, 1, JumpReasonRiskScore},
		{"attempt two", func(*fx, *ResourceRequest) {}, 2, JumpReasonAttempt},
		{"min_quality max", func(_ *fx, r *ResourceRequest) { r.MinQuality = QualityMax }, 1, JumpReasonMinQuality},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			f.seedJob("job-1", provider.SensitivityInternal)
			req := request()
			c.mutate(f, &req)
			l, err := f.fleet.Lease(t.Context(), req, dispatchFor("job-1"), c.attempt)
			if err != nil {
				t.Fatalf("Lease: %v", err)
			}
			d := f.decisionOf(l.DecisionID)
			if l.SelectedTier != TierZero || l.LaneID != "lane-t0" || !d.JumpRuleFired || d.JumpReasonCode != c.want {
				t.Fatalf("lease %+v decision %+v, want tier-0 on lane-t0 with jump reason %q", l, d, c.want)
			}
			var code string
			if err := f.db.QueryRow(`SELECT jump_code FROM test_decision WHERE id = ?`, l.DecisionID).Scan(&code); err != nil || code != string(c.want) {
				t.Fatalf("stored jump_code = (%q, %v), want %q", code, err, c.want)
			}
		})
	}
}

func TestFleetLeaseNoCapableTier(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	for id := range f.snap.Lanes {
		f.setState(id, StateExhausted)
	}
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoCapableTier)
	requireSentinel(t, err, ErrFleetExhausted)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err kind = %v, want KindUnavailable", err)
	}
	f.zeroRows()
}

func TestFleetLeaseTier0Reserved(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	f.setState("lane-t2", StateExhausted)
	f.setState("lane-t1", StateExhausted)
	req := request()
	req.ReserveTier0 = true
	_, err := f.fleet.Lease(t.Context(), req, dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrTier0Reserved)
	if hasSentinel(err, ErrNoCapableTier) {
		t.Fatalf("err = %v, a reserved tier-0 must not read as ErrNoCapableTier", err)
	}
	f.zeroRows()
	l, err := f.fleet.Lease(t.Context(), req, dispatchFor("job-1"), 2)
	if err != nil {
		t.Fatalf("Lease(attempt 2): %v", err)
	}
	d := f.decisionOf(l.DecisionID)
	if l.SelectedTier != TierZero || !d.ReserveTier0 || !d.JumpRuleFired || d.JumpReasonCode != JumpReasonAttempt {
		t.Fatalf("lease %+v decision %+v, want tier-0 with the reserve recorded and the jump not suppressed", l, d)
	}
}

func TestFleetLeaseSensitivityConstraint(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-local", provider.SensitivityLocalOnly)
	delete(f.snap.Lanes, "lane-t0")
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-local"), 1)
	requireSentinel(t, err, ErrSensitivityConstraint, "lane-t1 (external-api)", "lane-t2 (external-api)", "local-only")
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied", err)
	}
	f.zeroRows()

	f = newFx(t)
	f.seedJob("job-local", provider.SensitivityLocalOnly)
	d := dispatchFor("job-local")
	d.Placement = &PlacementNeed{Capabilities: []string{"docker"}}
	_, err = f.fleet.Lease(t.Context(), request(), d, 1)
	requireSentinel(t, err, ErrSensitivityConstraint, "node-worker")
	f.zeroRows()
}

func TestFleetLeaseDataClassDenied(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-r", provider.SensitivityRestricted)
	delete(f.snap.Lanes, "lane-t0")
	req := request()
	req.Sensitivity = provider.SensitivityRestricted
	_, err := f.fleet.Lease(t.Context(), req, dispatchFor("job-r"), 1)
	requireSentinel(t, err, ErrDataClassDenied, "lane-t1 (external-api)", "lane-t2 (external-api)")
	f.zeroRows()

	below := provider.SensitivityPublic
	req.DataClass = &below
	_, err = f.fleet.Lease(t.Context(), req, dispatchFor("job-r"), 1)
	if !cascade.HasKind(err, cascade.KindPolicyDenied) || hasSentinel(err, ErrDataClassDenied) || !strings.Contains(err.Error(), `data_class "public" is below the derived floor "confidential"`) {
		t.Fatalf("err = %v, want the ValidateResourceRequest below-floor refusal (KindPolicyDenied)", err)
	}
	f.zeroRows()

	permits, perr := conductor.ParseRestrictedPermits(map[string]interface{}{"restricted_external_lanes": []string{"lane-t2"}})
	if perr != nil {
		t.Fatalf("ParseRestrictedPermits: %v", perr)
	}
	f.deps.RestrictedPermits = permits
	f.rebuild()
	req.DataClass = nil
	l, err := f.fleet.Lease(t.Context(), req, dispatchFor("job-r"), 1)
	if err != nil || l.LaneID != "lane-t2" || l.Sensitivity != provider.SensitivityRestricted {
		t.Fatalf("Lease with lane-t2 permitted = (%+v, %v), want lane-t2 at restricted", l, err)
	}
	r, _, _ := f.fleet.ReservationByExecution(t.Context(), l.ExecutionID)
	if r.Sensitivity != "restricted" {
		t.Fatalf("reservation sensitivity = %q, want restricted", r.Sensitivity)
	}
}

// pairedDevicePlace is nodes.Engine over one paired-device node that reports "gpu".
func pairedDevicePlace(trusted bool) func(context.Context, nodes.Requirement) (string, error) {
	tier := nodes.TierPairedDevice
	if trusted {
		tier = nodes.TierWorkerTrusted
	}
	rec := nodes.DeviceRecord{NodeID: "node-gpu", Tier: tier, Presence: nodes.PresenceReachable,
		LastReport: nodes.CapabilityReport{Capabilities: []string{"gpu"}}}
	engine := nodes.Engine{Tunnels: func(string) nodes.TunnelState { return nodes.TunnelUp }}
	return func(_ context.Context, req nodes.Requirement) (string, error) {
		got, err := engine.Eligible(req, nodes.CandidatesFrom([]nodes.DeviceRecord{rec}))
		if err != nil {
			return "", err
		}
		return got[0].NodeID, nil
	}
}

func TestFleetLeasePlacementSensitivityFromRequest(t *testing.T) {
	typ := reflect.TypeFor[PlacementNeed]()
	for i := range typ.NumField() {
		if ft := typ.Field(i).Type; ft == reflect.TypeFor[provider.SensitivityTier]() || typ.Field(i).Name == "Sensitivity" {
			t.Fatalf("PlacementNeed.%s: the requirement's tier is derived, never a field", typ.Field(i).Name)
		}
	}
	f := newFx(t)
	f.seedJob("job-r", provider.SensitivityRestricted)
	f.deps.Place = pairedDevicePlace(false)
	f.rebuild()
	req := request()
	req.Sensitivity = provider.SensitivityRestricted
	d := dispatchFor("job-r")
	d.Placement = &PlacementNeed{Capabilities: []string{"gpu"}}
	_, err := f.fleet.Lease(t.Context(), req, d, 1)
	requireSentinel(t, err, ErrSensitivityConstraint, "trust_tier paired-device is below worker-trusted")
	f.zeroRows()

	pub := newFx(t)
	pub.seedJob("job-p", provider.SensitivityPublic)
	pub.deps.Place = pairedDevicePlace(false)
	pub.rebuild()
	req.Sensitivity = provider.SensitivityPublic
	d.JobID = "job-p"
	if l, err := pub.fleet.Lease(t.Context(), req, d, 1); err != nil || l.NodeID != "node-gpu" {
		t.Fatalf("public Lease on the paired device = (%+v, %v), want node-gpu", l, err)
	}

	trusted := newFx(t)
	trusted.seedJob("job-r", provider.SensitivityRestricted)
	trusted.deps.Place = pairedDevicePlace(true)
	trusted.rebuild()
	req.Sensitivity = provider.SensitivityRestricted
	d.JobID = "job-r"
	if l, err := trusted.fleet.Lease(t.Context(), req, d, 1); err != nil || l.NodeID != "node-gpu" {
		t.Fatalf("restricted Lease on a worker-trusted node = (%+v, %v), want node-gpu", l, err)
	}
}

func TestFleetCommitRefusesALeaseThatDoesNotOwnItsReservation(t *testing.T) {
	f := newFx(t)
	a := abortLease(t, f)
	d := scopedDispatch("job-1")
	d.RepoID, d.ScopeGlobs = "repo-b", []string{"other/**"}
	b, err := f.fleet.Lease(t.Context(), request(), d, 1)
	if err != nil {
		t.Fatalf("Lease b: %v", err)
	}
	mixed := a
	mixed.ReservationID = b.ReservationID
	requireSentinel(t, f.fleet.Commit(t.Context(), mixed), ErrLeaseUnknown, b.ReservationID)
	if held, _ := f.store.ListByState(t.Context(), economics.ReservationHeld); len(held) != 2 {
		t.Fatalf("%d held rows, want both reservations still held", len(held))
	}
	if err := f.fleet.Commit(t.Context(), a); err != nil {
		t.Fatalf("Commit of the owning lease = %v, want nil", err)
	}
}

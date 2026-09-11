// Purpose: tests for LastProbeResult (probe.go) and the ChangedPayload
//   Endpoint/ProbeStatus fields (health.go) -- the "probe failure detail"
//   acceptance criterion's storage half -- and the security properties the
//   AGENT-BRIEF names explicitly: fail-closed on an ambiguous probe result,
//   never a fallthrough to healthy. Split from probe_test.go to stay under
//   the 300-line file cap.
// SPORT: provider.health/ADD (P1-E10-W3-S20-T3).

package health

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestRecoveryEventCarriesProbeEndpointAndStatus: TestRecoveryReset (in
// probe_test.go) already asserts the recovery event's status fields; this
// test asserts the ADDED Endpoint/ProbeStatus fields on that same success
// path, plus LastProbeResult's storage half of the "probe failure detail"
// acceptance criterion.
func TestRecoveryEventCarriesProbeEndpointAndStatus(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{Success: true, Status: 200, Endpoint: "https://api.example/v1/models"}}
	eng := testEngine(t)
	mgr, reg, bus := newTestManagerWithEgress(t, clk, 3, eng, prober)
	seedProvider(t, reg, "p1")
	ctx := context.Background()
	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
		t.Fatalf("seed demotion: %v", err)
	}
	ch := subscribeEvents(t, bus, eventNamespace, "endpoint-cursor")
	<-ch // drain the setup demotion event

	if _, err := mgr.RecoverProbe(ctx, "p1"); err != nil {
		t.Fatalf("RecoverProbe: %v", err)
	}
	ev := <-ch
	var payload ChangedPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	if payload.Endpoint != "https://api.example/v1/models" || payload.ProbeStatus != 200 {
		t.Fatalf("recovery event missing probe detail: %+v", payload)
	}

	got, ok := mgr.LastProbeResult("p1")
	if !ok || got.Endpoint != "https://api.example/v1/models" || got.Status != 200 {
		t.Fatalf("LastProbeResult after success = %+v, ok=%v", got, ok)
	}
}

// TestLastProbeResultRecordsFailureDetailAndFailsClosed: a failing probe's
// endpoint/status is retrievable via LastProbeResult (the storage half of
// "probe failure detail"), and the failure never flips health_status --
// fail-closed, not "assume healthy".
func TestLastProbeResultRecordsFailureDetailAndFailsClosed(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{Success: false, Status: 500, TransportErr: "server error", Endpoint: "https://api.example/v1/models"}}
	eng := testEngine(t)
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 3, eng, prober)
	seedProvider(t, reg, "p1")
	ctx := context.Background()

	recovered, err := mgr.RecoverProbe(ctx, "p1")
	if err != nil || recovered {
		t.Fatalf("RecoverProbe on a failing probe: recovered=%v err=%v, want false/nil", recovered, err)
	}
	rec, _ := reg.GetProvider(ctx, "p1")
	if rec.HealthStatus == registry.HealthHealthy {
		t.Fatal("a failing probe must never fail OPEN into healthy")
	}
	got, ok := mgr.LastProbeResult("p1")
	if !ok || got.Status != 500 || got.Endpoint != "https://api.example/v1/models" || got.TransportErr != "server error" {
		t.Fatalf("LastProbeResult after failure = %+v, ok=%v", got, ok)
	}
}

// TestLastProbeResultUnknownProviderReturnsFalse: a provider that has never
// been probed reports ok=false, never a stale/zero-value success.
func TestLastProbeResultUnknownProviderReturnsFalse(t *testing.T) {
	mgr := NewManager(nil, nil, testkit.NewFrozenClock(time.Now()), 3)
	if _, ok := mgr.LastProbeResult("never-probed"); ok {
		t.Fatal("LastProbeResult ok=true for a provider that was never probed")
	}
}

// TestRecoverProbeAmbiguousResultFailsClosed: an "ambiguous" probe outcome
// -- Success=false paired with a zero/unparseable Status and no
// TransportErr, i.e. exactly what a Prober returns when it cannot classify
// the response at all -- still takes the failure branch: no recovery, no
// health_status mutation, never a fallthrough to healthy.
func TestRecoverProbeAmbiguousResultFailsClosed(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{}} // zero value: Success=false, Status=0, no error string
	eng := testEngine(t)
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 3, eng, prober)
	seedProvider(t, reg, "p1")
	ctx := context.Background()

	before, _ := reg.GetProvider(ctx, "p1")
	recovered, err := mgr.RecoverProbe(ctx, "p1")
	if err != nil {
		t.Fatalf("RecoverProbe: %v", err)
	}
	if recovered {
		t.Fatal("an ambiguous zero-value probe result must never report recovered=true")
	}
	after, _ := reg.GetProvider(ctx, "p1")
	if after.HealthStatus != before.HealthStatus || after.HealthStatus == registry.HealthHealthy {
		t.Fatalf("ambiguous probe result mutated health state: before=%s after=%s", before.HealthStatus, after.HealthStatus)
	}
}

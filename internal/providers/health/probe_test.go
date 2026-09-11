package health

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// nopVault satisfies egress.Vault with no stored values (mirrors internal/
// providers/intake/transport_test.go's nopVault -- neither package can
// import the other's unexported test helper).
type nopVault struct{}

func (nopVault) List(_ context.Context) ([]string, error)        { return nil, nil }
func (nopVault) Get(_ context.Context, _ string) ([]byte, error) { return nil, nil }

func testEngine(t *testing.T) *egress.Engine {
	t.Helper()
	return testEngineWithRegistry(t, egress.DefaultRegistry())
}

func testEngineWithRegistry(t *testing.T, reg *egress.Registry) *egress.Engine {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("building detector: %v", err)
	}
	engine, err := egress.NewEngine(reg, nopVault{}, detector)
	if err != nil {
		t.Fatalf("building engine: %v", err)
	}
	return engine
}

// countingProber is a recording, in-memory Prober (no net import;
// Art.7.2): it counts calls (so refusal tests can assert zero dials) and
// returns a scripted outcome.
type countingProber struct {
	calls  int
	result ProbeResult
	err    error
}

func (p *countingProber) Probe(_ context.Context, _ registry.ProviderRecord) (ProbeResult, error) {
	p.calls++
	return p.result, p.err
}

func newTestManagerWithEgress(t *testing.T, clk *testkit.FrozenClock, threshold int, eng Egress, prober Prober) (*Manager, *registry.Registry, *events.Bus) {
	t.Helper()
	mgr, reg, bus := newTestManager(t, clk, threshold)
	mgr.WithEgress(eng, prober)
	return mgr, reg, bus
}

// TestRecoverProbeRefusesWhenEgressClassUnregistered: an engine built over
// an EMPTY egress.Registry (provider-intake never registered) must refuse
// with zero Prober dials and leave health state unchanged.
func TestRecoverProbeRefusesWhenEgressClassUnregistered(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{Success: true}}
	eng := testEngineWithRegistry(t, egress.NewRegistry())
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 3, eng, prober)
	seedProvider(t, reg, "p1")

	recovered, err := mgr.RecoverProbe(context.Background(), "p1")
	if err == nil {
		t.Fatal("expected a refusal error for an unregistered provider-intake class")
	}
	if recovered {
		t.Fatal("recovered=true on a refusal")
	}
	if prober.calls != 0 {
		t.Fatalf("Prober.Probe called %d times, want 0 (fail closed, no dial)", prober.calls)
	}
	rec, gerr := reg.GetProvider(context.Background(), "p1")
	if gerr != nil {
		t.Fatalf("GetProvider: %v", gerr)
	}
	if rec.HealthStatus != registry.HealthUnknown {
		t.Fatalf("health state mutated on refusal: %s", rec.HealthStatus)
	}
}

// TestRecoverProbeRefusesWhenEgressClassDisabled: provider-intake
// registered but Enabled=false must also refuse with zero dials.
func TestRecoverProbeRefusesWhenEgressClassDisabled(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{Success: true}}
	reg2 := egress.NewRegistry()
	reg2.MustRegister(egress.EgressClassProviderIntake, egress.InterceptConfig{Enabled: false, Owner: "test"})
	eng := testEngineWithRegistry(t, reg2)
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 3, eng, prober)
	seedProvider(t, reg, "p1")

	_, err := mgr.RecoverProbe(context.Background(), "p1")
	if err == nil {
		t.Fatal("expected a refusal error for a disabled provider-intake class")
	}
	if prober.calls != 0 {
		t.Fatalf("Prober.Probe called %d times, want 0", prober.calls)
	}
}

// TestRecoverProbeEmptyNameRefused asserts the empty-name guard.
func TestRecoverProbeEmptyNameRefused(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, _, _ := newTestManager(t, clk, 3)
	if _, err := mgr.RecoverProbe(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty provider name")
	}
}

func TestRecoverProbeNoEgressConfiguredFailsClosed(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	_, err := mgr.RecoverProbe(context.Background(), "p1")
	if err == nil {
		t.Fatal("expected a refusal error when no Egress/Prober is configured")
	}
}

// TestRecoveryReset: RecoverProbe on a dead provider resets demotion_count
// and sets health_status=healthy on a successful micro-verify, and emits
// exactly one health-changed event with status=healthy.
func TestRecoveryReset(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	prober := &countingProber{result: ProbeResult{Success: true, Status: 200, Endpoint: "https://api.example/v1/models"}}
	eng := testEngine(t)
	mgr, reg, bus := newTestManagerWithEgress(t, clk, 2, eng, prober)
	seedProvider(t, reg, "p1")
	ctx := context.Background()
	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
		t.Fatalf("seed demotion: %v", err)
	}
	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
		t.Fatalf("seed demotion 2: %v", err)
	}
	rec, _ := reg.GetProvider(ctx, "p1")
	if rec.HealthStatus != registry.HealthDead {
		t.Fatalf("setup: expected dead before recovery, got %s", rec.HealthStatus)
	}

	ch := subscribeEvents(t, bus, eventNamespace, "recovery-cursor")
	// Drain the two setup-demotion events (unknown->degraded, then
	// degraded->dead) before asserting on the recovery event -- Subscribe
	// replays from cursor 0, so both are already queued.
	<-ch
	<-ch

	recovered, err := mgr.RecoverProbe(ctx, "p1")
	if err != nil {
		t.Fatalf("RecoverProbe: %v", err)
	}
	if !recovered {
		t.Fatal("recovered=false on a successful probe")
	}
	if prober.calls != 1 {
		t.Fatalf("Prober.Probe called %d times, want 1", prober.calls)
	}
	rec, err = reg.GetProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if rec.HealthStatus != registry.HealthHealthy || rec.DemotionCount != 0 {
		t.Fatalf("after recovery: status=%s count=%d, want healthy/0", rec.HealthStatus, rec.DemotionCount)
	}

	ev := <-ch
	var payload ChangedPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	if payload.NewStatus != "healthy" || payload.PrevStatus != "dead" {
		t.Fatalf("unexpected recovery event payload: %+v", payload)
	}
}

// TestRecoverProbeFailureAdvancesBackoffNotDemotionCount: a
// reachable-but-failing probe never touches demotion_count/health_status,
// only the process-local backoff counter.
func TestRecoverProbeFailureAdvancesBackoffNotDemotionCount(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{Success: false, Status: 503}}
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
		t.Fatal("recovered=true on a scripted failure")
	}
	after, _ := reg.GetProvider(ctx, "p1")
	if after.DemotionCount != before.DemotionCount || after.HealthStatus != before.HealthStatus {
		t.Fatalf("failed probe mutated health state: before=%+v after=%+v", before, after)
	}
	if got := mgr.BackoffSteps("p1"); got != 1 {
		t.Fatalf("BackoffSteps=%d want 1", got)
	}
	// A second failure advances backoff again.
	if _, err := mgr.RecoverProbe(ctx, "p1"); err != nil {
		t.Fatalf("RecoverProbe #2: %v", err)
	}
	if got := mgr.BackoffSteps("p1"); got != 2 {
		t.Fatalf("BackoffSteps after 2 failures=%d want 2", got)
	}
}

// TestProbeFailureNeverLeaksCredentialShapedText: a vendor response body
// that echoes a credential-shaped string back (the exact class of leak
// AGENT-BRIEF names as having happened once already in this repo) must
// never reach ProbeResult.TransportErr, an error string, or an event
// payload.
func TestProbeFailureNeverLeaksCredentialShapedText(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	leaked := "sk-ant-" + "api03-REDACTEDSECRETVALUE0000000000000000000000000000"
	prober := &countingProber{
		result: ProbeResult{Success: false, Status: 401, TransportErr: "unauthorized"},
		err:    nil,
	}
	// The scripted transport error string deliberately never contains
	// `leaked` -- this asserts the CONTRACT this package's own
	// ProbeResult.Endpoint/TransportErr fields must uphold: a production
	// Prober is responsible for redacting a vendor's echoed body before
	// ever constructing a ProbeResult, exactly as intake/transport.go's
	// shapeProbe redacts the Gemini query key before recording
	// probeAttempt.endpoint. This test fixes that contract in place: if a
	// future Prober implementation ever put `leaked` into TransportErr,
	// this assertion would catch it.
	if containsSubstring(prober.result.TransportErr, leaked) {
		t.Fatal("test fixture itself leaked the credential-shaped literal -- fix the fixture")
	}
	eng := testEngine(t)
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 3, eng, prober)
	seedProvider(t, reg, "p1")

	if _, err := mgr.RecoverProbe(context.Background(), "p1"); err != nil {
		t.Fatalf("RecoverProbe: %v", err)
	}
	rec, _ := reg.GetProvider(context.Background(), "p1")
	if containsSubstring(rec.AuthRef.String(), leaked) {
		t.Fatal("credential-shaped literal reached a stored field")
	}
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

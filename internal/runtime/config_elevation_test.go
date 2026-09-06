package runtime

// Purpose: the loosening-via-elevation gate's tests. The expected
//   behaviour is transcribed from the gate's own rule — loosening a
//   guarded family needs a local approval, tightening needs none, and
//   every failure to obtain one refuses — and asserted against those
//   literals rather than against the gate's implementation.
// Constraints: FixedClock only; no sleeps; every refusal path is exercised
//   (no approver, approver error, approver with no reference).

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recordingApprover is a test ElevationApprover. FORWARD NOTE: no
// production implementation exists anywhere in this tree yet.
type recordingApprover struct {
	ref   string
	err   error
	calls int
	seen  []LooseningPath
}

func (a *recordingApprover) ApproveLoosening(_ context.Context, paths []LooseningPath) (string, error) {
	a.calls++
	a.seen = append(a.seen, paths...)
	return a.ref, a.err
}

// looseningReloader returns a reloader whose next Reload loosens
// conductor.external_routing_enabled, the one guarded key with a ratified
// tight/loose ordering.
func looseningReloader(t *testing.T) (*HotReloader, *fakeEventPublisher, func()) {
	t.Helper()
	hr, path, events, _, _ := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = false\n")
	return hr, events, func() {
		_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = true\n")
	}
}

// TestConfigLoosenElevationGranted proves an approved loosening is
// applied and its approval reference is carried on the outcome.
func TestConfigLoosenElevationGranted(t *testing.T) {
	hr, _, loosen := looseningReloader(t)
	approver := &recordingApprover{ref: "elev-ref-1"}
	hr.SetElevationApprover(approver)
	loosen()

	outcome := hr.Reload(context.Background())
	if !outcome.Accepted {
		t.Fatalf("outcome = %+v, want an accepted reload", outcome)
	}
	if outcome.ElevationRef != "elev-ref-1" {
		t.Fatalf("ElevationRef = %q, want the approver's reference", outcome.ElevationRef)
	}
	if approver.calls != 1 {
		t.Fatalf("approver calls = %d, want 1", approver.calls)
	}
	if !extractEffectiveConfig(hr.Current()).Conductor.ExternalRoutingEnabled {
		t.Fatal("the approved loosening was not applied to the running config")
	}
}

// TestConfigLoosenElevationRefAuditLogged proves the approval reference
// reaches the audit trail on success.
func TestConfigLoosenElevationRefAuditLogged(t *testing.T) {
	hr, events, loosen := looseningReloader(t)
	hr.SetElevationApprover(&recordingApprover{ref: "elev-ref-2"})
	loosen()

	if outcome := hr.Reload(context.Background()); !outcome.Accepted {
		t.Fatalf("outcome = %+v, want an accepted reload", outcome)
	}
	fields := acceptedFields(t, events)
	if ref, _ := fields["elevation_ref"].(string); ref != "elev-ref-2" {
		t.Fatalf("elevation_ref = %v, want the approval reference recorded", fields["elevation_ref"])
	}
}

// TestConfigLoosenElevationDenied proves a helper refusal refuses the
// write and names the loosened key.
func TestConfigLoosenElevationDenied(t *testing.T) {
	hr, _, loosen := looseningReloader(t)
	hr.SetElevationApprover(&recordingApprover{err: errors.New("operator declined")})
	loosen()

	outcome := hr.Reload(context.Background())
	assertElevationRequired(t, outcome, "conductor.external_routing_enabled")
	if extractEffectiveConfig(hr.Current()).Conductor.ExternalRoutingEnabled {
		t.Fatal("a refused loosening was applied anyway")
	}
}

// TestConfigLoosenNoHelperEnrolled proves the absent case refuses: a
// machine with no helper enrolled cannot loosen a guarded family.
func TestConfigLoosenNoHelperEnrolled(t *testing.T) {
	hr, _, loosen := looseningReloader(t)
	loosen()

	outcome := hr.Reload(context.Background())
	assertElevationRequired(t, outcome, "conductor.external_routing_enabled")
	if !strings.Contains(outcome.Err.Error(), "no elevation helper is enrolled") {
		t.Fatalf("err = %v, want the no-helper reason", outcome.Err)
	}
}

// TestConfigLoosenEmptyApprovalRefused proves an approver that approves
// without producing a reference is treated as a refusal: an approval
// nothing can be recorded against is not an approval.
func TestConfigLoosenEmptyApprovalRefused(t *testing.T) {
	hr, _, loosen := looseningReloader(t)
	hr.SetElevationApprover(&recordingApprover{ref: ""})
	loosen()

	assertElevationRequired(t, hr.Reload(context.Background()), "conductor.external_routing_enabled")
}

// TestConfigTighteningBypasses proves a tightening write never reaches
// the elevation flow.
func TestConfigTighteningBypasses(t *testing.T) {
	hr, path, _, _, _ := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = true\n")
	approver := &recordingApprover{ref: "unused"}
	hr.SetElevationApprover(approver)
	_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = false\n")

	outcome := hr.Reload(context.Background())
	if !outcome.Accepted {
		t.Fatalf("outcome = %+v, want the tightening accepted", outcome)
	}
	if approver.calls != 0 {
		t.Fatalf("approver calls = %d, want a tightening write to skip elevation entirely", approver.calls)
	}
	if outcome.ElevationRef != "" {
		t.Fatalf("ElevationRef = %q, want empty for a tightening write", outcome.ElevationRef)
	}
}

// TestRequiresElevationToLoosen asserts the guarded-family list from the
// spec text: policy, secrets, sync, nodes, conductor and elevation need
// elevation to loosen; an ordinary section does not; and an unparseable
// key is not exempted.
func TestRequiresElevationToLoosen(t *testing.T) {
	guarded := []string{
		"policy.autonomy_profile", "secrets.keychain_backend", "sync.class",
		"nodes.trust_tier", "conductor.spill_enabled", "elevation.allow_remote",
	}
	for _, key := range guarded {
		if !RequiresElevationToLoosen(key) {
			t.Fatalf("%q: want elevation required", key)
		}
	}
	for _, key := range []string{"logging.level", "storage.driver", "telemetry.enabled"} {
		if RequiresElevationToLoosen(key) {
			t.Fatalf("%q: want no elevation requirement", key)
		}
	}
	for _, key := range []string{"", "policy..autonomy_profile", "not a key"} {
		if !RequiresElevationToLoosen(key) {
			t.Fatalf("%q: an unparseable key must not be waved through", key)
		}
	}
}

// acceptedFields returns the payload of the config.reload.accepted event.
func acceptedFields(t *testing.T, events *fakeEventPublisher) map[string]interface{} {
	t.Helper()
	events.mu.Lock()
	defer events.mu.Unlock()
	for _, e := range events.events {
		if e.name == eventReloadAccepted {
			return e.payload
		}
	}
	t.Fatal("no config.reload.accepted event was published")
	return nil
}

// assertElevationRequired checks one refused outcome names key.
func assertElevationRequired(t *testing.T, outcome ReloadOutcome, key string) {
	t.Helper()
	if !outcome.Rejected || !outcome.ElevationRequired {
		t.Fatalf("outcome = %+v, want a rejected, elevation-required reload", outcome)
	}
	var required *ElevationRequiredError
	if !errors.As(outcome.Err, &required) {
		t.Fatalf("err = %v, want an *ElevationRequiredError", outcome.Err)
	}
	found := false
	for _, k := range required.Keys {
		if k == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusal names %v, want it to name %q", required.Keys, key)
	}
}

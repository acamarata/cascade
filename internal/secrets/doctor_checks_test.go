// Purpose: tests for the five secrets doctor checks. Every one is driven
//
//	against a real file-vault custody, a real quarantine ledger and a
//	real detector under t.TempDir(); none of them is exercised through a
//	stand-in.
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (tests).

package secrets

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// doctorTestNow is the fixed instant every expiry comparison here uses.
func doctorTestNow() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) }

// newDoctorDeps builds DoctorCheckDeps over real components in t.TempDir().
func newDoctorDeps(t *testing.T) DoctorCheckDeps {
	t.Helper()
	broker := newUnelevatedBroker(t)
	quarantine, err := NewQuarantineStore(t.TempDir(), fixedClock{at: doctorTestNow()})
	if err != nil {
		t.Fatalf("NewQuarantineStore: %v", err)
	}
	detector, err := NewDetector(DefaultRegistry(), DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	return DoctorCheckDeps{
		Broker:     broker,
		Quarantine: quarantine,
		Detector:   detector,
		Now:        doctorTestNow,
	}
}

// checkNamed returns the built check with name, failing if it is absent.
func checkNamed(t *testing.T, deps DoctorCheckDeps, name string) doctor.Check {
	t.Helper()
	checks, err := NewDoctorChecks(deps)
	if err != nil {
		t.Fatalf("NewDoctorChecks: %v", err)
	}
	for _, c := range checks {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("no check named %s among the built set", name)
	return nil
}

func TestDoctorChecksRefuseIncompleteDeps(t *testing.T) {
	full := newDoctorDeps(t)
	for _, tc := range []struct {
		name   string
		mutate func(DoctorCheckDeps) DoctorCheckDeps
	}{
		{"no broker", func(d DoctorCheckDeps) DoctorCheckDeps { d.Broker = nil; return d }},
		{"no quarantine", func(d DoctorCheckDeps) DoctorCheckDeps { d.Quarantine = nil; return d }},
		{"no detector", func(d DoctorCheckDeps) DoctorCheckDeps { d.Detector = nil; return d }},
		{"no clock", func(d DoctorCheckDeps) DoctorCheckDeps { d.Now = nil; return d }},
	} {
		if _, err := NewDoctorChecks(tc.mutate(full)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s built a check set anyway: %v", tc.name, err)
		}
	}
}

func TestDoctorChecksAreTheFiveNamedOnes(t *testing.T) {
	checks, err := NewDoctorChecks(newDoctorDeps(t))
	if err != nil {
		t.Fatalf("NewDoctorChecks: %v", err)
	}
	got := map[string]bool{}
	for _, c := range checks {
		got[c.Name()] = true
		if c.Describe() == "" {
			t.Fatalf("%s has no description", c.Name())
		}
	}
	for _, want := range []string{
		"secrets/keychain-reachable", "secrets/keys-resolvable", "secrets/oauth-not-expired",
		"secrets/patterns-loaded", "secrets/quarantine-depth",
	} {
		if !got[want] {
			t.Fatalf("%s is missing from the built set: %v", want, got)
		}
	}
}

func TestKeychainReachableProbesTheBackend(t *testing.T) {
	deps := newDoctorDeps(t)
	check := checkNamed(t, deps, "secrets/keychain-reachable")
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run = %+v, want ok on a reachable backend", res)
	}
	if _, ferr := check.Fix(context.Background()); ferr != doctor.ErrCheckNotFixable {
		t.Fatalf("Fix = %v, want ErrCheckNotFixable", ferr)
	}
	if !check.Metadata().FirstRun {
		t.Fatal("keychain-reachable must be a first-run check")
	}
}

func TestKeysResolvableReportsMissingKeys(t *testing.T) {
	deps := newDoctorDeps(t)
	deps.ConfigKeys = []string{"PRESENT_KEY", "MISSING_KEY"}
	if _, err := deps.Broker.Set(context.Background(), "PRESENT_KEY", []byte("value-bytes"), SetUpdate); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	res, err := checkNamed(t, deps, "secrets/keys-resolvable").Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Run = %+v, want an error for an unresolved key", res)
	}
	if !strings.Contains(res.Detail, "MISSING_KEY") || strings.Contains(res.Detail, "value-bytes") {
		t.Fatalf("detail = %q; it must name the key and never the value", res.Detail)
	}
}

func TestKeysResolvableGreenPaths(t *testing.T) {
	deps := newDoctorDeps(t)
	res, err := checkNamed(t, deps, "secrets/keys-resolvable").Run(context.Background())
	if err != nil || res.Status != doctor.StatusOK {
		t.Fatalf("with no configured keys: (%+v, %v)", res, err)
	}
	deps.ConfigKeys = []string{"PRESENT_KEY"}
	if _, serr := deps.Broker.Set(context.Background(), "PRESENT_KEY", []byte("value-bytes"), SetUpdate); serr != nil {
		t.Fatalf("seeding: %v", serr)
	}
	res, err = checkNamed(t, deps, "secrets/keys-resolvable").Run(context.Background())
	if err != nil || res.Status != doctor.StatusOK {
		t.Fatalf("with every key present: (%+v, %v)", res, err)
	}
	if _, ferr := checkNamed(t, deps, "secrets/keys-resolvable").Fix(context.Background()); ferr != doctor.ErrCheckNotFixable {
		t.Fatalf("Fix = %v", ferr)
	}
}

func TestPatternsLoadedCountsTheRealRegistry(t *testing.T) {
	deps := newDoctorDeps(t)
	res, err := checkNamed(t, deps, "secrets/patterns-loaded").Run(context.Background())
	if err != nil || res.Status != doctor.StatusOK {
		t.Fatalf("Run = (%+v, %v)", res, err)
	}
	if _, ferr := checkNamed(t, deps, "secrets/patterns-loaded").Fix(context.Background()); ferr != doctor.ErrCheckNotFixable {
		t.Fatalf("Fix = %v", ferr)
	}
}

func TestPatternsLoadedFailsOnAnEmptyLibrary(t *testing.T) {
	deps := newDoctorDeps(t)
	empty, err := NewDetector(Registry{}, DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector over an empty registry: %v", err)
	}
	deps.Detector = empty
	res, rerr := checkNamed(t, deps, "secrets/patterns-loaded").Run(context.Background())
	if rerr != nil {
		t.Fatalf("Run: %v", rerr)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Run = %+v, want an error: a detector with no patterns reports every payload clean", res)
	}
}

func TestQuarantineDepthAndFlush(t *testing.T) {
	deps := newDoctorDeps(t)
	deps.QuarantineThreshold = 1
	check := checkNamed(t, deps, "secrets/quarantine-depth")
	// An empty queue: ok, and a flush that changed nothing.
	res, err := check.Run(context.Background())
	if err != nil || res.Status != doctor.StatusOK {
		t.Fatalf("empty queue = (%+v, %v)", res, err)
	}
	fix, err := check.Fix(context.Background())
	if err != nil || fix.Applied || fix.Delta != "" {
		t.Fatalf("flushing an empty queue = (%+v, %v), want no change", fix, err)
	}
	// Two entries against a threshold of one: a warning.
	for offset, name := range []string{"FIRST_FINDING", "SECOND_FINDING"} {
		if _, aerr := deps.Quarantine.Put(DetectionHit{
			Class: ClassAPIKey, Pattern: "test", Offset: offset, Len: 8,
			Confidence: 0.9, SuggestedName: name,
		}, "doctor-checks-test", nil); aerr != nil {
			t.Fatalf("seeding the queue: %v", aerr)
		}
	}
	res, err = check.Run(context.Background())
	if err != nil || res.Status != doctor.StatusWarn {
		t.Fatalf("queue above threshold = (%+v, %v)", res, err)
	}
	fix, err = check.Fix(context.Background())
	if err != nil || !fix.Applied || !strings.Contains(fix.Delta, "2") {
		t.Fatalf("flushing two entries = (%+v, %v)", fix, err)
	}
	depth, err := deps.Quarantine.PendingCount()
	if err != nil || depth != 0 {
		t.Fatalf("after the flush the queue holds %d (%v)", depth, err)
	}
	if !check.Metadata().Fixable {
		t.Fatal("quarantine-depth must declare itself fixable")
	}
}

func TestVaultRefsInReadsOnlyWellFormedTags(t *testing.T) {
	text := "token=<token>CI_TOKEN</token> and <apikey>SERVICE_KEY</apikey> and <token>broken"
	got := VaultRefsIn(text)
	if len(got) != 2 || got[0] != "CI_TOKEN" || got[1] != "SERVICE_KEY" {
		t.Fatalf("VaultRefsIn = %v, want the two well-formed references, sorted", got)
	}
	if len(VaultRefsIn("no tags at all")) != 0 {
		t.Fatal("VaultRefsIn invented a reference from untagged text")
	}
}

// TestDoctorCheckMetadataIsDeclared asserts each check's static tags
// against the contract, not against a second copy of the implementation:
// only quarantine-depth is fixable, and the three first-run checks are
// the ones an installation health check needs.
func TestDoctorCheckMetadataIsDeclared(t *testing.T) {
	wantFirstRun := map[string]bool{
		"secrets/keychain-reachable": true,
		"secrets/keys-resolvable":    true,
		"secrets/patterns-loaded":    true,
		"secrets/oauth-not-expired":  false,
		"secrets/quarantine-depth":   false,
	}
	wantFixable := map[string]bool{"secrets/quarantine-depth": true}
	checks, err := NewDoctorChecks(newDoctorDeps(t))
	if err != nil {
		t.Fatalf("NewDoctorChecks: %v", err)
	}
	for _, c := range checks {
		meta := c.Metadata()
		if meta.FirstRun != wantFirstRun[c.Name()] {
			t.Fatalf("%s FirstRun = %v, want %v", c.Name(), meta.FirstRun, wantFirstRun[c.Name()])
		}
		if meta.Fixable != wantFixable[c.Name()] {
			t.Fatalf("%s Fixable = %v, want %v", c.Name(), meta.Fixable, wantFixable[c.Name()])
		}
	}
}

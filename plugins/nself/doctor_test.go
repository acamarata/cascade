// Purpose: the doctor probe's two outcomes, and the egress seam's
//
//	fail-closed default. TestNselfPlugin_WindowsRefusal is the contract's
//	named platform test: it injects BOTH the platform and the PATH lookup,
//	so it proves the refusal on every machine it runs on rather than on a
//	machine that happens not to have nself installed (the draft's version
//	never involved GOOS and read this machine's real PATH).
//
// SPORT: plugins/nself doctor (TEST) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fixedLocator is a hermetic binaryLocator.
type fixedLocator struct {
	path string
	err  error
}

func (l fixedLocator) Lookup(string) (string, error) { return l.path, l.err }

// TestNselfPlugin_WindowsRefusal is the contract's named test: on a
// platform where the binary is absent, the doctor probe returns a TYPED
// error with a remediation hint and never panics.
func TestNselfPlugin_WindowsRefusal(t *testing.T) {
	locator := execLocator{goos: "windows", lookPath: absentLookPath}
	res, err := runDoctor(locator, nselfBinary)
	if err == nil {
		t.Fatal("runDoctor on windows without the binary = nil error, want a typed refusal")
	}
	var derr *doctorError
	if !errors.As(err, &derr) {
		t.Fatalf("runDoctor err = %v (%T), want *doctorError", err, err)
	}
	if derr.GOOS != "windows" {
		t.Fatalf("doctorError.GOOS = %q, want the injected platform \"windows\"", derr.GOOS)
	}
	if derr.Remediation == "" {
		t.Fatal("doctorError.Remediation is empty; the contract requires a remediation hint")
	}
	if got := derr.Error(); !strings.Contains(got, "windows") || !strings.Contains(got, nselfBinary) {
		t.Fatalf("doctorError.Error() = %q, want it to name the platform and the binary", got)
	}
	if res.BinaryReachable || res.BinaryPath != "" {
		t.Fatalf("doctorResult = %+v, want the zero value on a refusal", res)
	}
}

func TestRunDoctor_ReachableBinaryReportsItsPath(t *testing.T) {
	res, err := runDoctor(fixedLocator{path: "/opt/bin/nself"}, nselfBinary)
	if err != nil {
		t.Fatalf("runDoctor err = %v, want nil", err)
	}
	if !res.BinaryReachable || res.BinaryPath != "/opt/bin/nself" {
		t.Fatalf("doctorResult = %+v, want the reachable path reported", res)
	}
}

// TestRunDoctor_NilLocatorFallsBackToTheRealOne asserts the production
// default consistently, whichever way this machine answers: either the
// binary resolves and the result says so, or it does not and the refusal
// names THIS platform.
func TestRunDoctor_NilLocatorFallsBackToTheRealOne(t *testing.T) {
	res, err := runDoctor(nil, nselfBinary)
	if err == nil {
		if !res.BinaryReachable || res.BinaryPath == "" {
			t.Fatalf("runDoctor(nil) = (%+v, nil), want a reachable result with a path", res)
		}
		return
	}
	var derr *doctorError
	if !errors.As(err, &derr) {
		t.Fatalf("runDoctor(nil) err = %v, want *doctorError", err)
	}
	if derr.GOOS != goruntime.GOOS {
		t.Fatalf("doctorError.GOOS = %q, want this machine's %q", derr.GOOS, goruntime.GOOS)
	}
}

func TestDoctorError_WithoutRemediationStillNamesThePlatform(t *testing.T) {
	got := (&doctorError{Reason: "something", GOOS: "linux"}).Error()
	if !strings.Contains(got, "linux") || !strings.Contains(got, "something") {
		t.Fatalf("doctorError.Error() = %q, want the platform and the reason", got)
	}
}

func TestExecLocator_HostGOOSDefaultsToTheRealPlatform(t *testing.T) {
	if got := (execLocator{}).hostGOOS(); got != goruntime.GOOS {
		t.Fatalf("execLocator{}.hostGOOS() = %q, want %q", got, goruntime.GOOS)
	}
}

// TestEgressInterceptor_DefaultRefusesToEmit is the fail-closed proof. The
// draft's default was a PASS-THROUGH, which meant the registered class
// config (enabled, restricted tier refused) guarded nothing while the
// plugin's doc claimed the traffic "transits the substitution and
// sensitivity pass".
func TestEgressInterceptor_DefaultRefusesToEmit(t *testing.T) {
	orig := egressInterceptor
	egressInterceptor = refusingInterceptor{}
	t.Cleanup(func() { egressInterceptor = orig })

	out, err := marshalThroughEgress(context.Background(), projectInfoResponse{Method: "none"})
	if err == nil {
		t.Fatal("marshalThroughEgress with no interceptor bound = nil error, want a refusal")
	}
	if out != nil {
		t.Fatalf("marshalThroughEgress returned %q on a refusal, want nothing", out)
	}
	if !errors.Is(err, ErrNoEgressInterceptor) {
		t.Fatalf("err = %v, want it to wrap ErrNoEgressInterceptor (identity, not just a Kind)", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("cascade.KindOf(err) = (%v, %v), want (KindUnavailable, true)", kind, ok)
	}
}

func TestSetEgressInterceptor_RefusesNilAndInstallsReal(t *testing.T) {
	orig := egressInterceptor
	t.Cleanup(func() { egressInterceptor = orig })

	if err := SetEgressInterceptor(nil); err == nil {
		t.Fatal("SetEgressInterceptor(nil) = nil error, want a refusal")
	}
	if kind, ok := cascade.KindOf(SetEgressInterceptor(nil)); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("SetEgressInterceptor(nil) kind = (%v, %v), want (KindInvalidInput, true)", kind, ok)
	}
	spy := &spyInterceptor{}
	if err := SetEgressInterceptor(spy); err != nil {
		t.Fatalf("SetEgressInterceptor(spy) = %v, want nil", err)
	}
	if _, err := marshalThroughEgress(context.Background(), projectInfoResponse{}); err != nil {
		t.Fatalf("marshalThroughEgress after installing an interceptor = %v, want nil", err)
	}
	if spy.calls != 1 || spy.class != EgressClassNselfBackend || spy.tier != TierInternal {
		t.Fatalf("spy = %+v, want one call on the nself-backend class at the internal tier", spy)
	}
}

// Purpose (this file): the doctor probe (binary reachability, with a typed
//
//	remediation-bearing failure) and the egress seam every tool response
//	this plugin emits must transit before it leaves DispatchTool.
//
// Inputs: a binaryLocator (real exec.LookPath by default, injected in
//
//	tests so no test reads this machine's PATH) and an EgressInterceptor
//	the host's composition root binds (internal/plugins/nself_wiring.go).
//
// Outputs: a doctorResult, or a typed *doctorError carrying a remediation
//
//	hint — never a panic, on any platform (TestNselfPlugin_WindowsRefusal).
//
// Constraints: same internal/** import ban as the rest of this package
//
//	(Art.10.2). EgressClass/SensitivityTier/EgressInterceptor below are
//	this package's OWN local view of internal/hooks/egress's types, not
//	that package's types — the identical pattern
//	internal/plugins/process/types.go documents for the same depguard
//	reason, string-identical so the composition-root adapter is a plain
//	cast rather than a lookup table.
//
//	THERE IS NO PASS-THROUGH DEFAULT. An unbound interceptor makes every
//	tool response REFUSE to emit. A pass-through would mean the class's
//	registered InterceptConfig (enabled, restricted-tier refused) guarded
//	nothing while the plugin's own doc claimed it did — the exact
//	"declared but not wired" shape the adversarial review caught in the
//	draft this replaces.
//
// SPORT: plugins/nself doctor (ADD) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"errors"
	"os/exec"
	goruntime "runtime"

	"github.com/acamarata/cascade/pkg/cascade"
)

// EgressClass is this package's local view of an egress-inventory class
// identifier (internal/hooks/egress/classes.go's EgressClassNselfBackend
// is the real inventory entry this mirrors by string value).
type EgressClass string

// EgressClassNselfBackend names the class covering this plugin's own
// outbound tool responses. It matches
// internal/hooks/egress.EgressClassNselfBackend's string value exactly,
// and internal/plugins/nself_wiring.go asserts that equality at wiring
// time rather than trusting this comment.
const EgressClassNselfBackend EgressClass = "nself-backend"

// SensitivityTier mirrors internal/hooks/egress.SensitivityTier's string
// constants.
type SensitivityTier string

// TierInternal is the tier this plugin's responses declare: the content
// stays on the operator's own machine, crossing only the daemon-to-MCP
// client boundary.
const TierInternal SensitivityTier = "internal"

// EgressInterceptor is the firewall seam this plugin's tool-response path
// transits. internal/plugins/nself_wiring.go binds the REAL
// egress.Engine.InterceptClass to it at process boot.
type EgressInterceptor interface {
	InterceptClass(ctx context.Context, class EgressClass, tier SensitivityTier, content []byte) ([]byte, error)
}

// ErrNoEgressInterceptor is the refusal identity an unbound interceptor
// produces. Exported so the host bridge (and its test) can assert THIS
// condition rather than matching on message text — cascade.Error's own
// errors.Is compares Kind only, so a Kind check alone would match any
// other KindUnavailable failure on this path.
var ErrNoEgressInterceptor = errors.New(
	"cascade-nself: no egress interceptor is bound; the host composition root must call SetEgressInterceptor")

// refusingInterceptor is EgressInterceptor's default: it emits nothing.
type refusingInterceptor struct{}

func (refusingInterceptor) InterceptClass(context.Context, EgressClass, SensitivityTier, []byte) ([]byte, error) {
	return nil, cascade.Wrap(cascade.KindUnavailable, ErrNoEgressInterceptor,
		"cascade-nself: refusing to emit a tool response")
}

// egressInterceptor is the active interceptor. Assigned once at process
// boot by the host bridge, exactly as plugins/review's reviewProvider is.
var egressInterceptor EgressInterceptor = refusingInterceptor{}

// SetEgressInterceptor installs i as the active interceptor. A nil i is
// refused rather than silently leaving the plugin unable to answer without
// saying why.
func SetEgressInterceptor(i EgressInterceptor) error {
	if i == nil {
		return cascade.New(cascade.KindInvalidInput,
			"cascade-nself: SetEgressInterceptor: interceptor must not be nil")
	}
	egressInterceptor = i
	return nil
}

// binaryLocator abstracts resolving the `nself` binary, so
// TestNselfPlugin_WindowsRefusal proves the absent-binary path without
// depending on GOOS or on what this machine happens to have on PATH.
type binaryLocator interface {
	Lookup(binary string) (path string, err error)
}

// execLocator is the real, production binaryLocator. Both fields are
// injectable and both default to the real host values, so production
// behaviour is the zero value and a test is hermetic by construction.
type execLocator struct {
	goos     string
	lookPath func(string) (string, error)
}

// Lookup implements binaryLocator, reporting an absent binary as this
// package's typed outcome rather than exec's own.
func (l execLocator) Lookup(binary string) (string, error) {
	look := l.lookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(binary)
	if err != nil {
		return "", &binaryAbsentError{Binary: binary, GOOS: l.hostGOOS()}
	}
	return path, nil
}

func (l execLocator) hostGOOS() string {
	if l.goos != "" {
		return l.goos
	}
	return goruntime.GOOS
}

// activeLocator is the locator every dispatch resolves `nself` through.
// Same-package test files assign it directly; no host bridge configures
// it, so there is no exported setter.
var activeLocator binaryLocator = execLocator{}

// doctorResult is one doctor-probe outcome.
type doctorResult struct {
	BinaryReachable bool
	BinaryPath      string
}

// doctorError is a typed doctor-probe failure with a remediation hint —
// the contract's "typed error with remediation hint on failure".
type doctorError struct {
	Reason      string
	Remediation string
	// GOOS names the platform the probe ran on, so a Windows operator
	// reads a platform-specific answer rather than a generic one.
	GOOS string
}

func (e *doctorError) Error() string {
	msg := "cascade-nself doctor (" + e.GOOS + "): " + e.Reason
	if e.Remediation == "" {
		return msg
	}
	return msg + " (" + e.Remediation + ")"
}

// runDoctor checks nself binary reachability. It never panics: an absent
// binary — the steady state on a platform nself does not ship for —
// returns a typed *doctorError.
//
// It deliberately checks NOTHING ELSE. The draft this replaces also
// "validated server-profile URL health"; at this ticket's honest floor no
// handshake runs, so there are no URLs to validate and no credential-
// bearing value anywhere in this plugin to leak through a health report.
func runDoctor(locator binaryLocator, binary string) (doctorResult, error) {
	if locator == nil {
		locator = execLocator{}
	}
	path, err := locator.Lookup(binary)
	if err != nil {
		var absent *binaryAbsentError
		platform := goruntime.GOOS
		if errors.As(err, &absent) {
			platform = absent.GOOS
		}
		return doctorResult{}, &doctorError{
			Reason:      "the " + binary + " binary is not on PATH",
			Remediation: "install the " + binary + " CLI and ensure it is on PATH",
			GOOS:        platform,
		}
	}
	return doctorResult{BinaryReachable: true, BinaryPath: path}, nil
}

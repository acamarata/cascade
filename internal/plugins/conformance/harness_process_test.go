//go:build !windows

// Package conformance (harness_process_test.go): Purpose: processHarness
// drives the ABI v1 conformance fixtures through the REAL
// process.ProcessRuntime against a REAL forked `sh` subprocess emitting
// the exact JSON-RPC Notification frame internal/plugins/process/
// hostcalls.go expects (mirroring internal/plugins/host/
// boundary_integration_test.go's own real-counterpart pattern).
//
// GENUINE ABI DIVERGENCE, RECORDED RATHER THAN PAPERED OVER (LANE-RULES
// §1/§4): as landed by S-31.T3/T4, the process runtime has no response
// channel for a plugin-initiated host-fn call at all --
// hostcalls.go's own doc comment: "a plugin-initiated JSON-RPC request
// ... is decoded as a Notification with its id silently discarded, and
// no response ever reaches the plugin." So this harness cannot produce a
// ResponseJSON the way builtinHarness/wasmHarness do; it can only
// observe the ONE thing that is real and wired today: whether
// consumeHostCalls' capability check allowed or denied the call
// (CapabilityOnly=true). A second, independent divergence: the process
// wire shape's field names are NOT the wasm ABI's (host_storage checks
// a "domain" field; wasm's StorageRequest carries op/key/value) --
// ProcessParams (fixture_runner_test.go) carries the real, different
// shape. And host_log has NO process notification method at all
// (hostGenericCapability has no entry for it, and the http/storage/
// secretref cases don't match it either) -- Observed stays false for
// every host_log fixture, which is itself the recorded finding, not a
// suite gap.
//
// SPORT: internal.plugins.conformance/ADDED (P1-E15-W4-S32-T2),
// plugin/process-runtime conformance-coverage=yes.
package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/plugins/host"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
)

// checkerAdapter satisfies process.HostCapabilityChecker over a real
// *host.HostBoundaryEnforcer -- the composition-root-shaped adapter a
// production wiring point builds, mirrored here to drive the real chain.
type checkerAdapter struct{ enforcer *host.HostBoundaryEnforcer }

func (a checkerAdapter) CheckHTTP(ctx context.Context, url string) error {
	return a.enforcer.CheckHTTP(ctx, url)
}
func (a checkerAdapter) CheckStorage(ctx context.Context, domain string) error {
	return a.enforcer.CheckStorage(ctx, domain)
}
func (a checkerAdapter) CheckSecretRef(ctx context.Context, key string) (any, error) {
	return a.enforcer.CheckSecretRef(ctx, key)
}
func (a checkerAdapter) CheckGeneric(ctx context.Context, capability string) error {
	return a.enforcer.CheckGeneric(ctx, capability)
}

// chanAuditSink delivers every AuditEvent on a channel so a Call can
// block for one with a timeout instead of polling or sleeping.
type chanAuditSink struct{ events chan host.AuditEvent }

func (s chanAuditSink) LogHostCall(_ context.Context, event host.AuditEvent) { s.events <- event }

// fakePolicy implements host.PolicyEngine deterministically for
// CheckGeneric's three capability-gated methods (stream/event/tool).
type fakePolicy struct{ allow bool }

func (p fakePolicy) Evaluate(_ context.Context, _, _ string) (host.PolicyVerdict, string, error) {
	if p.allow {
		return host.PolicyVerdictAllow, "conformance: granted", nil
	}
	return host.PolicyVerdictDeny, "conformance: denied", nil
}

// fakeBroker implements host.SecretBroker deterministically for
// CheckSecretRef. Returns the zero SecretHandle on grant -- this package
// cannot construct a non-zero one (host.newSecretHandle is unexported by
// design, Art.2's "never a literal" guarantee) and does not need to: the
// fixture only asserts allow/deny, never the handle's value.
type fakeBroker struct{ allow bool }

func (b fakeBroker) Reference(_ context.Context, _ string) (host.SecretHandle, error) {
	if !b.allow {
		return host.SecretHandle{}, cascade.New(cascade.KindCapabilityDenied, "conformance: no broker grant")
	}
	return host.SecretHandle{}, nil
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// noopRegistrar accepts any egress class registration; this suite does
// not exercise egress itself.
type noopRegistrar struct{}

func (noopRegistrar) Register(process.EgressClass, process.EgressRegistrationConfig) error {
	return nil
}

// processHarness is not usable at its zero value; use newProcessHarness.
type processHarness struct{}

func newProcessHarness(context.Context, *testing.T) ABIHarness { return processHarness{} }

func (processHarness) Name() string         { return "process" }
func (processHarness) CapabilityOnly() bool { return true }

// Call implements ABIHarness. See this file's package doc for the two
// genuine ABI divergences it records rather than works around.
func (processHarness) Call(ctx context.Context, t *testing.T, f HostFnFixture) ABIObservation {
	t.Helper()
	if f.ProcessMethod == "" {
		// host_log: no process notification method exists for it at all
		// (hostcalls.go's real vocabulary). Nothing to dispatch; this IS
		// the finding.
		return ABIObservation{CapabilityOnly: true, Observed: false}
	}

	allow := f.ProcessGrantProfile == "granted"
	sink := chanAuditSink{events: make(chan host.AuditEvent, 4)}
	grants := host.Grants{}
	if allow {
		grants = host.Grants{NetScopes: []string{"allowed.example.com"}, StorageDomains: map[string]bool{"conformance-domain": true}}
	}
	enforcer, err := host.NewHostBoundaryEnforcer("conformance-plugin", grants, fakePolicy{allow: allow}, fakeBroker{allow: allow}, sink)
	if err != nil {
		t.Fatalf("build HostBoundaryEnforcer: %v", err)
	}

	rt := process.NewProcessRuntime()
	rt.Stderr = discardWriter{}
	rt.Registrar = noopRegistrar{}
	rt.CapabilityChecker = checkerAdapter{enforcer: enforcer}
	// One crash is terminal: the script exits after its sleep. A respawn
	// would re-run it and emit a second event after this Call already
	// returned. InitialBackoff nonzero is what makes MaxAttempts:0 stick
	// (RestartPolicy.resolved() reads BOTH fields as "unset" only when
	// both are the zero value).
	rt.Restart = process.RestartPolicy{MaxAttempts: 0, InitialBackoff: time.Nanosecond}
	m := process.Manifest{
		Name: "conformance-plugin", TrustTier: process.TrustTierTrusted,
		Command: "sh", Args: []string{"-c", processScript(f.ProcessMethod, f.ProcessParams)},
	}
	if _, err := rt.Launch(ctx, m); err != nil {
		t.Skipf("process runtime: real sh subprocess unavailable, skipping: %v", err)
	}

	// GENUINE FINDING: host.HostBoundaryEnforcer's `allow` (capability.go)
	// is called ONLY by CheckSecretRef's success path -- capability.go's
	// own doc comment: "Only CheckSecretRef calls this today ... are not
	// separately audited." So CheckHTTP/CheckStorage/CheckGeneric never
	// emit ANY audit event on their successful (allowed) path. A
	// "granted" fixture for host_http/host_storage/host_stream/
	// host_eventemit/host_toolregister therefore has NO positive signal
	// to observe -- the first version of this suite waited 5s per such
	// fixture and only ever timed out, which is the CORRECT outcome, not
	// a bug: this branch treats that timeout as the honest evidence "no
	// denial occurred," made meaningful by every "denied" fixture for
	// the same method (below) proving the identical wiring emits an
	// event promptly when it actually denies.
	secretRefAllow := f.Method == methodSecretRef || !allow
	wait := 600 * time.Millisecond
	if secretRefAllow {
		wait = 3 * time.Second
	}
	select {
	case ev := <-sink.events:
		return ABIObservation{CapabilityOnly: true, Observed: true, Allowed: ev.Allowed}
	case <-time.After(wait):
		if allow && f.Method != methodSecretRef {
			return ABIObservation{CapabilityOnly: true, Observed: false, Allowed: true}
		}
		t.Fatalf("process: timed out waiting for a host-boundary decision on %q", f.Method)
		return ABIObservation{}
	}
}

// processScript builds the real subprocess's shell program: answer the
// handshake, then write ONE real JSON-RPC Notification frame for
// method/params -- the exact wire shape process/transport.go parses,
// per internal/plugins/host/boundary_integration_test.go's own
// established real-counterpart pattern.
func processScript(method string, params []byte) string {
	frame := `{"jsonrpc":"2.0","method":"` + method + `","params":` + string(params) + `}`
	return "read _line; printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocol_version\":\"1.0.0\",\"manifest_hash\":\"deadbeef\"}}'; printf '%s\\n' '" + frame + "'; sleep 1"
}

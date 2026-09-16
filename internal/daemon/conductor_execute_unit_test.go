package daemon

// Purpose: unit coverage for RegisterConductorExecuteHandler and its two
//   handler branches, driven through a real *rpc.Registry.Dispatch (the
//   fleet.rpc_test.go precedent), not by calling either closure directly.
//   The real-socket integration test in conductor_execute_integration_test.go
//   stays the R-16.80 Ruling 3(a)/(b) proof; this file adds the untagged
//   unit lane the coverage gate actually measures.
// SPORT: internal/daemon (ADD, coverage-floor fix).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeProviderResolver satisfies conductor.ProviderResolver so
// conductor.NewExecutor's Resolver!=nil requirement is met. Resolve is
// never reached by the tests below: Pipeline.Ready() fails closed on the
// missing Classifier/Taxonomy/Policy/Sensitivity/Firewall collaborators
// before execute.go ever calls the capability that would invoke it.
type fakeProviderResolver struct{}

func (fakeProviderResolver) Resolve(context.Context, provider.Selection) (provider.ModelProvider, error) {
	return nil, cascade.New(cascade.KindUnavailable, "fakeProviderResolver: not reachable in this test")
}

func newTestAuditWriter(t *testing.T) audit.Writer {
	t.Helper()
	return audit.New(storetest.NewMemStore(), runtime.NewSystemClock(), nil)
}

// TestRegisterConductorExecuteHandler_NilResolver_RealRefusal proves the
// unavailable branch: with no ProviderResolver (today's real production
// posture per the ticket journal), Dispatch answers with the real
// ErrConstructionFailed reason, wrapped as KindUnavailable, and NEVER
// method-not-found. This is conductorExecuteUnavailableHandler's own
// behaviour, exercised through RegisterConductorExecuteHandler's real
// wiring rather than by calling the closure directly.
func TestRegisterConductorExecuteHandler_NilResolver_RealRefusal(t *testing.T) {
	registry := rpc.NewRegistry()
	manifest := NewManifest(nil, runtime.NewSystemClock())
	clock := runtime.NewSystemClock()
	auditWriter := newTestAuditWriter(t)

	if err := RegisterConductorExecuteHandler(registry, manifest, fakeRegistryReader{}, fakeQuotaSpiller{}, nil, auditWriter, clock, ConductorSecurity{}); err != nil {
		t.Fatalf("RegisterConductorExecuteHandler: unexpected error %v", err)
	}
	if !registry.Registered(ConductorExecuteMethod) {
		t.Fatal("conductor.execute never reached the registry")
	}

	params, err := json.Marshal(conductorExecuteParams{TaskID: "t1", TaskClass: "chat"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: ConductorExecuteMethod, Params: params})
	if errObj == nil {
		t.Fatalf("Dispatch() = %v, %v, want a real construction-failure error", result, errObj)
	}
	if !strings.Contains(errObj.Message, "conductor.execute: executor unavailable") {
		t.Errorf("error message = %q, want it to name the refusal reason", errObj.Message)
	}
	if !strings.Contains(errObj.Message, conductor.ErrConstructionFailed.Error()) {
		t.Errorf("error message = %q, want it to name the real ErrConstructionFailed cause", errObj.Message)
	}
}

// TestRegisterConductorExecuteHandler_RealExecutor_DecodesAndDispatches
// proves conductorExecuteHandler's own branch: with every NewExecutor
// mechanical requirement met (Router/Resolver/Audit/Clock non-nil),
// registration builds a real *conductor.Executor and the handler decodes
// the wire params and calls exec.Execute for real. execute.go's own
// security-pipeline gate then fails closed on the missing Classifier/
// Taxonomy/Policy/Sensitivity/Firewall collaborators (a real, distinct,
// typed error, never a fabricated success) - which is exactly the proof
// that decode-and-dispatch reached the real Executor rather than the
// unavailable branch above.
func TestRegisterConductorExecuteHandler_RealExecutor_DecodesAndDispatches(t *testing.T) {
	registry := rpc.NewRegistry()
	manifest := NewManifest(nil, runtime.NewSystemClock())
	clock := runtime.NewSystemClock()
	auditWriter := newTestAuditWriter(t)

	if err := RegisterConductorExecuteHandler(registry, manifest, fakeRegistryReader{}, fakeQuotaSpiller{}, fakeProviderResolver{}, auditWriter, clock, ConductorSecurity{}); err != nil {
		t.Fatalf("RegisterConductorExecuteHandler: unexpected error %v", err)
	}

	params, err := json.Marshal(conductorExecuteParams{
		TaskID:    "t1",
		TaskClass: "chat",
		Inputs:    []conductorExecuteMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: ConductorExecuteMethod, Params: params})
	if errObj == nil {
		t.Fatal("Dispatch() = nil error, want the real security-pipeline-not-ready refusal")
	}
	if strings.Contains(errObj.Message, "conductor.execute: executor unavailable") {
		t.Fatalf("error message = %q, still the unavailable branch, want the real executor's own refusal", errObj.Message)
	}
	if !strings.Contains(errObj.Message, conductor.ErrSecurityPipelineNotReady.Error()) {
		t.Errorf("error message = %q, want it to name ErrSecurityPipelineNotReady", errObj.Message)
	}
}

// TestRegisterConductorExecuteHandler_RealExecutor_BadParams proves
// conductorExecuteHandler's decode-failure branch: malformed JSON never
// reaches exec.Execute at all, and is reported as KindInvalidInput.
func TestRegisterConductorExecuteHandler_RealExecutor_BadParams(t *testing.T) {
	registry := rpc.NewRegistry()
	manifest := NewManifest(nil, runtime.NewSystemClock())
	clock := runtime.NewSystemClock()
	auditWriter := newTestAuditWriter(t)

	if err := RegisterConductorExecuteHandler(registry, manifest, fakeRegistryReader{}, fakeQuotaSpiller{}, fakeProviderResolver{}, auditWriter, clock, ConductorSecurity{}); err != nil {
		t.Fatalf("RegisterConductorExecuteHandler: unexpected error %v", err)
	}

	badParams := json.RawMessage(`{"sensitivity": 3}`) // sensitivity must be a string, not a number
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: ConductorExecuteMethod, Params: badParams})
	if errObj == nil {
		t.Fatal("Dispatch() = nil error, want a decode failure")
	}
	kind, ok := cascade.KindFromJSONRPCCode(errObj.Code)
	if !ok || kind != cascade.KindInvalidInput {
		t.Errorf("error kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

// fullSecurity is a ConductorSecurity with every collaborator present, so
// Pipeline.Ready() passes. It uses the package's REAL production
// collaborators wherever they are pure; only the firewall needs a
// constructed engine, which this helper builds over a temp-dir custody.
func fullSecurity(t *testing.T) ConductorSecurity {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Passphrase plus an always-failing runner forces the encrypted file
	// vault: on a host with an OS keychain SelectCustody prefers it, and a
	// unit test must never write into the operator's real credential store.
	custody, err := secrets.SelectCustody(secrets.Config{
		Service:    "cascade-conductor-security-test",
		Dir:        t.TempDir(),
		Passphrase: "conductor-security-test-pass",
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no platform keychain in this test")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatal(err)
	}
	firewall, err := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	if err != nil {
		t.Fatal(err)
	}
	classifier, err := conductor.NewContentClassifier(testScanner{})
	if err != nil {
		t.Fatal(err)
	}
	return ConductorSecurity{
		Classifier: classifier, Taxonomy: conductor.TaskClassRegistry{},
		Policy: conductor.NewOwnerPolicy(nil), Sensitivity: conductor.FailClosedSensitivity{},
		Firewall: firewall,
	}
}

// testScanner finds nothing, so the classifier admits every request.
type testScanner struct{}

func (testScanner) ScanCertainClasses(string) []string { return nil }

// TestRegisterConductorExecuteHandler_WithSecurity_PipelineIsReady is the
// W3 gate's own finding turned into a standing assertion: with every
// collaborator supplied, a dispatch must get PAST Ready() and fail at the
// provider boundary instead. Before P1-E10-W4-S87-T1 the composition root
// supplied none of them, so the shipped daemon refused every call with
// "security pipeline not ready" and no test in the tree noticed.
func TestRegisterConductorExecuteHandler_WithSecurity_PipelineIsReady(t *testing.T) {
	registry := rpc.NewRegistry()
	manifest := NewManifest(nil, runtime.NewSystemClock())
	clock := runtime.NewSystemClock()

	if err := RegisterConductorExecuteHandler(registry, manifest, fakeRegistryReader{}, fakeQuotaSpiller{},
		fakeProviderResolver{}, newTestAuditWriter(t), clock, fullSecurity(t)); err != nil {
		t.Fatalf("RegisterConductorExecuteHandler: %v", err)
	}

	params, err := json.Marshal(conductorExecuteParams{
		TaskID: "t1", TaskClass: "chat",
		Inputs: []conductorExecuteMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: ConductorExecuteMethod, Params: params})
	if errObj != nil && strings.Contains(errObj.Message, conductor.ErrSecurityPipelineNotReady.Error()) {
		t.Fatalf("the pipeline is still not ready with every collaborator supplied: %q", errObj.Message)
	}
}

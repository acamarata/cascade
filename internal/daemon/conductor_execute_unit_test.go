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
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
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

	if err := RegisterConductorExecuteHandler(registry, manifest, fakeRegistryReader{}, fakeQuotaSpiller{}, nil, auditWriter, clock); err != nil {
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

	if err := RegisterConductorExecuteHandler(registry, manifest, fakeRegistryReader{}, fakeQuotaSpiller{}, fakeProviderResolver{}, auditWriter, clock); err != nil {
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

	if err := RegisterConductorExecuteHandler(registry, manifest, fakeRegistryReader{}, fakeQuotaSpiller{}, fakeProviderResolver{}, auditWriter, clock); err != nil {
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

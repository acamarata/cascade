package retrieval

// Purpose: unit tests for RecallWhatService.Query — fan-out, fusion, the
// Domain-unavailable partial-result contract, the server-resolved-scope
// seam (recallwhat_scope.go), and the recall.what RPC boundary (Art.2, via
// the real internal/rpc Registry/Handler pipeline in-process, matching
// recall's own precedent but with no OS socket: Art.7.2 forbids
// "net"/"net/http" in an untagged file, and the pipeline exercised
// (rpc.Parse + Registry.Dispatch) is the same code either way). The
// egress substitution/exclusion tests use a REAL *egress.Engine
// (testEngine, mirroring internal/hooks/firewall_test.go's own pattern)
// over egress.DefaultRegistry() — the actual EgressClassRecallWhat entry
// this ticket registered — so the redaction/exclusion proof is against
// the real enforcer, never a fake standing in for the thing under test.
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Test doubles and builders (fakeFilesLeg, fakeConvLeg, fakeMemoryLeg,
// fakeScopeResolver, resolverFor, mapVault, testEgress, testClock,
// baselineLegs, newBaselineService, newDefaultService) live in
// recallwhat_testkit_test.go, shared by every _test.go file in this
// package.

func TestNewRecallWhatService_RequiresAtLeastOneLeg(t *testing.T) {
	if _, err := NewRecallWhatService(nil, nil, nil, rrf.Params{}, testClock(), resolverFor("p"), testEgress(t)); err == nil {
		t.Fatal("expected an error constructing a service with no legs")
	}
}

func TestNewRecallWhatService_RequiresScopeResolver(t *testing.T) {
	files, conv, mem := baselineLegs()
	if _, err := NewRecallWhatService(files, conv, mem, rrf.Params{}, testClock(), nil, testEgress(t)); err == nil {
		t.Fatal("expected an error constructing a service with no scope resolver")
	}
}

func TestNewRecallWhatService_RequiresEgressSubstitutor(t *testing.T) {
	files, conv, mem := baselineLegs()
	if _, err := NewRecallWhatService(files, conv, mem, rrf.Params{}, testClock(), resolverFor("p"), nil); err == nil {
		t.Fatal("expected an error constructing a service with no egress boundary")
	}
}

func TestRecallWhatService_Query_EmptyQuery(t *testing.T) {
	svc := newDefaultService(t)
	if _, err := svc.Query(context.Background(), RecallWhatRequest{Query: "  ", Scope: "proj1"}); err == nil {
		t.Fatal("expected an error for a blank query")
	}
}

func TestRecallWhatService_Query_KBounds(t *testing.T) {
	svc := newDefaultService(t)
	for name, k := range map[string]int{"negative": -1, "too large": recall.MaxK + 1} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Query(context.Background(), RecallWhatRequest{Query: "x", Scope: "proj1", K: k}); err == nil {
				t.Fatalf("k=%d: expected a refusal", k)
			}
		})
	}
}

// TestRecallWhatService_Query_InventedScopeRefused proves D5.1: a
// caller-asserted scope that disagrees with the server-resolved one is
// refused with KindInvalidInput, never silently honoured. Failing input:
// scope "attacker-invented-scope" against a resolver that resolves to
// "proj1".
func TestRecallWhatService_Query_InventedScopeRefused(t *testing.T) {
	svc := newDefaultService(t)
	_, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello", Scope: "attacker-invented-scope"})
	kind, _ := cascade.KindOf(err)
	if err == nil || kind != cascade.KindInvalidInput {
		t.Fatalf("err = %v (kind %v), want KindInvalidInput", err, kind)
	}
}

// TestRecallWhatService_Query_ScopeResolutionFailurePropagates: a broken
// resolver fails the whole query rather than silently falling back to an
// unscoped search.
func TestRecallWhatService_Query_ScopeResolutionFailurePropagates(t *testing.T) {
	files, conv, mem := baselineLegs()
	broken := fakeScopeResolver{err: cascade.New(cascade.KindUnavailable, "graph store unreachable")}
	svc, err := NewRecallWhatService(files, conv, mem, rrf.Params{}, testClock(), broken, testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	if _, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"}); err == nil {
		t.Fatal("expected the scope resolution failure to propagate")
	}
}

// TestRecallWhatService_Query_FusesAcrossDomains fuses across the two
// domains that can still answer: D6/Q1 made the conversation leg
// unconditionally unavailable (conversationOutcome, recallwhat_legs.go),
// so a baseline query now produces file+memory results only, and
// Errors[conversation] is always populated alongside them.
func TestRecallWhatService_Query_FusesAcrossDomains(t *testing.T) {
	svc := newDefaultService(t)
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("Results = %d rows, want 2 (file + memory; conversation is unconditionally unavailable): %+v", len(resp.Results), resp.Results)
	}
	seen := map[string]bool{}
	for _, r := range resp.Results {
		seen[r.Domain] = true
	}
	for _, d := range []string{DomainFile, DomainMemory} {
		if !seen[d] {
			t.Errorf("domain %q missing from Results: %+v", d, resp.Results)
		}
	}
	if seen[DomainConversation] {
		t.Errorf("conversation reached Results despite being unconditionally unavailable: %+v", resp.Results)
	}
	if len(resp.Citations) != 2 {
		t.Errorf("Citations = %d, want 2", len(resp.Citations))
	}
	if got := strings.Join(resp.Legs, ","); got != "file,memory" {
		t.Errorf("Legs = %q, want sorted file,memory", got)
	}
	if resp.Errors[DomainConversation] == "" {
		t.Error("Errors[conversation] empty, want the domain-unavailable refusal recorded on every call")
	}
}

// TestRecallWhatService_Query_DomainUnavailable_PartialResults: with files
// ALSO forced unavailable and conversation unconditionally unavailable
// (D6/Q1), only memory can still answer -- a two-domain partial failure,
// not the single-domain case this test proved before the rework.
func TestRecallWhatService_Query_DomainUnavailable_PartialResults(t *testing.T) {
	files, conv, mem := baselineLegs()
	files.err = cascade.New(cascade.KindUnavailable, "files index unreadable")
	svc := newBaselineService(t, files, conv, mem)
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	if err != nil {
		t.Fatalf("a domain-unavailable leg must not fail the whole query: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Results = %d, want 1 (memory only; files forced unavailable, conversation always is)", len(resp.Results))
	}
	if resp.Errors[DomainFile] == "" {
		t.Errorf("Errors[%q] empty, want the files leg's error recorded", DomainFile)
	}
	if resp.Errors[DomainConversation] == "" {
		t.Errorf("Errors[%q] empty, want the conversation leg's domain-unavailable refusal recorded", DomainConversation)
	}
}

// TestRecallWhatService_Query_AllDomainsUnavailable: conversation no
// longer needs conv.searchErr forced (D6/Q1 -- it fails unconditionally,
// SearchTurns is never called), so only files and memory are forced here.
func TestRecallWhatService_Query_AllDomainsUnavailable(t *testing.T) {
	files, conv, mem := baselineLegs()
	files.err = errors.New("boom")
	mem.err = errors.New("boom")
	svc := newBaselineService(t, files, conv, mem)
	_, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	kind, _ := cascade.KindOf(err)
	if err == nil || kind != cascade.KindUnavailable {
		t.Fatalf("err = %v (kind %v), want a KindUnavailable refusal", err, kind)
	}
}

func TestRecallWhatHandler_Query_NoServiceConfigured(t *testing.T) {
	h := NewRecallWhatHandler(nil)
	_, err := h.Query(context.Background(), nil)
	kind, _ := cascade.KindOf(err)
	if err == nil || kind != cascade.KindUnavailable {
		t.Fatalf("err = %v (kind %v), want KindUnavailable", err, kind)
	}
}

func TestRecallWhatHandler_Query_MalformedParams(t *testing.T) {
	svc := newDefaultService(t)
	h := NewRecallWhatHandler(svc)
	_, err := h.Query(context.Background(), json.RawMessage(`{"query":`))
	kind, _ := cascade.KindOf(err)
	if err == nil || kind != cascade.KindInvalidInput {
		t.Fatalf("err = %v (kind %v), want KindInvalidInput", err, kind)
	}
}

// TestRecallWhatRPC is this ticket's Art.2 proof: the real internal/rpc
// Registry/Handler pipeline (rpc.Parse + Registry.Dispatch), the same
// code the daemon composition root builds, not a self-authored dialect.
func TestRecallWhatRPC(t *testing.T) {
	svc := newDefaultService(t)
	registry := rpc.NewRegistry()
	NewRecallWhatHandler(svc).Register(registry)
	if !registry.Registered(MethodWhat) {
		t.Fatal("recall.what was not registered")
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"recall.what","params":{"query":"hello"}}`
	req, errObj := rpc.Parse([]byte(body))
	if errObj != nil {
		t.Fatalf("Parse: %+v", errObj)
	}
	result, errObj := registry.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	resp, ok := result.(WhatResult)
	if !ok {
		t.Fatalf("result type = %T, want WhatResult", result)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("Results = %d, want 2 (file + memory; conversation is unconditionally unavailable, D6/Q1)", len(resp.Results))
	}
}

// TestScopeRefFor_GeneralKindResolvesToEmpty: an unresolved session
// (R-16.3's restricted-but-successful `general` result) narrows every
// leg to nothing rather than widening to everything.
func TestScopeRefFor_GeneralKindResolvesToEmpty(t *testing.T) {
	if got := scopeRefFor(scope.SessionScope{Kind: scope.ScopeKindGeneral, Project: "leaked"}); got != "" {
		t.Fatalf("scopeRefFor(general) = %q, want empty even though Project was set", got)
	}
}

// TestConversationOutcome_AlwaysUnavailable and
// TestConversationOutcome_NilLegNotConfigured (D6/Q1's exact-Kind proof at
// the leg level) live in recallwhat_legs_test.go, split out purely for
// the 300-line cap.

func TestNonNilWhatResult_NormalizesNilSlices(t *testing.T) {
	got := nonNilWhatResult(RecallWhatResponse{})
	if got.Results == nil || got.Citations == nil || got.Legs == nil {
		t.Fatalf("nonNilWhatResult left a nil slice: %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"results":[]`, `"citations":[]`, `"legs":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("json = %s, missing %s", raw, want)
		}
	}
}

// brokenEgress refuses every call, proving respondFromFused fails the
// WHOLE request rather than silently emitting a field the boundary could
// not sanitize (recallwhat_redact.go's documented fail-closed contract).
type brokenEgress struct{}

func (brokenEgress) InterceptClass(context.Context, egress.EgressClass, egress.SensitivityTier, []byte) ([]byte, error) {
	return nil, errors.New("egress unavailable")
}

func TestRecallWhatService_Query_EgressBoundaryFailureFailsWholeRequest(t *testing.T) {
	files, conv, mem := baselineLegs()
	svc, err := NewRecallWhatService(files, conv, mem, rrf.Params{}, testClock(), resolverFor("proj1"), brokenEgress{})
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	_, err = svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	if err == nil {
		t.Fatal("an unavailable egress boundary must fail the whole request, not emit an unsanitized field")
	}
}

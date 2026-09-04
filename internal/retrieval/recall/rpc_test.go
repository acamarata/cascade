// Purpose: the recall.* namespace's tests — dispatch through the REAL
// internal/rpc Registry (the same type the daemon composition root
// builds), the params decode, the wire error mapping, and the scope
// guarantee re-asserted at the RPC boundary because that is where the
// answer stops being a Go value and becomes bytes on a socket.
//
// Constraints: Art.7 — no network in this untagged lane; the real socket
// and the real HTTP/1.1 JSON-RPC exchange are rpc_integration_test.go's.
// SPORT: internal.retrieval.recall.Handler/ADDED (P1-E06-W2-S11-T3).

package recall

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// dispatch sends one request through the real registry and returns the
// decoded result or the wire error object.
func dispatch(t *testing.T, registry *rpc.Registry, method string, params any) (QueryResult, *rpc.ErrorObject) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Method: method, Params: raw,
	})
	if errObj != nil {
		return QueryResult{}, errObj
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out QueryResult
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out, nil
}

// registryWithRecall registers the handler on a real registry.
func registryWithRecall(t *testing.T, legs ...Leg) *rpc.Registry {
	t.Helper()
	registry := rpc.NewRegistry()
	NewHandler(newTestService(t, legs...)).Register(registry)
	return registry
}

// TestRecallRPCRegistersBothNames is the mirror-rule proof: recall.query
// and its v1-parity alias both resolve on the real registry.
func TestRecallRPCRegistersBothNames(t *testing.T) {
	registry := registryWithRecall(t)
	for _, method := range []string{MethodQuery, MethodSearchAlias} {
		if !registry.Registered(method) {
			t.Errorf("%s is not registered on the real registry", method)
		}
	}
}

// TestRecallRPCAliasAnswersIdentically: the alias is bound to the same
// handler value, so the two names cannot answer differently.
func TestRecallRPCAliasAnswersIdentically(t *testing.T) {
	registry := registryWithRecall(t)
	params := QueryParams{Query: testQuery, Scope: "project/cascade"}
	canonical, errObj := dispatch(t, registry, MethodQuery, params)
	if errObj != nil {
		t.Fatalf("recall.query: %+v", errObj)
	}
	alias, errObj := dispatch(t, registry, MethodSearchAlias, params)
	if errObj != nil {
		t.Fatalf("%s: %+v", MethodSearchAlias, errObj)
	}
	if len(canonical.Results) == 0 {
		t.Fatal("recall.query returned nothing, so the comparison would be vacuous")
	}
	canonicalJSON, _ := json.Marshal(canonical)
	aliasJSON, _ := json.Marshal(alias)
	if string(canonicalJSON) != string(aliasJSON) {
		t.Errorf("the alias answered differently:\n%s\n%s", canonicalJSON, aliasJSON)
	}
}

// TestRecallRPCCitationsAlwaysRide: v1 parity — a search response
// describes its own provenance whether or not --cite was asked for. Only
// the RENDERED block is gated on the flag.
func TestRecallRPCCitationsAlwaysRide(t *testing.T) {
	registry := registryWithRecall(t)
	plain, errObj := dispatch(t, registry, MethodQuery, QueryParams{Query: testQuery, Scope: "project/cascade"})
	if errObj != nil {
		t.Fatalf("recall.query: %+v", errObj)
	}
	if len(plain.Citations) == 0 {
		t.Error("the result carried no citations array")
	}
	if plain.Rendered != "" {
		t.Errorf("the rendered block appeared without --cite: %q", plain.Rendered)
	}
	cited, errObj := dispatch(t, registry, MethodQuery,
		QueryParams{Query: testQuery, Scope: "project/cascade", Cite: true})
	if errObj != nil {
		t.Fatalf("recall.query --cite: %+v", errObj)
	}
	if !strings.Contains(cited.Rendered, "handbook/fusion.md") {
		t.Errorf("--cite rendered no citation block:\n%s", cited.Rendered)
	}
}

// TestRecallRPCScopeHoldsOnTheWire re-asserts the guarantee where the
// answer becomes bytes: nothing from the unauthorized corpus may appear
// in the serialized response at all, including in its error text.
func TestRecallRPCScopeHoldsOnTheWire(t *testing.T) {
	registry := registryWithRecall(t, lexicalLeg{}, leakyLeg{chunkID: "c-secret"})
	result, errObj := dispatch(t, registry, MethodQuery,
		QueryParams{Query: testQuery, Scope: "project/cascade", Cite: true})
	if errObj != nil {
		t.Fatalf("recall.query: %+v", errObj)
	}
	if len(result.Results) == 0 {
		t.Fatal("the query returned nothing, so the scope assertion would be vacuous")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, forbidden := range []string{"quokka", "secrets.md", "journal", "c-secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("the wire response carried %q:\n%s", forbidden, encoded)
		}
	}
	if result.Withheld != 1 {
		t.Errorf("Withheld = %d, want the leaked row counted", result.Withheld)
	}
}

// TestRecallRPCErrorsCarryTheirTaxonomyCode: the registry maps each Kind
// onto its frozen JSON-RPC code, so a peer can classify a refusal without
// reading its prose.
func TestRecallRPCErrorsCarryTheirTaxonomyCode(t *testing.T) {
	registry := registryWithRecall(t)
	cases := map[string]struct {
		params QueryParams
		code   int
	}{
		"empty query":    {QueryParams{Scope: "project/cascade"}, cascade.RPCCodeInvalidInput},
		"bad scope":      {QueryParams{Query: testQuery, Scope: "a scope"}, cascade.RPCCodeInvalidInput},
		"unknown corpus": {QueryParams{Query: testQuery, Scope: "project/cascade", Corpus: []string{"nope"}}, cascade.RPCCodeNotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, errObj := dispatch(t, registry, MethodQuery, tc.params)
			if errObj == nil {
				t.Fatal("want a wire error, got a result")
			}
			if errObj.Code != tc.code {
				t.Errorf("code = %d, want %d (%s)", errObj.Code, tc.code, errObj.Message)
			}
		})
	}
}

// TestRecallRPCMalformedParamsAreInvalidInput: a peer's bad bytes are a
// typed refusal, never a panic and never an empty result set.
func TestRecallRPCMalformedParamsAreInvalidInput(t *testing.T) {
	registry := registryWithRecall(t)
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Method: MethodQuery,
		Params: json.RawMessage(`{"k":"not a number"}`),
	})
	if errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("errObj = %+v, want invalid-input", errObj)
	}
}

// TestRecallRPCAbsentParamsRefuseOnTheMissingField: a call with no params
// at all is answered with "the query is empty" rather than a parse error
// that says nothing about what was missing.
func TestRecallRPCAbsentParamsRefuseOnTheMissingField(t *testing.T) {
	registry := registryWithRecall(t)
	for _, params := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Method: MethodQuery, Params: params,
		})
		if errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
			t.Fatalf("errObj = %+v, want invalid-input", errObj)
		}
		if !strings.Contains(errObj.Message, "query is empty") {
			t.Errorf("message = %q, want it to name the missing field", errObj.Message)
		}
	}
}

// TestRecallRPCWithNoServiceIsUnavailable: a handler the composition root
// built without a service refuses rather than panicking at the far end of
// an RPC, which is a crash reported as a hang.
func TestRecallRPCWithNoServiceIsUnavailable(t *testing.T) {
	registry := rpc.NewRegistry()
	NewHandler(nil).Register(registry)
	_, errObj := dispatch(t, registry, MethodQuery, QueryParams{Query: testQuery, Scope: "s"})
	if errObj == nil || errObj.Code != cascade.RPCCodeUnavailable {
		t.Fatalf("errObj = %+v, want unavailable", errObj)
	}
}

// TestRecallRPCEmptyMatchIsAResultNotAnError, and its arrays serialize as
// [] rather than null so a client can iterate them without a nil check.
func TestRecallRPCEmptyMatchIsAResultNotAnError(t *testing.T) {
	registry := registryWithRecall(t)
	result, errObj := dispatch(t, registry, MethodQuery,
		QueryParams{Query: "nothing in this index says this", Scope: "project/cascade"})
	if errObj != nil {
		t.Fatalf("an empty match must not be a wire error: %+v", errObj)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"results":[]`, `"citations":[]`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("response %s does not contain %s", encoded, want)
		}
	}
}

// TestRecallRPCPassesKAndCorpusThrough proves the flags reach the fusion
// query rather than being decorative.
func TestRecallRPCPassesKAndCorpusThrough(t *testing.T) {
	registry := registryWithRecall(t)
	result, errObj := dispatch(t, registry, MethodQuery, QueryParams{
		Query: testQuery, Scope: "project/cascade", Corpus: []string{"handbook"}, K: 1,
	})
	if errObj != nil {
		t.Fatalf("recall.query: %+v", errObj)
	}
	if len(result.Results) != 1 {
		t.Fatalf("k=1 returned %d results", len(result.Results))
	}
	if result.Results[0].CorpusID != handbookCorpus.ID {
		t.Errorf("result came from corpus %q", result.Results[0].CorpusID)
	}
	if len(result.Legs) != 1 || result.Legs[0] != string(rrf.StrategyFTS) {
		t.Errorf("Legs = %v", result.Legs)
	}
}

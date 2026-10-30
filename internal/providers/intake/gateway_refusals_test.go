package intake

// Purpose: the refusal and edge paths of gateway add and reauth: incomplete
// dependencies, malformed requests, a source that yields no token, hostile
// catalog responses, transport failures and a re-add of a stored record.
// Constraints: fakeDoer and MemoryRegistry only; no network, vault or keychain.
// SPORT: provider.intake.gateway tests (ADD).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAddGatewayRefusesMalformedRequests(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Deps, *GatewayAddRequest)
		kind   cascade.Kind
	}{
		"incomplete deps":  {func(d *Deps, _ *GatewayAddRequest) { d.Doer = nil }, cascade.KindInvalidInput},
		"bad header style": {func(_ *Deps, r *GatewayAddRequest) { r.Account.Header = "bogus" }, cascade.KindInvalidInput},
		"bad account name": {func(_ *Deps, r *GatewayAddRequest) { r.Account.Name = "a/b" }, cascade.KindInvalidInput},
		"blank name":       {func(_ *Deps, r *GatewayAddRequest) { r.Name = "  " }, cascade.KindInvalidInput},
		"source has no token": {func(_ *Deps, r *GatewayAddRequest) {
			r.Source = gateway.Source{Getenv: func(string) string { return "" }}
		}, cascade.KindUnavailable},
	}
	for name, c := range cases {
		deps, custody, doer, req, _ := gatewaySetup(t)
		c.mutate(&deps, &req)
		if _, err := AddGateway(context.Background(), deps, req); !cascade.HasKind(err, c.kind) {
			t.Fatalf("%s: %v, want %v", name, err, c.kind)
		}
		if len(doer.calls) != 0 || len(custody.setCall) != 0 {
			t.Fatalf("%s: a refused request sent traffic or wrote custody", name)
		}
	}
}

func TestAddGatewayCatalogFailures(t *testing.T) {
	models := func(r *GatewayAddRequest) string { return r.Account.BaseURL + "/v1/models" }
	cases := map[string]struct {
		set  func(*fakeDoer, string)
		kind cascade.Kind
	}{
		"rate limited":  {func(d *fakeDoer, u string) { d.responses[u] = HTTPResponse{Status: 429} }, cascade.KindQuotaExhausted},
		"malformed":     {func(d *fakeDoer, u string) { d.responses[u] = HTTPResponse{Status: 200, Body: []byte("{")} }, cascade.KindIntegrity},
		"empty catalog": {func(d *fakeDoer, u string) { d.responses[u] = HTTPResponse{Status: 200, Body: []byte(`{"data":[]}`)} }, cascade.KindUnavailable},
		"transport":     {func(d *fakeDoer, u string) { d.err = map[string]error{u: errors.New("dial failed")} }, cascade.KindUnavailable},
	}
	for name, c := range cases {
		deps, _, doer, req, canary := gatewaySetup(t)
		c.set(doer, models(&req))
		_, err := AddGateway(context.Background(), deps, req)
		if !cascade.HasKind(err, c.kind) {
			t.Fatalf("%s: %v, want %v", name, err, c.kind)
		}
		if err != nil && strings.Contains(err.Error(), canary) {
			t.Fatalf("%s: the error carries the token", name)
		}
	}
}

func TestAddGatewayMicroVerifyTransportFailure(t *testing.T) {
	deps, _, doer, req, canary := gatewaySetup(t)
	req.Account.Models = []string{"model-a"}
	doer.err = map[string]error{req.Account.BaseURL + "/v1/messages": errors.New("reset by peer " + canary)}
	_, err := AddGateway(context.Background(), deps, req)
	if !cascade.HasKind(err, cascade.KindUnavailable) || strings.Contains(err.Error(), canary) {
		t.Fatalf("transport failure: %v, want unavailable with the token redacted", err)
	}
}

func TestAddGatewayReaddUpdatesStoredRecord(t *testing.T) {
	deps, _, _, req, _ := gatewaySetup(t)
	first, err := AddGateway(context.Background(), deps, req)
	if err != nil || first.Status != "converged" {
		t.Fatalf("first add: %+v, %v", first, err)
	}
	second, err := AddGateway(context.Background(), deps, req)
	if err != nil || second.Status != "updated" || !second.Record.CreatedAt.Equal(first.Record.CreatedAt) {
		t.Fatalf("re-add: %+v, %v; want an update that keeps CreatedAt", second, err)
	}
}

func TestReauthGatewayRefusals(t *testing.T) {
	deps, _, _, req, _ := gatewaySetup(t)
	req.NoVerify = true
	added, err := AddGateway(context.Background(), deps, req)
	if err != nil {
		t.Fatal(err)
	}
	rec := added.Record
	noModels, moved := rec, rec
	noModels.KnownModels = nil
	moved.BaseURL = "https://elsewhere.invalid"
	empty := req.Source
	empty.Getenv = func(string) string { return "" }
	incomplete := deps
	incomplete.Clock = nil
	cases := map[string]struct {
		deps Deps
		rec  ProviderRecord
		src  gateway.Source
		kind cascade.Kind
	}{
		"incomplete deps": {incomplete, rec, req.Source, cascade.KindInvalidInput},
		"no catalog":      {deps, noModels, req.Source, cascade.KindUnavailable},
		"endpoint moved":  {deps, moved, req.Source, cascade.KindInvalidInput},
		"no token":        {deps, rec, empty, cascade.KindUnavailable},
	}
	for name, c := range cases {
		if _, err := ReauthGateway(context.Background(), c.deps, c.rec, req.Account, c.src); !cascade.HasKind(err, c.kind) {
			t.Fatalf("%s: %v, want %v", name, err, c.kind)
		}
	}
}

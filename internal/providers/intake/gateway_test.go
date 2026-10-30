package intake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/pkg/cascade"
)

func gatewayFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/gateway/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gatewaySetup(t *testing.T) (Deps, *spyCustody, *fakeDoer, GatewayAddRequest, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	canary := hex.EncodeToString(b[:])
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://gateway.invalid/v1/models":   {Status: 200, Body: gatewayFixture(t, "models.json")},
		"https://gateway.invalid/v1/messages": {Status: 200, Body: gatewayFixture(t, "messages_ok.json")},
	}}
	deps, custody := testDeps(t, doer)
	acct := gateway.Account{Name: "acct", BaseURL: "https://gateway.invalid", Header: gateway.HeaderBearer,
		Credential: gateway.Credential{Kind: gateway.CredentialEnv, EnvVar: "GATEWAY_TOKEN"}, TermsSensitive: true}
	req := GatewayAddRequest{Name: "gw1", Account: acct, Source: gateway.Source{Getenv: func(string) string { return canary }}}
	return deps, custody, doer, req, canary
}

// TestAddGatewayDiscoversCatalog pins auth, catalog metadata and custody isolation.
func TestAddGatewayDiscoversCatalog(t *testing.T) {
	deps, custody, doer, req, canary := gatewaySetup(t)
	res, err := AddGateway(context.Background(), deps, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(doer.calls) != 2 || doer.calls[0].Method != "GET" || doer.calls[0].URL != req.Account.BaseURL+"/v1/models" {
		t.Fatal("wrong catalog request")
	}
	if doer.calls[0].Headers["authorization"] != "Bearer "+canary || doer.calls[0].Headers["x-api-key"] != "" {
		t.Fatal("wrong catalog auth")
	}
	r := res.Record
	if r.Auth != AuthGateway || r.AuthRef != "gateway:acct" || r.Driver != DriverAnthropic || r.BaseURL != req.Account.BaseURL {
		t.Fatalf("record: %+v", r)
	}
	if !reflect.DeepEqual(r.KnownModels, []string{"model-a", "model-b"}) || !r.CapabilitiesProbedAt.Equal(deps.Clock.Now()) {
		t.Fatal("wrong catalog metadata")
	}
	if len(custody.setCall) != 0 || len(custody.getCall) != 0 {
		t.Fatal("gateway accessed custody")
	}
	if !reflect.DeepEqual(res.Warnings, []string{"catalog: discovered"}) {
		t.Fatal(res.Warnings)
	}
}

// TestAddGatewayDeclaredCatalog ensures the first declared model is verified.
func TestAddGatewayDeclaredCatalog(t *testing.T) {
	deps, _, doer, req, canary := gatewaySetup(t)
	req.Account.Models = []string{"model-b", "model-a"}
	req.Account.Header = gateway.HeaderXAPIKey
	res, err := AddGateway(context.Background(), deps, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(doer.calls) != 1 || doer.calls[0].Method != "POST" {
		t.Fatal("declared catalog was fetched")
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(doer.calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "model-b" || doer.calls[0].Headers["x-api-key"] != canary || doer.calls[0].Headers["authorization"] != "" {
		t.Fatal("wrong declared micro-verify")
	}
	if !reflect.DeepEqual(res.Warnings, []string{"catalog: declared"}) {
		t.Fatal(res.Warnings)
	}
}

// TestAddGatewayNoCatalog pins both missing-endpoint remedies.
func TestAddGatewayNoCatalog(t *testing.T) {
	for _, status := range []int{404, 405} {
		deps, custody, doer, req, _ := gatewaySetup(t)
		doer.responses[req.Account.BaseURL+"/v1/models"] = HTTPResponse{Status: status}
		_, err := AddGateway(context.Background(), deps, req)
		if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "declare models") || !strings.Contains(err.Error(), "/v1/models") {
			t.Fatalf("missing remedies: %v", err)
		}
		if len(custody.setCall) != 0 {
			t.Fatal("stored a token")
		}
	}
}

// TestAddGatewayMicroVerifyBearer replays success and unauthorized responses.
func TestAddGatewayMicroVerifyBearer(t *testing.T) {
	for _, status := range []int{200, 401} {
		deps, custody, doer, req, canary := gatewaySetup(t)
		req.Account.Models = []string{"model-a"}
		if status == 401 {
			doer.responses[req.Account.BaseURL+"/v1/messages"] = HTTPResponse{Status: status, Body: gatewayFixture(t, "unauthorized_401.json")}
		}
		_, err := AddGateway(context.Background(), deps, req)
		if status == 200 && err != nil {
			t.Fatal(err)
		}
		if status == 401 && !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("401: %v", err)
		}
		if doer.calls[0].Headers["authorization"] != "Bearer "+canary || doer.calls[0].Headers["x-api-key"] != "" {
			t.Fatal("wrong verify auth")
		}
		if len(custody.setCall) != 0 {
			t.Fatal("stored a token")
		}
	}
}

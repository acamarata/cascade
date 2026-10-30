package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

type gatewayCLIDoer struct {
	calls  []intake.HTTPRequest
	status int
}

func (d *gatewayCLIDoer) Do(_ context.Context, req intake.HTTPRequest) (intake.HTTPResponse, error) {
	d.calls = append(d.calls, req)
	if strings.HasSuffix(req.URL, "/v1/models") {
		return intake.HTTPResponse{Status: 200, Body: []byte(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`)}, nil
	}
	return intake.HTTPResponse{Status: d.status, Body: []byte(`{"content":[{"type":"text","text":"ok"}]}`)}, nil
}

func gatewayCLIDeps(t *testing.T) (providerDeps, *gatewayCLIDoer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, "cascade"))
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	canary := hex.EncodeToString(b[:])
	deps := testProviderDeps(t, map[string]string{"GATEWAY_TOKEN": canary, "HOME": home})
	deps.Registry = nil
	deps.NewCustody = func() (secrets.Custody, error) { t.Fatal("gateway selected custody"); return nil, nil }
	deps.ReadStdin = func() ([]byte, error) { t.Fatal("gateway read stdin"); return nil, nil }
	d := &gatewayCLIDoer{status: 200}
	deps.Doer = d
	if err := os.MkdirAll(filepath.Dir(deps.Paths.ConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[gateways.acct]\nbase_url = \"https://gateway.invalid\"\nkey_env = \"GATEWAY_TOKEN\"\n"
	if err := os.WriteFile(deps.Paths.ConfigPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return deps, d, canary
}

func gatewayCLIRecords(t *testing.T, deps providerDeps) (intake.ProviderRecord, []registry.LaneRecord) {
	t.Helper()
	reg, closeReg, err := gatewayProviderRegistry(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeReg() }()
	rec, err := reg.GetProvider(context.Background(), "gw1")
	if err != nil {
		t.Fatal(err)
	}
	lanes, err := reg.(registryAdapter).reg.ListLanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rec, lanes
}

// TestProviderAddGatewayCLI pins flag exclusions, durable lanes and token secrecy.
func TestProviderAddGatewayCLI(t *testing.T) {
	deps, doer, canary := gatewayCLIDeps(t)
	for _, extra := range [][]string{{"--key"}, {"--key-env", "K"}, {"--oauth"}, {"--base-url", "https://gateway.invalid"}, {"--kind", "anthropic"}, {"--key=false"}, {"--key-env="}} {
		args := append([]string{"add", "gw1", "--gateway", "acct"}, extra...)
		_, _, err := runProvider(t, deps, args...)
		if !cascade.HasKind(err, cascade.KindInvalidInput) || len(doer.calls) != 0 {
			t.Fatalf("forbidden flags: %v", err)
		}
	}
	out, stderr, err := runProvider(t, deps, "add", "gw1", "--gateway", "acct", "--pool", "pp", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out+stderr, canary) {
		t.Fatal("token in output")
	}
	rec, lanes := gatewayCLIRecords(t, deps)
	if rec.Auth != intake.AuthGateway || rec.AuthRef != "gateway:acct" || len(lanes) != 1 || lanes[0].LaneName != "pp/gw1" || lanes[0].Capacity != registry.CapacityAPICredit || lanes[0].State != registry.LaneStateAvailable {
		t.Fatalf("record/lane mismatch: %+v %+v", rec, lanes)
	}
	if doer.calls[0].Headers["authorization"] != "Bearer "+canary {
		t.Fatal("missing bearer credential")
	}
	for _, root := range []string{deps.Paths.DataDir(), filepath.Dir(deps.Paths.ConfigPath()), deps.Getenv("HOME")} {
		scanGatewayCanary(t, root, canary)
	}
}

func scanGatewayCanary(t *testing.T, root, canary string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), canary) {
			t.Error("token persisted in file")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestProviderReauthGatewayReverifies pins flagless verification and lane recovery.
func TestProviderReauthGatewayReverifies(t *testing.T) {
	deps, doer, canary := gatewayCLIDeps(t)
	if _, _, err := runProvider(t, deps, "add", "gw1", "--gateway", "acct", "--no-verify"); err != nil {
		t.Fatal(err)
	}
	before, _ := gatewayCLIRecords(t, deps)
	calls := len(doer.calls)
	if _, _, err := runProvider(t, deps, "reauth", "gw1", "--key-env", "K"); !cascade.HasKind(err, cascade.KindInvalidInput) || len(doer.calls) != calls {
		t.Fatalf("override accepted: %v", err)
	}
	doer.status = 401
	if _, _, err := runProvider(t, deps, "reauth", "gw1"); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("failed verify: %v", err)
	}
	after, lanes := gatewayCLIRecords(t, deps)
	if !reflect.DeepEqual(before, after) || lanes[0].State != registry.LaneStateUnknown {
		t.Fatal("failed reauth changed state")
	}
	doer.status = 200
	out, stderr, err := runProvider(t, deps, "reauth", "gw1")
	if err != nil {
		t.Fatal(err)
	}
	_, lanes = gatewayCLIRecords(t, deps)
	if len(lanes) != 1 || lanes[0].State != registry.LaneStateAvailable || strings.Contains(out+stderr, canary) {
		t.Fatal("reauth did not recover safely")
	}
}

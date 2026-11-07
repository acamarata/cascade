// Purpose: tests for the gateway credential class outside a running spawn: the
// [agents.auth.<harness>] shape, admission of the account, the exact driver and
// tool environments, and the three host-injected variables staying out of
// every other spawn. Shared fixtures for the gateway tests live here.
// Constraints: fake values are built at run time from plain words; the host is
// gateway.invalid; a cascade error is asserted by kind and message.
// SPORT: internal.providers.agents tests (ADD) - P1-GWY-05.

package agents

import (
	"context"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/agents/claude"
)

const (
	gwAccountName = "gateway-one"
	gwBaseURL     = "https://gateway.invalid"
	gwHost        = egressproxy.Destination("gateway.invalid:443")
	gwSourceVar   = "TEST_GATEWAY_SOURCE"
	gwAuthName    = "ANTHROPIC_AUTH_TOKEN"
	gwBaseName    = "ANTHROPIC_BASE_URL"
	gwManagedName = "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"
)

// gwCanary is a per-test fake value: plain lower-case words, no digits.
// gwFix is a shim fixture on class gateway. The integration-tagged egress
// tests install a recording in-memory dial through gwInstallDial; untagged
// tests keep the production dial and never reach an upstream.
type gwFix struct {
	*shimFix
	mu    sync.Mutex
	dials []string
}

// gwInstallDial is set by the integration-tagged tests; nil elsewhere.
var gwInstallDial func(*gwFix)

// newGatewayFix wires class gateway; value "" leaves the account's source unset.
func newGatewayFix(t *testing.T, value string) *gwFix {
	t.Helper()
	f := &gwFix{shimFix: newShimFix(t)}
	if value != "" {
		f.env[gwSourceVar] = value
	}
	f.deps.Auth[HarnessClaude] = gwAuth()
	f.deps.Gateway = GatewayDeps{Accounts: gwAccounts(), Source: gateway.Source{}}
	f.rebuild()
	return f
}

// rebuild rebuilds the shim and installs the recording dial when set.
func (f *gwFix) rebuild() {
	f.build()
	if gwInstallDial != nil {
		gwInstallDial(f)
	}
}

func gwCanary(t *testing.T) string {
	t.Helper()
	return "fake-gateway-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
}

func gwAccounts() []gateway.Account {
	return []gateway.Account{{Name: gwAccountName, ID: gwBaseURL, BaseURL: gwBaseURL, Header: gateway.HeaderBearer,
		Credential: gateway.Credential{Kind: gateway.CredentialEnv, EnvVar: gwSourceVar}, TermsSensitive: true}}
}

func gwAuth() DriverAuth {
	return DriverAuth{Harness: HarnessClaude, Class: CredentialGateway, Gateway: gwAccountName}
}

func TestParseDriverAuthGatewayClass(t *testing.T) {
	bs := []Binding{claudeRow, codexRow}
	got, err := ParseDriverAuth(authExtra("claude", map[string]any{"class": "gateway", "gateway": gwAccountName}), bs)
	if err != nil || got[HarnessClaude] != gwAuth() {
		t.Fatalf("ParseDriverAuth(gateway) = %+v, %v", got, err)
	}
	if v, err := KeyVarFor(claudeRow, CredentialGateway); err != nil || v != gwAuthName {
		t.Fatalf("KeyVarFor(gateway) = %q, %v, want the row's variable", v, err)
	}
	for name, c := range map[string]struct {
		harness string
		table   map[string]any
		want    string
	}{
		"account required":       {"claude", map[string]any{"class": "gateway"}, "gateway is required"},
		"key_env with gateway":   {"claude", map[string]any{"class": "gateway", "gateway": gwAccountName, "key_env": "SOME_SOURCE"}, "key_env is not allowed"},
		"auth_source refused":    {"claude", map[string]any{"class": "gateway", "gateway": gwAccountName, "auth_source": "/a/b"}, "auth_source is not allowed"},
		"harness without row":    {"codex", map[string]any{"class": "gateway", "gateway": gwAccountName}, "not offered"},
		"account on another key": {"claude", map[string]any{"class": "api_key", "key_env": "SOME_SOURCE", "gateway": gwAccountName}, "only allowed for class gateway"},
		"variable name in table": {"claude", map[string]any{"class": "gateway", "gateway": gwAccountName, "key_var": "OTHER"}, "unknown key"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDriverAuth(authExtra(c.harness, c.table), bs)
			wantInvalid(t, err, c.want)
		})
	}
}

func TestGatewayRowValidation(t *testing.T) {
	for name, edit := range map[string]func(*Binding){
		"row in the shared table": func(b *Binding) { b.Credentials = append(b.Credentials, b.Gateway) },
		"variable repeated":       func(b *Binding) { b.Gateway.ManagedVar = b.Gateway.KeyVar },
		"variable missing":        func(b *Binding) { b.Gateway.BaseURLVar = "" },
		"config directory reused": func(b *Binding) { b.Gateway.KeyVar = b.ConfigDirVar },
		"wrong class":             func(b *Binding) { b.Gateway.Class = CredentialAPIKey },
		"gateway field elsewhere": func(b *Binding) {
			b.Credentials = []CredentialSpec{{Class: CredentialAPIKey, KeyVar: "K", ManagedVar: "M"}}
		},
	} {
		b := copyBinding(claudeRow)
		edit(&b)
		if validateBinding(b) == nil {
			t.Errorf("%s: validateBinding accepted the row", name)
		}
	}
	if err := validateBinding(claudeRow); err != nil {
		t.Fatalf("the claude row is invalid: %v", err)
	}
}

func TestResolveGatewayCredentialRefusesUnadmitted(t *testing.T) {
	canary := gwCanary(t)
	deps := GatewayDeps{Accounts: gwAccounts(), Source: gateway.Source{Getenv: func(n string) string {
		if n == gwSourceVar {
			return canary
		}
		return ""
	}}}
	got, err := ResolveGatewayCredential(context.Background(), deps, gwAccountName)
	if err != nil || string(got.Token) != canary || got.BaseURL != gwBaseURL || got.Destination != gwHost {
		t.Fatalf("admitted account = %+v, %v", got.Destination, err)
	}
	_, err = ResolveGatewayCredential(context.Background(), deps, "not-admitted")
	if !cascade.HasKind(err, cascade.KindNotFound) || !strings.Contains(err.Error(), `"not-admitted" is not configured`) {
		t.Fatalf("unadmitted account: %v, want KindNotFound naming it", err)
	}
	t.Run("at the spawn", func(t *testing.T) {
		f := newGatewayFix(t, gwCanary(t))
		f.deps.Auth[HarnessClaude] = DriverAuth{Harness: HarnessClaude, Class: CredentialGateway, Gateway: "not-admitted"}
		f.rebuild()
		_, err := f.shim.Spawn(context.Background(), f.req)
		f.refused(err)
		if !cascade.HasKind(err, cascade.KindNotFound) {
			t.Fatalf("Spawn = %v, want KindNotFound", err)
		}
	})
	t.Run("unavailable token", func(t *testing.T) {
		f := newGatewayFix(t, "")
		_, err := f.shim.Spawn(context.Background(), f.req)
		f.refused(err)
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("Spawn = %v, want KindUnavailable", err)
		}
	})
}

// gwHostile answers every name, so a builder that copies getenv is caught.
func gwHostile(name string) string {
	if name == "PATH" {
		return "/usr/bin:/bin"
	}
	return "fake-inherited-value"
}

func TestBuildDriverEnvGateway(t *testing.T) {
	canary := gwCanary(t)
	gw := GatewayEnv{BaseURL: gwBaseURL, Token: []byte(canary), Destination: gwHost}
	got, err := BuildDriverEnv(claudeRow, gwAuth(), testProfile(), "job-1", "ptok", "lease-1", sixProxy(), gwHostile, gw)
	if err != nil {
		t.Fatalf("BuildDriverEnv: %v", err)
	}
	want := []string{"CASCADE_JOB_ID", "CASCADE_LEASE_ID", "CASCADE_PRINCIPAL_TOKEN", claude.VendorConfigDirVar, "HOME", "HTTPS_PROXY", "HTTP_PROXY",
		"LANG", "NO_PROXY", "PATH", "TMPDIR", gwAuthName, gwBaseName, gwManagedName, "http_proxy", "https_proxy", "no_proxy"}
	sort.Strings(want)
	if names := envNames(got.Driver); !slices.Equal(names, want) {
		t.Fatalf("driver names = %v, want exactly %v", names, want)
	}
	for _, kv := range []string{gwAuthName + "=" + canary, gwBaseName + "=" + gwBaseURL, gwManagedName + "=1"} {
		if !envHas(got.Driver, kv) {
			t.Fatalf("driver env lacks the %s pair", strings.SplitN(kv, "=", 2)[0])
		}
	}
	for _, name := range envNames(got.Tool) {
		if name == gwAuthName || name == gwBaseName || name == gwManagedName || name == claude.VendorConfigDirVar {
			t.Fatalf("tool env carries %s", name)
		}
	}
	if !slices.ContainsFunc(got.Secrets, func(s []byte) bool { return string(s) == canary }) {
		t.Fatal("the gateway value is not in Secrets, so the output scrub would miss it")
	}
}

func TestBuildDriverEnvGatewayRefusals(t *testing.T) {
	canary := gwCanary(t)
	good := GatewayEnv{BaseURL: gwBaseURL, Token: []byte(canary), Destination: gwHost}
	apiKey := DriverAuth{Harness: HarnessClaude, Class: CredentialAPIKey, KeyEnv: "FAKE_SRC"}
	badValue := good
	badValue.Token = []byte(canary + "\nextra")
	otherHost := good
	otherHost.BaseURL = "https://other.invalid"
	empty := good
	empty.Token = nil
	for name, c := range map[string]struct {
		auth DriverAuth
		gw   []GatewayEnv
		kind cascade.Kind
	}{
		"newline in the value":   {gwAuth(), []GatewayEnv{badValue}, cascade.KindInvalidInput},
		"host differs":           {gwAuth(), []GatewayEnv{otherHost}, cascade.KindInvalidInput},
		"empty value":            {gwAuth(), []GatewayEnv{empty}, cascade.KindUnavailable},
		"no account":             {gwAuth(), nil, cascade.KindInvalidInput},
		"two accounts":           {gwAuth(), []GatewayEnv{good, good}, cascade.KindInvalidInput},
		"account on another key": {apiKey, []GatewayEnv{good}, cascade.KindInvalidInput},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildDriverEnv(claudeRow, c.auth, testProfile(), "job-1", "ptok", "lease-1", sixProxy(), gwHostile, c.gw...)
			if !cascade.HasKind(err, c.kind) || strings.Contains(err.Error(), canary) {
				t.Fatalf("err = %v, want kind %v without the value", err, c.kind)
			}
		})
	}
}

func TestInheritedGatewayEnvNeverReachesOtherSpawns(t *testing.T) {
	isolateHome(t)
	canary := gwCanary(t)
	for _, name := range []string{gwAuthName, gwBaseName, gwManagedName} {
		t.Setenv(name, canary+"-"+strings.ToLower(name))
	}
	t.Setenv("TEST_INHERIT_SOURCE", "fake-spawn-key")
	for name, c := range map[string]struct {
		b Binding
		a DriverAuth
	}{
		"codex":         {codexRow, DriverAuth{Harness: "codex", Class: CredentialAPIKey, KeyEnv: "TEST_INHERIT_SOURCE"}},
		"opencode":      {fakeBinding("opencode"), DriverAuth{Harness: "opencode", Class: CredentialAPIKey, KeyEnv: "TEST_INHERIT_SOURCE"}},
		"claude apikey": {claudeRow, DriverAuth{Harness: HarnessClaude, Class: CredentialAPIKey, KeyEnv: "TEST_INHERIT_SOURCE"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := BuildDriverEnv(c.b, c.a, testProfile(), "job-1", "ptok", "lease-1", sixProxy(), os.Getenv)
			if err != nil {
				t.Fatalf("BuildDriverEnv: %v", err)
			}
			for _, kv := range append(slices.Clone(got.Driver), got.Tool...) {
				name, _, _ := strings.Cut(kv, "=")
				if strings.Contains(kv, canary) || name == gwAuthName || name == gwBaseName || name == gwManagedName {
					t.Fatalf("a host-injected variable reached the spawn: %s", name)
				}
			}
		})
	}
}

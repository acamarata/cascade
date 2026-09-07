package intake

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fixedClock is a deterministic Clock.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// spyCustody is an in-memory secrets.Custody that records every Set/Get
// call, so a test can prove credential storage was DELEGATED to the vault
// broker rather than handled ad hoc.
type spyCustody struct {
	mu      sync.Mutex
	values  map[string][]byte
	setCall []string
	getCall []string
}

func newSpyCustody() *spyCustody { return &spyCustody{values: map[string][]byte{}} }

func (c *spyCustody) Name() string    { return "spy" }
func (c *spyCustody) Available() bool { return true }
func (c *spyCustody) Set(_ context.Context, name string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCall = append(c.setCall, name)
	c.values[name] = append([]byte(nil), value...)
	return nil
}
func (c *spyCustody) Get(_ context.Context, name string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCall = append(c.getCall, name)
	v, ok := c.values[name]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "spy: no value for %q", name)
	}
	return v, nil
}
func (c *spyCustody) Delete(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, name)
	return nil
}
func (c *spyCustody) List(_ context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for k := range c.values {
		out = append(out, k)
	}
	return out, nil
}

// allowGate authorises every elevated verb - fine for a test that is not
// exercising the elevation gate itself.
type allowGate struct{}

func (allowGate) Authorize(context.Context, string) error { return nil }

// testDeps builds a full Deps over a fresh spyCustody, a real vault broker
// and egress engine, a fresh MemoryRegistry, and a fixed clock. Doer is
// supplied per test.
func testDeps(t *testing.T, doer Doer) (Deps, *spyCustody) {
	t.Helper()
	custody := newSpyCustody()
	broker, err := secrets.NewBroker(custody, allowGate{})
	if err != nil {
		t.Fatalf("building broker: %v", err)
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("building egress vault: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("building detector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	if err != nil {
		t.Fatalf("building engine: %v", err)
	}
	return Deps{
		Doer:     doer,
		Clock:    fixedClock{now: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)},
		Vault:    broker,
		Egress:   engine,
		Registry: NewMemoryRegistry(),
		NewOAuthBroker: func(cfg provider.ProviderOAuthConfig, oa secrets.OAuthDeps) (provider.OAuthBroker, error) {
			return secrets.NewOAuthBroker(cfg, oa)
		},
		Getenv: func(string) string { return "" },
	}, custody
}

func anthropicSuccessDoer(t *testing.T) *fakeDoer {
	t.Helper()
	return &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/models":   {Status: 200, Body: loadFixture(t, "probe_anthropic.golden.json")},
		"https://api.anthropic.com/v1/messages": {Status: 200, Body: []byte(`{"content":[{"type":"text","text":"hi"}]}`)},
	}}
}

func TestProviderAddKeyPath(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	req := AddRequest{Name: "myclaude", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value")}
	result, err := Add(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if result.Status != "converged" {
		t.Fatalf("expected status converged, got %q", result.Status)
	}
	if result.Record.Driver != DriverAnthropic {
		t.Fatalf("expected driver anthropic, got %s", result.Record.Driver)
	}
	if result.Record.AuthRef == "" || string(result.Record.AuthRef) == "sk-ant-test-value" {
		t.Fatalf("AuthRef must be a vault-key ref, never the credential value; got %q", result.Record.AuthRef)
	}
	// The credential value must never appear anywhere in the result, its
	// JSON-ish string form, or the names the custody backend recorded.
	dump := result.Status + result.Record.Name + string(result.Record.AuthRef) + strings.Join(result.Record.KnownModels, ",")
	if strings.Contains(dump, "sk-ant-test-value") {
		t.Fatal("the raw credential value leaked into the AddResult")
	}
	for _, name := range append(append([]string{}, custody.setCall...), custody.getCall...) {
		if strings.Contains(name, "sk-ant-test-value") {
			t.Fatalf("the raw credential value leaked into a vault-key NAME: %q", name)
		}
	}
	if len(custody.setCall) == 0 {
		t.Fatal("expected the key to be stored through custody.Set (vault delegation)")
	}
}

func TestProviderAddKeyPathCredentialNeverInError(t *testing.T) {
	deps, _ := testDeps(t, &fakeDoer{}) // every probe 404s
	req := AddRequest{Name: "myclaude", Credential: CredentialKey, KeyValue: []byte("sk-ant-should-not-leak")}
	_, err := Add(context.Background(), deps, req)
	if err == nil {
		t.Fatal("expected shape-probe to fail against an all-404 doer")
	}
	if strings.Contains(err.Error(), "sk-ant-should-not-leak") {
		t.Fatalf("the credential value leaked into the error message: %q", err.Error())
	}
}

func TestProviderAddIdempotent(t *testing.T) {
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	req := AddRequest{Name: "myclaude", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value")}

	first, err := Add(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if first.Status != "converged" {
		t.Fatalf("expected first add to converge, got %q", first.Status)
	}

	second, err := Add(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if second.Status != "updated" {
		t.Fatalf("expected the re-add to report updated, got %q", second.Status)
	}
	if second.Record.CreatedAt != first.Record.CreatedAt {
		t.Fatal("re-add must preserve the original CreatedAt, not create a duplicate")
	}

	all, err := deps.Registry.(*MemoryRegistry).ListPool(context.Background(), "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	count := 0
	for _, r := range all {
		if r.Name == "myclaude" {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("expected at most one record named myclaude, found %d", count)
	}
}

func TestProviderAddNoVerifyRecordsWarning(t *testing.T) {
	// Only the /v1/models leg is stubbed: if micro-verify ran anyway it
	// would 404 and Add would fail, so a passing test proves the skip.
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/models": {Status: 200, Body: loadFixture(t, "probe_anthropic.golden.json")},
	}}
	deps, _ := testDeps(t, doer)
	req := AddRequest{Name: "myclaude", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value"), NoVerify: true}
	result, err := Add(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected a warning recording the --no-verify skip")
	}
	if !result.Record.VerifySkipped {
		t.Fatal("expected VerifySkipped to be recorded on the record")
	}
}

func TestOAuthCASCADE_NO_INPUT(t *testing.T) {
	deps, _ := testDeps(t, &fakeDoer{})
	deps.Getenv = func(name string) string {
		if name == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	req := AddRequest{Name: "anthropic", Credential: CredentialOAuth}
	_, err := Add(context.Background(), deps, req)
	if err == nil {
		t.Fatal("expected CASCADE_NO_INPUT=1 to refuse the OAuth path")
	}
	msg := err.Error()
	if !strings.Contains(msg, "--key") || !strings.Contains(msg, "--key-env") {
		t.Errorf("expected the refusal to cite --key and --key-env, got %q", msg)
	}
}

func TestOAuthUnknownProviderNameIsRefused(t *testing.T) {
	deps, _ := testDeps(t, &fakeDoer{})
	req := AddRequest{Name: "some-random-vendor", Credential: CredentialOAuth}
	if _, err := Add(context.Background(), deps, req); err == nil {
		t.Fatal("expected a refusal for an OAuth name outside the built-in anthropic/gemini set")
	}
}

func TestNonInteractiveDirFromInitConfig(t *testing.T) {
	doc := `
[[providers]]
name = "myclaude"
kind = "anthropic"
auth = "key"
key_env = "TEST_INTAKE_KEY_ENV"
`
	reqs, err := ParseInitConfig([]byte(doc))
	if err != nil {
		t.Fatalf("ParseInitConfig: %v", err)
	}
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	deps.Getenv = func(name string) string {
		if name == "TEST_INTAKE_KEY_ENV" {
			return "sk-ant-test-value"
		}
		return ""
	}
	result, err := Add(context.Background(), deps, reqs[0])
	if err != nil {
		t.Fatalf("Add via non-interactive directive: %v", err)
	}
	if result.Record.Auth != AuthKey {
		t.Fatalf("expected AuthKey, got %s", result.Record.Auth)
	}
}

func TestKeychainDelegation(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	req := AddRequest{Name: "myclaude", Credential: CredentialKeyEnv}
	deps.Getenv = func(string) string { return "sk-ant-test-value" }
	req.KeyEnvVar = "ANY_ENV_VAR"
	if _, err := Add(context.Background(), deps, req); err != nil {
		t.Fatalf("Add: %v", err)
	}
	found := false
	for _, name := range custody.setCall {
		if strings.HasPrefix(name, "provider.myclaude.") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the credential to be stored through the vault broker's custody backend under a provider.<name>.* key, proving delegation rather than an ad hoc store")
	}
}

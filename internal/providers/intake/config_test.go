package intake

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestAddRequestValidateRequiresName(t *testing.T) {
	req := AddRequest{Credential: CredentialKeyEnv, KeyEnvVar: "X"}
	if err := req.Validate(); err == nil {
		t.Fatal("expected an error for an empty provider name")
	}
}

func TestAddRequestValidateRequiresCredential(t *testing.T) {
	req := AddRequest{Name: "p1"}
	if err := req.Validate(); err == nil {
		t.Fatal("expected an error for CredentialUnset")
	}
}

func TestNoInputHelper(t *testing.T) {
	if !noInput(func(string) string { return "1" }) {
		t.Error("expected noInput true when CASCADE_NO_INPUT=1")
	}
	if noInput(func(string) string { return "" }) {
		t.Error("expected noInput false when unset")
	}
	if noInput(nil) {
		t.Error("expected noInput false for a nil getenv")
	}
}

func TestLooksLikeSecretDetectsShapedLiteral(t *testing.T) {
	// A split literal (AGENT-BRIEF's credential-shaped-fixture rule): no
	// contiguous match exists in this source file, but the runtime value
	// concatenates to a real AWS-access-key SHAPE.
	shaped := "AKIA" + "7YQ2XPLM4RZV6WTB"
	if !looksLikeSecret(shaped) {
		t.Errorf("expected %q to be detected as secret-shaped", shaped)
	}
}

func TestLooksLikeSecretAllowsAnOrdinaryEnvVarName(t *testing.T) {
	if looksLikeSecret("ANTHROPIC_API_KEY") {
		t.Error("an env-var NAME must not itself be flagged as a secret literal")
	}
	if looksLikeSecret("") {
		t.Error("an empty string must never be flagged")
	}
}

func TestParseInitConfigKeyEnvPath(t *testing.T) {
	doc := `
[[providers]]
name = "myclaude"
kind = "anthropic"
auth = "key"
key_env = "ANTHROPIC_API_KEY"
verify = false
`
	reqs, err := ParseInitConfig([]byte(doc))
	if err != nil {
		t.Fatalf("ParseInitConfig: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Name != "myclaude" || r.Credential != CredentialKeyEnv || r.KeyEnvVar != "ANTHROPIC_API_KEY" || !r.NoVerify {
		t.Fatalf("unexpected request: %+v", r)
	}
}

func TestParseInitConfigOAuthPath(t *testing.T) {
	doc := `
[[providers]]
name = "gemini"
kind = "gemini"
auth = "oauth"
`
	reqs, err := ParseInitConfig([]byte(doc))
	if err != nil {
		t.Fatalf("ParseInitConfig: %v", err)
	}
	if reqs[0].Credential != CredentialOAuth {
		t.Fatalf("expected CredentialOAuth, got %v", reqs[0].Credential)
	}
}

func TestParseInitConfigRejectsUnknownKind(t *testing.T) {
	doc := `
[[providers]]
name = "bad"
kind = "made-up-kind"
key_env = "X"
`
	if _, err := ParseInitConfig([]byte(doc)); err == nil {
		t.Fatal("expected a refusal for an unrecognised kind")
	}
}

func TestParseInitConfigRejectsSecretLiteralInKeyEnv(t *testing.T) {
	shaped := "AKIA" + "7YQ2XPLM4RZV6WTB"
	doc := `
[[providers]]
name = "bad"
kind = "anthropic"
key_env = "` + shaped + `"
`
	_, err := ParseInitConfig([]byte(doc))
	if err == nil {
		t.Fatal("expected a refusal for a secret-shaped key_env value")
	}
	if !strings.Contains(err.Error(), "cascade vault set") {
		t.Errorf("expected the refusal to cite `cascade vault set`, got %q", err.Error())
	}
}

func TestParseInitConfigRejectsMissingKeyEnvForKeyAuth(t *testing.T) {
	doc := `
[[providers]]
name = "bad"
kind = "anthropic"
`
	if _, err := ParseInitConfig([]byte(doc)); err == nil {
		t.Fatal("expected a refusal when key auth has no key_env")
	}
}

func TestParseInitConfigRejectsMalformedTOML(t *testing.T) {
	if _, err := ParseInitConfig([]byte("this is not [ toml")); err == nil {
		t.Fatal("expected a refusal for unparseable TOML")
	}
}

func TestParseInitConfigRejectsMissingName(t *testing.T) {
	doc := `
[[providers]]
kind = "anthropic"
key_env = "X"
`
	if _, err := ParseInitConfig([]byte(doc)); err == nil {
		t.Fatal("expected a refusal for a missing name")
	}
}

func TestParseInitConfigRejectsUnknownAuthMode(t *testing.T) {
	doc := `
[[providers]]
name = "bad"
kind = "anthropic"
auth = "bearer-token-thing"
`
	if _, err := ParseInitConfig([]byte(doc)); err == nil {
		t.Fatal("expected a refusal for an unrecognised auth mode")
	}
}

func TestParseInitConfigWholeBatchFailsClosed(t *testing.T) {
	doc := `
[[providers]]
name = "good"
kind = "anthropic"
key_env = "ANTHROPIC_API_KEY"

[[providers]]
name = "bad"
kind = "not-a-kind"
`
	if _, err := ParseInitConfig([]byte(doc)); err == nil {
		t.Fatal("expected the whole batch to fail closed when any directive is unparseable")
	}
}

func TestProviderAddPoolJoinRebalancesIndex(t *testing.T) {
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	first, err := Add(context.Background(), deps, AddRequest{Name: "a", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value"), Pool: "gf"})
	if err != nil {
		t.Fatalf("first join: %v", err)
	}
	second, err := Add(context.Background(), deps, AddRequest{Name: "b", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value"), Pool: "gf"})
	if err != nil {
		t.Fatalf("second join: %v", err)
	}
	if first.Record.PoolIndex != 0 || second.Record.PoolIndex != 1 {
		t.Fatalf("expected pool indices 0 then 1, got %d then %d", first.Record.PoolIndex, second.Record.PoolIndex)
	}
	members, err := deps.Registry.ListPool(context.Background(), "gf")
	if err != nil || len(members) != 2 {
		t.Fatalf("expected 2 pool members, got %d (err %v)", len(members), err)
	}
}

// fakeOAuthBroker deterministically satisfies provider.OAuthBroker without
// any network - a test double for the real internal/secrets.OAuthBroker,
// which this test never constructs.
type fakeOAuthBroker struct{ rec provider.TokenRecord }

func (f fakeOAuthBroker) Start(context.Context) (provider.TokenRecord, error) { return f.rec, nil }
func (f fakeOAuthBroker) Refresh(context.Context, string) (provider.TokenRecord, error) {
	return f.rec, nil
}
func (f fakeOAuthBroker) Revoke(context.Context, string) error { return nil }

func TestProviderAddOAuthSuccessPath(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	const accessRef = "provider.anthropic.oauth.access"
	if err := custody.Set(context.Background(), accessRef, []byte("sk-ant-test-value")); err != nil {
		t.Fatalf("seeding the access token: %v", err)
	}
	deps.NewOAuthBroker = func(provider.ProviderOAuthConfig, secrets.OAuthDeps) (provider.OAuthBroker, error) {
		return fakeOAuthBroker{rec: provider.TokenRecord{Provider: "anthropic", AccessRef: accessRef}}, nil
	}
	result, err := Add(context.Background(), deps, AddRequest{Name: "anthropic", Credential: CredentialOAuth})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if result.Record.Auth != AuthOAuth || result.Record.Driver != DriverAnthropic {
		t.Fatalf("unexpected record: %+v", result.Record)
	}
	if string(result.Record.AuthRef) != accessRef {
		t.Fatalf("expected AuthRef %q, got %q", accessRef, result.Record.AuthRef)
	}
}

func TestAddRequestValidateRejectsUnknownCredentialMode(t *testing.T) {
	req := AddRequest{Name: "p1", Credential: CredentialMode(99)}
	if err := req.Validate(); err == nil {
		t.Fatal("expected a refusal for an unrecognised credential mode")
	}
}

func TestResolveKeyEnvEmptyValueRefuses(t *testing.T) {
	deps := Deps{Getenv: func(string) string { return "" }}
	req := AddRequest{Name: "p1", KeyEnvVar: "UNSET_VAR"}
	if _, _, _, _, err := resolveKeyEnv(context.Background(), deps, req); err == nil {
		t.Fatal("expected a refusal for an empty environment variable")
	}
}

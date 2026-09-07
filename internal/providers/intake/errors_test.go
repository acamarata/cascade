package intake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestErrShapeProbeFailedNamesEveryAttempt(t *testing.T) {
	err := errShapeProbeFailed([]probeAttempt{
		{kind: DriverAnthropic, endpoint: "https://api.anthropic.com/v1/models", status: 401},
		{kind: DriverOpenAICompat, endpoint: "https://api.openai.com/v1/models", status: 403},
		{kind: DriverGemini, endpoint: "https://generativelanguage.googleapis.com/v1beta/models", err: errors.New("dial tcp: timeout")},
	})
	msg := err.Error()
	for _, want := range []string{"anthropic", "openai-compat", "gemini", "401", "403", "timeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not mention %q", msg, want)
		}
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("expected KindInvalidInput, got %v", err)
	}
	if !errors.Is(err, ErrShapeProbeFailed) {
		t.Error("expected errors.Is to match ErrShapeProbeFailed")
	}
}

func TestErrOAuthNoInputCitesAlternatives(t *testing.T) {
	err := errOAuthNoInput(errors.New("cause"))
	msg := err.Error()
	if !strings.Contains(msg, "--key") || !strings.Contains(msg, "--key-env") {
		t.Errorf("OAuth CASCADE_NO_INPUT error must cite --key and --key-env, got %q", msg)
	}
}

func TestErrSecretLiteralInKeyEnvCitesVaultSet(t *testing.T) {
	err := errSecretLiteralInKeyEnv("myclaude")
	msg := err.Error()
	if !strings.Contains(msg, "cascade vault set") {
		t.Errorf("expected the error to cite `cascade vault set`, got %q", msg)
	}
	if !errors.Is(err, ErrSecretLiteral) {
		t.Error("expected errors.Is to match ErrSecretLiteral")
	}
}

func TestErrMicroVerifyFailedIsUnavailable(t *testing.T) {
	err := errMicroVerifyFailed("claude-3-5-sonnet-20241022", 401, "the credential's scope looks insufficient")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("expected KindUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected the status to appear in the message, got %q", err.Error())
	}
}

func TestEveryConstructorUsesTaxonomyKind(t *testing.T) {
	for name, err := range map[string]error{
		"errNoCredentialSource":        errNoCredentialSource(),
		"errAmbiguousCredentialSource": errAmbiguousCredentialSource(),
		"errEmptyProviderName":         errEmptyProviderName(),
		"errUnparseableInitConfig":     errUnparseableInitConfig("reason"),
		"errEmptyKeyEnvValue":          errEmptyKeyEnvValue("VAR"),
		"errEgressClassRefused(nil)":   errEgressClassRefused(nil),
	} {
		var cerr *cascade.Error
		if !errors.As(err, &cerr) {
			t.Errorf("%s did not produce a *cascade.Error: %v", name, err)
			continue
		}
		if !cerr.Kind.Valid() {
			t.Errorf("%s produced an invalid taxonomy kind", name)
		}
	}
}

func TestErrEgressClassRefusedPreservesCauseKind(t *testing.T) {
	cause := cascade.New(cascade.KindPolicyDenied, "disabled")
	if got := errEgressClassRefused(cause); got != cause {
		t.Errorf("expected the original cause to pass through unwrapped, got %v", got)
	}
}

func TestMicroVerifyGuidanceMapsStatusToActionableText(t *testing.T) {
	cases := map[int]string{
		401: "scope",
		403: "scope",
		402: "quota",
		429: "quota",
		500: "rejected",
	}
	for status, want := range cases {
		got := microVerifyGuidance(status)
		if !strings.Contains(got, want) {
			t.Errorf("status %d: guidance %q does not mention %q", status, got, want)
		}
	}
}

func TestFirstOrEmpty(t *testing.T) {
	if got := firstOrEmpty(nil); got != "" {
		t.Errorf("expected empty string for nil, got %q", got)
	}
	if got := firstOrEmpty([]string{"a", "b"}); got != "a" {
		t.Errorf("expected the first element, got %q", got)
	}
}

func TestResolveCredentialUnknownModeRefuses(t *testing.T) {
	_, _, _, _, err := resolveCredential(context.Background(), Deps{}, AddRequest{Name: "p1", Credential: CredentialMode(99)})
	if err == nil {
		t.Fatal("expected a refusal for an unrecognised credential mode")
	}
}

func TestMicroVerifyEmptyModelRefuses(t *testing.T) {
	if err := microVerify(context.Background(), Deps{}, DriverAnthropic, "", "", ""); err == nil {
		t.Fatal("expected a refusal when no model was enumerated")
	}
}

func TestMicroVerifyNonOKStatusFails(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/messages": {Status: 401},
	}}
	deps := Deps{Doer: doer, Egress: engine}
	err := microVerify(context.Background(), deps, DriverAnthropic, "https://api.anthropic.com", "sk-test", "claude-3-5-sonnet-20241022")
	if err == nil {
		t.Fatal("expected a refusal for a non-200 verify response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected the status to appear in the message, got %q", err.Error())
	}
}

func TestMicroVerifyTransportErrorFails(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{err: map[string]error{
		"https://api.anthropic.com/v1/messages": errors.New("dial tcp: refused"),
	}}
	deps := Deps{Doer: doer, Egress: engine}
	if err := microVerify(context.Background(), deps, DriverAnthropic, "https://api.anthropic.com", "sk-test", "claude-3-5-sonnet-20241022"); err == nil {
		t.Fatal("expected a refusal when the transport call fails")
	}
}

func TestBuiltinOAuthConfigGemini(t *testing.T) {
	cfg, kind, err := builtinOAuthConfig("my-gemini-account")
	if err != nil {
		t.Fatalf("builtinOAuthConfig: %v", err)
	}
	if kind != DriverGemini || cfg.ProviderID != "gemini" {
		t.Fatalf("unexpected result: kind=%s cfg=%+v", kind, cfg)
	}
	if verr := cfg.Validate(); verr != nil {
		t.Errorf("the built-in gemini config must itself be Validate()-clean: %v", verr)
	}
}

func TestBuiltinOAuthConfigAnthropicIsValid(t *testing.T) {
	cfg, _, err := builtinOAuthConfig("anthropic")
	if err != nil {
		t.Fatalf("builtinOAuthConfig: %v", err)
	}
	if verr := cfg.Validate(); verr != nil {
		t.Errorf("the built-in anthropic config must itself be Validate()-clean: %v", verr)
	}
}

func TestProbeAndEnumerateDriverHintOverride(t *testing.T) {
	// The probed shape (anthropic, from the stubbed /v1/models response)
	// disagrees with driverHint (gemini): the OAuth caller's own resolved
	// driver kind wins, per resolveOAuth's contract.
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/models": {Status: 200, Body: loadFixture(t, "probe_anthropic.golden.json")},
	}}
	deps := Deps{Doer: doer, Egress: engine}
	kind, _, _, err := probeAndEnumerate(context.Background(), deps, "sk-test", "", DriverGemini)
	if err != nil {
		t.Fatalf("probeAndEnumerate: %v", err)
	}
	if kind != DriverGemini {
		t.Fatalf("expected driverHint to override the probed kind, got %s", kind)
	}
}

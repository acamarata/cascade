package intake

// Purpose: proves the flagless reauth form (P1-WID-01) and the NO_INPUT
//   guards fail closed. Each test names the input a weak implementation
//   would accept: a key record that falls back to reading a key, an empty
//   or unknown Auth that defaults to some mode, and a broker that ignores
//   CASCADE_NO_INPUT=1.
// Inputs: testDeps/seedReauthRecord/assertUnchanged (core_test.go,
//   reauth_test.go); a counting OAuth broker and a write-recording Registry.
// Outputs: none.
// Constraints: MemoryRegistry + spyCustody only; no network, no keychain,
//   no process HOME. Refusals are compared by sentinel identity AND message.
// SPORT: cli.provider.reauth/ADD (P1-WID-01).

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// countingBroker is a permissive OAuth broker: it never checks
// CASCADE_NO_INPUT, so only Reauth's own guards can refuse.
type countingBroker struct {
	starts *int
	ref    string
}

func (b countingBroker) Start(context.Context) (provider.TokenRecord, error) {
	*b.starts++
	return provider.TokenRecord{Provider: "anthropic", AccessRef: b.ref}, nil
}
func (b countingBroker) Refresh(context.Context, string) (provider.TokenRecord, error) {
	return provider.TokenRecord{}, errors.New("unused")
}
func (b countingBroker) Revoke(context.Context, string) error { return nil }

// writeRecorder wraps a Registry and records every UpsertProvider.
type writeRecorder struct {
	Registry
	upserts []ProviderRecord
}

func (w *writeRecorder) UpsertProvider(ctx context.Context, rec ProviderRecord) error {
	w.upserts = append(w.upserts, rec)
	return w.Registry.UpsertProvider(ctx, rec)
}

// oauthSeed seeds an anthropic record with the given Auth, a new access
// token under newRef, and a counting broker; it returns the seed.
func oauthSeed(t *testing.T, deps *Deps, custody *spyCustody, auth AuthType, starts *int) ProviderRecord {
	t.Helper()
	seed := seedReauthRecord(t, deps, "anthropic-main", DriverAnthropic)
	seed.Auth = auth
	if err := deps.Registry.UpsertProvider(context.Background(), seed); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	if err := custody.Set(context.Background(), "oauth.anthropic.access.2", []byte("sk-ant-"+"oat01-new")); err != nil {
		t.Fatalf("seed access token: %v", err)
	}
	deps.NewOAuthBroker = func(provider.ProviderOAuthConfig, secrets.OAuthDeps) (provider.OAuthBroker, error) {
		return countingBroker{starts: starts, ref: "oauth.anthropic.access.2"}, nil
	}
	return seed
}

// assertRefused checks kind, sentinel identity and message fragments.
func assertRefused(t *testing.T, err error, kind cascade.Kind, sentinel error, frags ...string) {
	t.Helper()
	if !cascade.HasKind(err, kind) || !errors.Is(err, sentinel) {
		t.Fatalf("want %v wrapping %q, got %v", kind, sentinel, err)
	}
	for _, f := range frags {
		if !strings.Contains(err.Error(), f) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), f)
		}
	}
}

func TestReauthFlaglessOAuthRecordRunsBroker(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	starts := 0
	seed := oauthSeed(t, &deps, custody, AuthOAuth, &starts)
	res, err := Reauth(context.Background(), deps, ReauthRequest{Name: "anthropic-main"})
	if err != nil || starts != 1 {
		t.Fatalf("flagless on an oauth record: err %v, broker starts %d (want nil, 1)", err, starts)
	}
	if res.Record.AuthRef != "oauth.anthropic.access.2" || res.Record.Auth != AuthOAuth || res.Record.Pool != seed.Pool {
		t.Fatalf("record not repointed at the new grant: %+v", res.Record)
	}
}

func TestReauthFlaglessKeyRecordRefused(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	starts := 0
	seed := oauthSeed(t, &deps, custody, AuthKey, &starts)
	sets := len(custody.setCall)
	// A weak implementation falls back to --key or --key-env here; KeyValue
	// and KeyEnvVar are set so such a fallback would have a value to write.
	deps.Getenv = func(string) string { return "sk-ant-" + "api03-fallback" }
	_, err := Reauth(context.Background(), deps, ReauthRequest{Name: "anthropic-main", KeyValue: []byte("x"), KeyEnvVar: "K"})
	assertRefused(t, err, cascade.KindInvalidInput, ErrFlaglessRefused, `"anthropic-main"`, "--key-env", "never reads a key")
	if starts != 0 || len(custody.setCall) != sets {
		t.Fatalf("refused flagless ran the broker (%d) or wrote the vault (%d sets)", starts, len(custody.setCall)-sets)
	}
	assertUnchanged(t, deps, seed)
}

func TestReauthFlaglessUnknownAuthFailsClosed(t *testing.T) {
	for _, auth := range []AuthType{"", "bogus"} {
		deps, custody := testDeps(t, anthropicSuccessDoer(t))
		starts := 0
		seed := oauthSeed(t, &deps, custody, auth, &starts)
		_, err := Reauth(context.Background(), deps, ReauthRequest{Name: "anthropic-main"})
		assertRefused(t, err, cascade.KindInvalidInput, ErrFlaglessRefused, "no recognised auth type")
		if starts != 0 {
			t.Fatalf("Auth %q: the broker ran on an unrecognised record", auth)
		}
		assertUnchanged(t, deps, seed)
	}
}

func TestReauthFlaglessRefusedUnderNoInput(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	starts := 0
	seed := oauthSeed(t, &deps, custody, AuthOAuth, &starts)
	deps.Getenv = func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
	_, err := Reauth(context.Background(), deps, ReauthRequest{Name: "anthropic-main"})
	assertRefused(t, err, cascade.KindUnavailable, ErrNoInputInteractive, "CASCADE_NO_INPUT=1", "--key-env")
	if starts != 0 {
		t.Fatal("CASCADE_NO_INPUT=1 flagless still started the OAuth broker")
	}
	assertUnchanged(t, deps, seed)
}

func TestReauthOAuthRefusedUnderNoInputBeforeBroker(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	starts := 0
	seed := oauthSeed(t, &deps, custody, AuthKey, &starts)
	deps.Getenv = func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
	_, err := Reauth(context.Background(), deps, ReauthRequest{Name: "anthropic-main", Credential: CredentialOAuth})
	assertRefused(t, err, cascade.KindUnavailable, ErrNoInputInteractive, "--key", "--key-env")
	if starts != 0 {
		t.Fatal("a broker that ignores CASCADE_NO_INPUT was started")
	}
	assertUnchanged(t, deps, seed)
}

func TestReauthInferModeRefusesBeforeReading(t *testing.T) {
	ctx := context.Background()
	if _, err := InferReauthMode(ctx, nil, nil, " "); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := InferReauthMode(ctx, nil, nil, "p"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil registry: %v", err)
	}
	_, err := InferReauthMode(ctx, NewMemoryRegistry(), nil, "never-added")
	assertRefused(t, err, cascade.KindNotFound, ErrUnknownProvider, "run `cascade provider add` first")
}

// failingRegistry fails the named registry call with a fixed error.
type failingRegistry struct {
	Registry
	getErr, upsertErr error
}

func (f failingRegistry) GetProvider(ctx context.Context, name string) (ProviderRecord, error) {
	if f.getErr != nil {
		return ProviderRecord{}, f.getErr
	}
	return f.Registry.GetProvider(ctx, name)
}

func (f failingRegistry) UpsertProvider(ctx context.Context, rec ProviderRecord) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	return f.Registry.UpsertProvider(ctx, rec)
}

// assertMessage fails unless err is a cascade error of kind whose message
// is exactly msg.
func assertMessage(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	if !cascade.HasKind(err, kind) || err.Error() != fmt.Sprintf("%v: %s", kind, msg) {
		t.Fatalf("want %v %q, got %v", kind, msg, err)
	}
}

// TestReauthRefusalsAndStoreFailures covers the paths that leave the record
// and vault value untouched: a registry read or write that fails for a
// reason other than not-found, an empty --key-env value, and --oauth for a
// provider name no built-in client serves.
func TestReauthRefusalsAndStoreFailures(t *testing.T) {
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	seed := seedReauthRecord(t, &deps, "myclaude", DriverAnthropic)
	mem := deps.Registry
	boom := cascade.New(cascade.KindUnavailable, "registry offline")
	req := ReauthRequest{Name: "myclaude", Credential: CredentialKeyEnv, KeyEnvVar: "NEW_KEY"}
	deps.Getenv = func(k string) string { return map[string]string{"NEW_KEY": "sk-ant-api03-new-value"}[k] }

	deps.Registry = failingRegistry{Registry: mem, getErr: boom}
	_, err := Reauth(context.Background(), deps, req)
	assertMessage(t, err, cascade.KindUnavailable, "registry offline")

	deps.Registry = mem
	deps.Getenv = func(string) string { return "  " }
	_, err = Reauth(context.Background(), deps, req)
	assertMessage(t, err, cascade.KindInvalidInput,
		`intake: environment variable "NEW_KEY" named by --key-env is unset or empty`)
	assertUnchanged(t, deps, seed)

	other := seedReauthRecord(t, &deps, "plain", DriverAnthropic)
	_, err = Reauth(context.Background(), deps, ReauthRequest{Name: "plain", Credential: CredentialOAuth})
	assertMessage(t, err, cascade.KindUnsupported,
		`intake: --oauth is supported only for a provider name containing "anthropic" or "gemini" in this build; use --key or --key-env for "plain"`)
	got, gerr := deps.Registry.GetProvider(context.Background(), "plain")
	if gerr != nil || got.AuthRef != other.AuthRef || !got.UpdatedAt.Equal(other.UpdatedAt) {
		t.Fatalf("refused oauth changed the record: %+v, %v", got, gerr)
	}

	// The vault write precedes the registry write, so only the record is
	// asserted unchanged after a failed upsert.
	deps.Getenv = func(k string) string { return map[string]string{"NEW_KEY": "sk-ant-api03-new-value"}[k] }
	deps.Registry = failingRegistry{Registry: mem, upsertErr: boom}
	_, err = Reauth(context.Background(), deps, req)
	assertMessage(t, err, cascade.KindUnavailable, "registry offline")
	if got, gerr := mem.GetProvider(context.Background(), "myclaude"); gerr != nil || !reflect.DeepEqual(got, seed) {
		t.Fatalf("failed upsert changed the record: %+v, %v", got, gerr)
	}
}

// TestReauthNoVerifyAndBaseURL covers --no-verify (no probe, a warning, the
// record flagged) and a record with its own base URL, which the re-verify
// must call instead of the driver default.
func TestReauthNoVerifyAndBaseURL(t *testing.T) {
	deps, _ := testDeps(t, &fakeDoer{})
	seedReauthRecord(t, &deps, "myclaude", DriverAnthropic)
	deps.Getenv = func(string) string { return "sk-ant-api03-new-value" }
	res, err := Reauth(context.Background(), deps, ReauthRequest{
		Name: "myclaude", Credential: CredentialKeyEnv, KeyEnvVar: "NEW_KEY", NoVerify: true})
	if err != nil || !res.Record.VerifySkipped ||
		len(res.Warnings) != 1 || res.Warnings[0] != "micro-verify skipped: --no-verify set" {
		t.Fatalf("--no-verify: %+v, %v; want VerifySkipped and the one skip warning", res, err)
	}
	if v := vaultValue(t, deps, keyRefFor("myclaude", "key")); v != "sk-ant-api03-new-value" {
		t.Fatalf("--no-verify did not store the new value")
	}

	deps, _ = testDeps(t, &fakeDoer{responses: map[string]HTTPResponse{
		"https://proxy.example.test/v1/messages": {Status: 200, Body: []byte(`{"content":[{"type":"text","text":"hi"}]}`)},
	}})
	rec := seedReauthRecord(t, &deps, "viaproxy", DriverAnthropic)
	rec.BaseURL = "https://proxy.example.test"
	if err := deps.Registry.UpsertProvider(context.Background(), rec); err != nil {
		t.Fatalf("seed base url: %v", err)
	}
	deps.Getenv = func(string) string { return "sk-ant-api03-new-value" }
	res, err = Reauth(context.Background(), deps, ReauthRequest{Name: "viaproxy", Credential: CredentialKeyEnv, KeyEnvVar: "K"})
	if err != nil || res.Record.BaseURL != "https://proxy.example.test" || res.Record.VerifySkipped {
		t.Fatalf("reauth over a custom base URL: %+v, %v", res.Record, err)
	}
}

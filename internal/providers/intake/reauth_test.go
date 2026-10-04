package intake

// Purpose: proves Reauth's contract (P1-E38-W8-S123-T1): an unknown name is
//   a typed add-first refusal; a successful reauth replaces the credential
//   and AuthRef IN PLACE with every other field unchanged; a failed
//   micro-verify changes neither the record nor the stored vault value; an
//   empty --key and an OAuth driver-family mismatch are refused before
//   anything is written; a key->oauth change repoints AuthRef at the broker
//   AccessRef; and reauth never adds a second record.
// Inputs: testDeps/fakeDoer/anthropicSuccessDoer/fakeOAuthBroker, shared
//   with core_test.go, transport_test.go and config_test.go.
// Outputs: none.
// Constraints: in-memory MemoryRegistry + spyCustody only; no network, no
//   real keychain, no process HOME read. Every "unchanged" assertion
//   compares the WHOLE record (reflect.DeepEqual) under a ticking clock, so
//   any write -- even one that only bumps UpdatedAt -- is visible.
// SPORT: cli.provider.reauth/ADD (P1-E38-W8-S123-T1).

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// tickingClock advances one second on every Now call.
type tickingClock struct{ now time.Time }

func (c *tickingClock) Now() time.Time { c.now = c.now.Add(time.Second); return c.now }

const oldKeyValue = "sk-ant-api03-old-value-for-reauth-tests"

// seedReauthRecord stores a fully populated record plus its vault value and
// switches deps to a ticking clock, returning the seeded record.
func seedReauthRecord(t *testing.T, deps *Deps, name string, driver DriverKind) ProviderRecord {
	t.Helper()
	ctx := context.Background()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rec := ProviderRecord{
		Name: name, Driver: driver, BaseURL: "", Auth: AuthKey, AuthRef: keyRefFor(name, "key"),
		KnownModels:          []string{"claude-3-5-sonnet-20241022", "claude-3-haiku"},
		Capabilities:         provider.Capabilities{Vision: provider.CapabilitySupported},
		CapabilitiesProbedAt: created.Add(time.Minute),
		Pool:                 "pp", PoolIndex: 3, VerifySkipped: true,
		CreatedAt: created, UpdatedAt: created.Add(time.Hour),
	}
	if err := deps.Registry.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	if _, err := deps.Vault.Set(ctx, rec.AuthRef.String(), []byte(oldKeyValue), secrets.SetUpdate); err != nil {
		t.Fatalf("seed vault: %v", err)
	}
	deps.Clock = &tickingClock{now: created.Add(48 * time.Hour)}
	return rec
}

// vaultValue reads ref from deps' vault or fails the test.
func vaultValue(t *testing.T, deps Deps, ref VaultKeyRef) string {
	t.Helper()
	v, err := deps.Vault.Get(context.Background(), ref.String())
	if err != nil {
		t.Fatalf("Vault.Get %s: %v", ref, err)
	}
	return string(v)
}

// assertUnchanged fails unless name's record DeepEquals want and the key
// ref still holds oldKeyValue.
func assertUnchanged(t *testing.T, deps Deps, want ProviderRecord) {
	t.Helper()
	got, err := deps.Registry.GetProvider(context.Background(), want.Name)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record changed:\n got  %+v\n want %+v", got, want)
	}
	if v := vaultValue(t, deps, keyRefFor(want.Name, "key")); v != oldKeyValue {
		t.Fatalf("vault value changed (len %d)", len(v))
	}
}

func TestReauthUnknownProviderNotFound(t *testing.T) {
	deps, _ := testDeps(t, &fakeDoer{})
	deps.Getenv = func(string) string { return "sk-ant-" + "api03-value" }
	for _, mode := range []CredentialMode{CredentialKeyEnv, CredentialUnset} {
		_, err := Reauth(context.Background(), deps, ReauthRequest{Name: "nope", Credential: mode, KeyEnvVar: "X"})
		assertRefused(t, err, cascade.KindNotFound, ErrUnknownProvider,
			"intake: unknown provider \"nope\": run `cascade provider add` first")
		// A weak implementation upserts a fresh record instead of refusing.
		if _, gerr := deps.Registry.GetProvider(context.Background(), "nope"); !cascade.HasKind(gerr, cascade.KindNotFound) {
			t.Fatalf("mode %d: reauth of an unknown name created a record (%v)", mode, gerr)
		}
	}
}

func TestReauthReplacesRefsInPlace(t *testing.T) {
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	seed := seedReauthRecord(t, &deps, "myclaude", DriverAnthropic)
	deps.Getenv = func(k string) string { return map[string]string{"NEW_KEY": "sk-ant-api03-new-value"}[k] }
	rec := &writeRecorder{Registry: deps.Registry}
	deps.Registry = rec

	result, err := Reauth(context.Background(), deps, ReauthRequest{Name: "myclaude", Credential: CredentialKeyEnv, KeyEnvVar: "NEW_KEY"})
	if err != nil {
		t.Fatalf("Reauth: %v", err)
	}
	want := seed
	want.Auth, want.AuthRef, want.VerifySkipped = AuthKey, keyRefFor("myclaude", "key"), false
	want.UpdatedAt = seed.CreatedAt.Add(48*time.Hour + time.Second)
	got, err := deps.Registry.GetProvider(context.Background(), "myclaude")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(result.Record, want) {
		t.Fatalf("record not replaced in place:\n got  %+v\n want %+v", got, want)
	}
	if result.Status != "reauthorized" {
		t.Fatalf("status = %q, want reauthorized", result.Status)
	}
	// Exactly one registry write, differing from the seed only in the auth
	// fields: any other field, including a lane-state field added later,
	// that Reauth writes fails here.
	if len(rec.upserts) != 1 {
		t.Fatalf("Reauth issued %d registry writes, want exactly 1", len(rec.upserts))
	}
	assertOnlyAuthFieldsDiffer(t, seed, rec.upserts[0])
	if v := vaultValue(t, deps, want.AuthRef); v != "sk-ant-api03-new-value" {
		t.Fatalf("vault ref does not hold the new value (len %d)", len(v))
	}
}

// assertOnlyAuthFieldsDiffer walks every ProviderRecord field by reflection
// and fails on a difference outside the four fields Reauth owns.
func assertOnlyAuthFieldsDiffer(t *testing.T, before, after ProviderRecord) {
	t.Helper()
	owned := map[string]bool{"Auth": true, "AuthRef": true, "VerifySkipped": true, "UpdatedAt": true}
	bv, av := reflect.ValueOf(before), reflect.ValueOf(after)
	for i := 0; i < bv.NumField(); i++ {
		name := bv.Type().Field(i).Name
		if !owned[name] && !reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
			t.Fatalf("Reauth changed non-auth field %s: %v -> %v", name, bv.Field(i), av.Field(i))
		}
	}
}

func TestReauthFailedVerifyLeavesRecordUntouched(t *testing.T) {
	deps, _ := testDeps(t, &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/messages": {Status: 401, Body: []byte(`{"error":"unauthorized"}`)},
	}})
	seed := seedReauthRecord(t, &deps, "myclaude", DriverAnthropic)
	for _, req := range []ReauthRequest{
		{Name: "myclaude", Credential: CredentialKey, KeyValue: []byte("sk-ant-api03-new-bad-value")},
		{Name: "myclaude", Credential: CredentialKeyEnv, KeyEnvVar: "BAD"},
	} {
		deps.Getenv = func(string) string { return "sk-ant-api03-new-bad-value" }
		_, err := Reauth(context.Background(), deps, req)
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("mode %d: expected the failed micro-verify to refuse, got %v", req.Credential, err)
		}
		assertUnchanged(t, deps, seed)
	}
}

func TestReauthNeverCreatesDuplicate(t *testing.T) {
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	seedReauthRecord(t, &deps, "myclaude", DriverAnthropic)
	for i := 0; i < 2; i++ {
		if _, err := Reauth(context.Background(), deps, ReauthRequest{Name: "myclaude", Credential: CredentialKey, KeyValue: []byte("sk-ant-api03-new")}); err != nil {
			t.Fatalf("Reauth %d: %v", i, err)
		}
	}
	reg := deps.Registry.(*MemoryRegistry)
	inPool, _ := reg.ListPool(context.Background(), "pp")
	standalone, _ := reg.ListPool(context.Background(), "")
	if len(inPool) != 1 || inPool[0].Name != "myclaude" || len(standalone) != 0 {
		t.Fatalf("want exactly one myclaude in pool pp and none standalone; pool=%+v standalone=%+v", inPool, standalone)
	}
}

func TestReauthModeChangeRepointsAuthRef(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	seed := seedReauthRecord(t, &deps, "anthropic-main", DriverAnthropic)
	const accessRef = "oauth.anthropic.default.access.1"
	if err := custody.Set(context.Background(), accessRef, []byte("sk-ant-oat01-oauth-token")); err != nil {
		t.Fatalf("seed access token: %v", err)
	}
	deps.NewOAuthBroker = func(provider.ProviderOAuthConfig, secrets.OAuthDeps) (provider.OAuthBroker, error) {
		return fakeOAuthBroker{rec: provider.TokenRecord{Provider: "anthropic", AccessRef: accessRef}}, nil
	}
	result, err := Reauth(context.Background(), deps, ReauthRequest{Name: "anthropic-main", Credential: CredentialOAuth})
	if err != nil {
		t.Fatalf("Reauth --oauth: %v", err)
	}
	want := seed
	want.Auth, want.AuthRef, want.VerifySkipped = AuthOAuth, VaultKeyRef(accessRef), false
	want.UpdatedAt = seed.CreatedAt.Add(48*time.Hour + time.Second)
	got, err := deps.Registry.GetProvider(context.Background(), "anthropic-main")
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(result.Record, want) {
		t.Fatalf("key->oauth did not repoint only the auth fields (err %v):\n got  %+v\n want %+v", err, got, want)
	}
	if v := vaultValue(t, deps, seed.AuthRef); v != oldKeyValue {
		t.Fatal("a mode change must keep the previous key entry, not delete or overwrite it")
	}
}

func TestReauthEmptyKeyRefused(t *testing.T) {
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	seed := seedReauthRecord(t, &deps, "myclaude", DriverAnthropic)
	for _, v := range [][]byte{nil, []byte(""), []byte("  \t")} {
		_, err := Reauth(context.Background(), deps, ReauthRequest{Name: "myclaude", Credential: CredentialKey, KeyValue: v, NoVerify: true})
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.HasSuffix(err.Error(), "intake: the value --key read from stdin is empty") {
			t.Fatalf("empty --key %q: want the typed empty-key refusal, got %v", v, err)
		}
		assertUnchanged(t, deps, seed)
	}
}

func TestReauthOAuthDriverMismatchRefused(t *testing.T) {
	deps, custody := testDeps(t, anthropicSuccessDoer(t))
	seed := seedReauthRecord(t, &deps, "anthropic-proxy", DriverOpenAICompat)
	// A real token and --no-verify: without the guard this reauth SUCCEEDS
	// and stores an anthropic grant against an openai-compat record.
	if err := custody.Set(context.Background(), "oauth.anthropic.x", []byte("sk-ant-oat01-grant")); err != nil {
		t.Fatalf("seed access token: %v", err)
	}
	started := false
	deps.NewOAuthBroker = func(provider.ProviderOAuthConfig, secrets.OAuthDeps) (provider.OAuthBroker, error) {
		started = true
		return fakeOAuthBroker{rec: provider.TokenRecord{Provider: "anthropic", AccessRef: "oauth.anthropic.x"}}, nil
	}
	_, err := Reauth(context.Background(), deps, ReauthRequest{Name: "anthropic-proxy", Credential: CredentialOAuth, NoVerify: true})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("expected a typed driver-mismatch refusal, got %v", err)
	}
	if started {
		t.Fatal("the OAuth broker ran before the driver mismatch was refused")
	}
	assertUnchanged(t, deps, seed)
}

func TestReauthRequestValidate(t *testing.T) {
	for _, r := range []ReauthRequest{{Name: " ", Credential: CredentialKeyEnv}, {Name: "p"}, {Name: "p", Credential: CredentialMode(99)}} {
		if !cascade.HasKind(r.Validate(), cascade.KindInvalidInput) {
			t.Fatalf("Validate(%+v) accepted an invalid request", r)
		}
	}
	deps, _ := testDeps(t, &fakeDoer{})
	if _, _, _, err := resolveReauthCredential(context.Background(), deps, ReauthRequest{Name: "p"}, DriverAnthropic); err == nil {
		t.Fatal("resolveReauthCredential accepted CredentialUnset")
	}
	if _, err := Reauth(context.Background(), Deps{}, ReauthRequest{Name: "p", Credential: CredentialKeyEnv}); err == nil {
		t.Fatal("Reauth accepted an empty Deps")
	}
}

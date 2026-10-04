// Purpose: the custody harness every `provider reauth` CLI test runs on,
//   plus the flagless-form tests (P1-WID-01) and the proof that no reauth
//   test reaches a platform credential store.
// Inputs: testProviderDeps/runProvider (provider_cmd_test.go) and the
//   durable-state helpers in provider_reauth_test.go.
// Outputs: none.
// Constraints: custody is forced to a file vault in t.TempDir() through a
//   recording keychain runner that REPORTS a usable keychain, so a reached
//   runner is counted, never trusted; HOME and USERPROFILE are fresh temp
//   dirs. Each flagless refusal names the weak input it must refuse.
// SPORT: cli.provider.reauth/ADD (P1-WID-01).

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// reauthSpy counts keychain-runner calls, custody selections, stdin reads
// and OAuth starts, and lists every directory a test may write.
type reauthSpy struct {
	runs, custodies, reads, starts atomic.Int32
	keychain                       string
	dirs                           []string
}

// run is the keychain runner: it reports a usable keychain, so a reached
// runner would hand custody a platform store. It must never be called.
func (s *reauthSpy) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	s.runs.Add(1)
	if len(args) > 0 && args[0] == "default-keychain" {
		return []byte(s.keychain), nil
	}
	return nil, nil
}

// grantBroker is a browser-free OAuth broker that stores a fresh token
// through the real vault broker, as the PKCE broker does.
type grantBroker struct {
	oa  secrets.OAuthDeps
	spy *reauthSpy
}

func (b grantBroker) Start(ctx context.Context) (provider.TokenRecord, error) {
	ref := fmt.Sprintf("oauth.anthropic.access.%d", b.spy.starts.Add(1))
	_, err := b.oa.Vault.Set(ctx, ref, []byte("sk-ant-"+"oat01-"+ref), secrets.SetUpdate)
	return provider.TokenRecord{Provider: "anthropic", AccessRef: ref}, err
}
func (grantBroker) Refresh(context.Context, string) (provider.TokenRecord, error) {
	return provider.TokenRecord{}, errors.New("unused")
}
func (grantBroker) Revoke(context.Context, string) error { return nil }

// reauthDeps is testProviderDeps over the durable registry with the spy
// wired into custody, stdin and the OAuth broker.
func reauthDeps(t *testing.T, env map[string]string, verifyStatus int) (providerDeps, *reauthSpy) {
	t.Helper()
	home, profile, vaultDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", profile)
	deps := testProviderDeps(t, env)
	spy := &reauthSpy{keychain: filepath.Join(vaultDir, "login.keychain-db"), dirs: []string{home, profile, vaultDir, deps.Paths.Root()}}
	if err := os.WriteFile(spy.keychain, nil, 0o600); err != nil {
		t.Fatalf("keychain decoy: %v", err)
	}
	deps.Registry, deps.Doer = nil, anthropicFullDoer(verifyStatus)
	deps.NewCustody = func() (secrets.Custody, error) {
		spy.custodies.Add(1)
		return secrets.SelectCustody(secrets.Config{
			Service: "cascade-provider-reauth-test", Dir: vaultDir, Passphrase: "cli-test-pass",
			Runner: spy.run, ForceFileVault: true,
		})
	}
	stdin := deps.ReadStdin
	deps.ReadStdin = func() ([]byte, error) { spy.reads.Add(1); return stdin() }
	deps.NewOAuthBroker = func(_ provider.ProviderOAuthConfig, oa secrets.OAuthDeps) (provider.OAuthBroker, error) {
		return grantBroker{oa: oa, spy: spy}, nil
	}
	t.Cleanup(func() {
		if n := spy.runs.Load(); n != 0 {
			t.Errorf("the keychain runner was called %d times: custody could reach a platform store", n)
		}
	})
	return deps, spy
}

// seedOAuth registers anthropic-main with `provider add --oauth` (grant 1)
// and zeroes the custody and stdin counters.
func seedOAuth(t *testing.T, deps providerDeps, spy *reauthSpy) registry.ProviderRecord {
	t.Helper()
	if _, _, err := runProvider(t, deps, "add", "anthropic-main", "--oauth"); err != nil {
		t.Fatalf("seed add --oauth: %v", err)
	}
	rec, _ := durableState(t, deps, "anthropic-main")
	if rec.Auth != registry.AuthOAuth || rec.AuthRef != "oauth.anthropic.access.1" {
		t.Fatalf("seed is not an oauth record on grant 1: %+v", rec)
	}
	spy.custodies.Store(0)
	spy.reads.Store(0)
	return rec
}

// assertNothingRead fails if a refused flagless call read stdin, selected
// custody or started the OAuth broker more than wantStarts times.
func assertNothingRead(t *testing.T, spy *reauthSpy, wantStarts int32) {
	t.Helper()
	if r, c, s := spy.reads.Load(), spy.custodies.Load(), spy.starts.Load(); r != 0 || c != 0 || s != wantStarts {
		t.Fatalf("refusal was not first: stdin reads %d, custody selections %d, broker starts %d (want 0, 0, %d)", r, c, s, wantStarts)
	}
}

func TestProviderReauthFlaglessOAuthRecordRunsOAuth(t *testing.T) {
	deps, spy := reauthDeps(t, nil, 200)
	before := seedOAuth(t, deps, spy)
	stdout, _, err := runProvider(t, deps, "reauth", "anthropic-main", "--json")
	after, lanes := durableState(t, deps, "anthropic-main")
	if err != nil || spy.starts.Load() != 2 || spy.reads.Load() != 0 || after.AuthRef != "oauth.anthropic.access.2" {
		t.Fatalf("flagless reauth: err %v, starts %d, reads %d, AuthRef %q (want grant 2)", err, spy.starts.Load(), spy.reads.Load(), after.AuthRef)
	}
	after.AuthRef, after.UpdatedAt = before.AuthRef, before.UpdatedAt
	if !reflect.DeepEqual(before, after) || !strings.Contains(stdout, "reauthorized") || len(lanes) != 1 ||
		lanes[0].Capacity != registry.CapacityInteractiveUsage || lanes[0].State != registry.LaneStateAvailable {
		t.Fatalf("flagless oauth changed more than the auth ref, or the lane is wrong: %+v %+v", after, lanes)
	}
}

func TestProviderReauthFlaglessKeyRecordRefusesWithRemedy(t *testing.T) {
	oldKey := realisticKey("old")
	deps, spy := reauthDeps(t, map[string]string{"OLD_KEY": oldKey}, 200)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key-env", "OLD_KEY"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	before, lanesBefore := durableState(t, deps, "myclaude")
	spy.custodies.Store(0)
	// Weak input: stdin holds a valid key and verify answers 200, so a
	// fallback to --key would succeed and rewrite the record.
	_, _, err := runProvider(t, deps, "reauth", "myclaude")
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !errors.Is(err, intake.ErrFlaglessRefused) ||
		!strings.Contains(err.Error(), `provider "myclaude" stores an API key`) || !strings.Contains(err.Error(), "--key-env") {
		t.Fatalf("want the typed key-record remedy naming myclaude and --key-env, got %v", err)
	}
	assertNothingRead(t, spy, 0)
	after, lanes := durableState(t, deps, "myclaude")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(lanesBefore, lanes) || storedValue(t, deps, "provider.myclaude.key") != oldKey {
		t.Fatal("a refused flagless reauth changed the record, the lane or the key")
	}
}

func TestProviderReauthFlaglessUnknownAuthFailsClosed(t *testing.T) {
	for _, auth := range []intake.AuthType{"", "bogus"} {
		deps, spy := reauthDeps(t, nil, 200)
		// Weak input: an OAuth-family name, so a default-to-oauth guess
		// would find a client config and start the broker.
		seed := intake.ProviderRecord{Name: "anthropic-main", Driver: intake.DriverAnthropic, Auth: auth,
			AuthRef: "provider.anthropic-main.key", KnownModels: []string{"claude-3-5-sonnet-20241022"}}
		mem := intake.NewMemoryRegistry()
		if err := mem.UpsertProvider(context.Background(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		deps.Registry = mem
		_, _, err := runProvider(t, deps, "reauth", "anthropic-main")
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !errors.Is(err, intake.ErrFlaglessRefused) ||
			!strings.Contains(err.Error(), "no recognised auth type") {
			t.Fatalf("Auth %q: want the fail-closed refusal, got %v", auth, err)
		}
		assertNothingRead(t, spy, 0)
		if got, _ := mem.GetProvider(context.Background(), "anthropic-main"); !reflect.DeepEqual(got, seed) {
			t.Fatalf("Auth %q: the record changed: %+v", auth, got)
		}
	}
}

func TestProviderReauthFlaglessRefusedUnderNoInput(t *testing.T) {
	env := map[string]string{}
	deps, spy := reauthDeps(t, env, 200)
	before := seedOAuth(t, deps, spy)
	env["CASCADE_NO_INPUT"] = "1" // weak input: an oauth record the broker could re-authorize
	_, _, err := runProvider(t, deps, "reauth", "anthropic-main")
	if !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, intake.ErrNoInputInteractive) ||
		!strings.Contains(err.Error(), "CASCADE_NO_INPUT=1 forbids") || !strings.Contains(err.Error(), "--key-env") {
		t.Fatalf("want CASCADE_NO_INPUT=1 to hard-error the flagless form, got %v", err)
	}
	assertNothingRead(t, spy, 1)
	if after, _ := durableState(t, deps, "anthropic-main"); !reflect.DeepEqual(before, after) {
		t.Fatal("a refused flagless reauth changed the record")
	}
}

// TestProviderReauthNeverTouchesKeychain drives every reauth mode through
// one harness: the file vault must answer and the recording keychain
// runner, which would report a usable keychain, must see zero calls.
func TestProviderReauthNeverTouchesKeychain(t *testing.T) {
	deps, spy := reauthDeps(t, map[string]string{"OLD_KEY": realisticKey("old"), "NEW_KEY": realisticKey("new")}, 200)
	for _, args := range [][]string{
		{"add", "myclaude", "--key-env", "OLD_KEY"}, {"reauth", "myclaude", "--key-env", "NEW_KEY"},
		{"reauth", "myclaude", "--key"}, {"reauth", "myclaude", "--key", "--no-verify"},
		{"add", "anthropic-main", "--oauth"}, {"reauth", "anthropic-main"}, {"reauth", "anthropic-main", "--oauth"},
	} {
		if _, _, err := runProvider(t, deps, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	deps.Doer = anthropicFullDoer(401)
	if _, _, err := runProvider(t, deps, "reauth", "myclaude", "--key-env", "NEW_KEY"); err == nil {
		t.Fatal("the failed-verify leg did not fail")
	}
	custody, err := deps.NewCustody()
	if err != nil || custody.Name() != "file-vault" || os.Getenv("HOME") != spy.dirs[0] || os.Getenv("USERPROFILE") != spy.dirs[1] {
		t.Fatalf("custody %v (err %v) or HOME/USERPROFILE is not the per-test file vault/temp dirs", custody, err)
	}
	if n, c := spy.runs.Load(), spy.custodies.Load(); n != 0 || c < 8 {
		t.Fatalf("keychain runner calls %d (want 0) over %d custody selections (want >= 8)", n, c)
	}
}

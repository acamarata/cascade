// Purpose: CLI tests for `cascade provider reauth` (P1-WID-01) over the
//   DURABLE registry production uses: in-place replacement, one pooled lane,
//   no credential in output, errors or on disk, the empty --key and NO_INPUT
//   refusals. The flagless form is in provider_reauth_flagless_test.go.
// Inputs: testProviderDeps/runProvider (provider_cmd_test.go) and reauthDeps
//   (provider_reauth_flagless_test.go): custody forced to a temp file vault
//   through a recording keychain runner, fresh HOME/USERPROFILE.
// Outputs: none.
// Constraints: no network (fakeProviderDoer), no platform keychain: every
//   test fails at cleanup if the runner was ever called. Credential-shaped
//   values are assembled at run time (C22).
// SPORT: cli.provider.reauth/ADD (P1-WID-01).

package main

import (
	"bytes"
	"context"
	"errors"
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

// anthropicFullDoer answers the shape probe and the micro-verify.
func anthropicFullDoer(verifyStatus int) intake.Doer {
	return fakeProviderDoer{responses: map[string]intake.HTTPResponse{
		"https://api.anthropic.com/v1/models": {Status: 200, Body: []byte(
			`{"data":[{"id":"claude-3-5-sonnet-20241022","type":"model"}]}`)},
		"https://api.anthropic.com/v1/messages": {Status: verifyStatus, Body: []byte(
			`{"content":[{"type":"text","text":"hi"}]}`)},
	}}
}

// realisticKey builds a key in the vendor's real shape at run time.
func realisticKey(tag string) string {
	return "sk-ant-" + "api03-" + tag + strings.Repeat("Xq7vR2mK9pL4wZ8s", 5) + "-AbCdEfAA"
}

// storedValue reads ref from the test vault through a fresh broker.
func storedValue(t *testing.T, deps providerDeps, ref string) string {
	t.Helper()
	custody, err := deps.NewCustody()
	if err != nil {
		t.Fatalf("custody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, deps.Gate)
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	v, err := broker.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Vault.Get %s: %v", ref, err)
	}
	return string(v)
}

// durableState returns name's durable row and every lane.
func durableState(t *testing.T, deps providerDeps, name string) (registry.ProviderRecord, []registry.LaneRecord) {
	t.Helper()
	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage: %v", err)
	}
	defer func() { _ = store.Close() }()
	rec, err := store.Registry.GetProvider(context.Background(), name)
	lanes, lerr := store.Registry.ListLanes(context.Background())
	if err != nil || lerr != nil {
		t.Fatalf("GetProvider/ListLanes: %v / %v", err, lerr)
	}
	return rec, lanes
}

// setPoolIndex moves the only lane's round-robin index, so a reset shows.
func setPoolIndex(t *testing.T, deps providerDeps, idx int) {
	t.Helper()
	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage: %v", err)
	}
	defer func() { _ = store.Close() }()
	lanes, err := store.Registry.ListLanes(context.Background())
	if err != nil || len(lanes) != 1 {
		t.Fatalf("setup: lanes = %+v (err %v), want one", lanes, err)
	}
	lanes[0].PoolIndex = idx
	if err := store.Registry.UpsertLane(context.Background(), lanes[0]); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
}

// onDisk counts files under the spy's directories that contain needle.
func onDisk(t *testing.T, spy *reauthSpy, needle string) (hits, files int) {
	t.Helper()
	for _, dir := range spy.dirs {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files++
				if b, rerr := os.ReadFile(p); rerr == nil && bytes.Contains(b, []byte(needle)) {
					hits++
				}
			}
			return nil
		})
	}
	return hits, files
}

// assertNoLeak fails if secret is in any text or file; the scan must see
// the provider's own ref on disk, so it cannot pass on empty input.
func assertNoLeak(t *testing.T, spy *reauthSpy, secret, ref string, texts ...string) {
	t.Helper()
	if seen, _ := onDisk(t, spy, ref); secret == "" || seen == 0 {
		t.Fatalf("leak scan is vacuous: ref %q not found on disk", ref)
	}
	if hits, _ := onDisk(t, spy, secret); hits != 0 || strings.Contains(strings.Join(texts, "\n"), secret) {
		t.Fatalf("a credential value leaked (%d files, or output/error text)", hits)
	}
}

func TestProviderReauthKeyEnvFlow(t *testing.T) {
	newKey := realisticKey("new")
	deps, _ := reauthDeps(t, map[string]string{"OLD_KEY": realisticKey("old"), "NEW_KEY": newKey}, 200)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key-env", "OLD_KEY"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	before, _ := durableState(t, deps, "myclaude")
	if _, _, err := runProvider(t, deps, "reauth", "myclaude", "--key-env", "NEW_KEY", "--json"); err != nil {
		t.Fatalf("provider reauth --key-env: %v", err)
	}
	after, lanes := durableState(t, deps, "myclaude")
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) || storedValue(t, deps, "provider.myclaude.key") != newKey {
		t.Fatalf("record not replaced in place:\n before %+v\n after  %+v", before, after)
	}
	if len(lanes) != 1 || lanes[0].LaneName != "myclaude" || lanes[0].State != registry.LaneStateAvailable {
		t.Fatalf("lanes = %+v, want one verified lane myclaude", lanes)
	}
}

func TestProviderReauthPooledProviderKeepsOneLane(t *testing.T) {
	deps, _ := reauthDeps(t, map[string]string{"OLD_KEY": realisticKey("old"), "NEW_KEY": realisticKey("new")}, 200)
	if _, _, err := runProvider(t, deps, "add", "p1", "--key-env", "OLD_KEY", "--pool", "pp"); err != nil {
		t.Fatalf("seed add --pool: %v", err)
	}
	setPoolIndex(t, deps, 5)
	before, _ := durableState(t, deps, "p1")
	if _, _, err := runProvider(t, deps, "reauth", "p1", "--key-env", "NEW_KEY"); err != nil {
		t.Fatalf("provider reauth: %v", err)
	}
	after, lanes := durableState(t, deps, "p1")
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) || len(lanes) != 1 || lanes[0].LaneName != "pp/p1" || lanes[0].PoolMembership != "pp" || lanes[0].PoolIndex != 5 {
		t.Fatalf("lanes after reauth %+v, want exactly one lane pp/p1 keeping pool index 5 (record changed: %v)", lanes, !reflect.DeepEqual(before, after))
	}
}

func TestProviderReauthCredentialNeverInOutput(t *testing.T) {
	oldKey, newKey := realisticKey("old"), realisticKey("leak")
	deps, spy := reauthDeps(t, map[string]string{"OLD_KEY": oldKey, "NEW_KEY": newKey}, 200)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key-env", "OLD_KEY"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	for _, args := range [][]string{{"--json"}, {}} {
		stdout, stderr, err := runProvider(t, deps, append([]string{"reauth", "myclaude", "--key-env", "NEW_KEY"}, args...)...)
		if err != nil || !strings.Contains(stdout, "provider.myclaude.key") {
			t.Fatalf("reauth %v: err %v, output omitted the ref: %q", args, err, stdout)
		}
		assertNoLeak(t, spy, newKey, "provider.myclaude.key", stdout, stderr)
		assertNoLeak(t, spy, oldKey, "provider.myclaude.key", stdout, stderr)
	}
	// A value pasted as a positional argument is refused without echo.
	_, _, err := runProvider(t, deps, "reauth", "myclaude", newKey)
	if err == nil || strings.Contains(err.Error(), newKey) {
		t.Fatalf("a positional credential was accepted or echoed: %v", err != nil)
	}
}

func TestProviderReauthFailedVerifyLeaksNothingAndKeepsOldKey(t *testing.T) {
	oldKey, badKey := realisticKey("old"), realisticKey("bad")
	deps, spy := reauthDeps(t, map[string]string{"OLD_KEY": oldKey, "BAD_KEY": badKey}, 200)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key-env", "OLD_KEY"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	before, lanesBefore := durableState(t, deps, "myclaude")
	deps.Doer = anthropicFullDoer(401)
	stdout, stderr, err := runProvider(t, deps, "reauth", "myclaude", "--key-env", "BAD_KEY")
	if err == nil {
		t.Fatal("expected the 401 micro-verify to refuse the reauth")
	}
	assertNoLeak(t, spy, badKey, "provider.myclaude.key", err.Error(), stdout, stderr)
	assertNoLeak(t, spy, oldKey, "provider.myclaude.key", err.Error(), stdout, stderr)
	after, lanes := durableState(t, deps, "myclaude")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(lanesBefore, lanes) || storedValue(t, deps, "provider.myclaude.key") != oldKey {
		t.Fatalf("a failed verify changed durable state or the key:\n before %+v %+v\n after  %+v %+v", before, lanesBefore, after, lanes)
	}
}

func TestProviderReauthEmptyKeyRefusedForAddAndReauth(t *testing.T) {
	deps, _ := reauthDeps(t, nil, 200)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	deps.ReadStdin = func() ([]byte, error) { return []byte(" \n"), nil }
	if _, _, err := runProvider(t, deps, "reauth", "myclaude", "--key", "--oauth"); err == nil || !strings.Contains(err.Error(), "pass at most one of") {
		t.Fatalf("reauth with two credential flags: %v", err)
	}
	for _, verb := range []string{"add", "reauth"} {
		_, _, err := runProvider(t, deps, verb, "myclaude", "--key", "--no-verify")
		if err == nil || !strings.Contains(err.Error(), "provider "+verb+": the value --key read from stdin is empty") {
			t.Fatalf("%s --key with empty stdin: want the empty-key refusal, got %v", verb, err)
		}
	}
	if storedValue(t, deps, "provider.myclaude.key") != "sk-ant-test-value" {
		t.Fatal("an empty --key overwrote the stored key")
	}
}

func TestProviderReauthUnknownProviderRefusesAddFirst(t *testing.T) {
	deps, _ := reauthDeps(t, map[string]string{"NEW_KEY": realisticKey("new")}, 200)
	// Flagless first: on a fresh CASCADE_HOME no data directory exists yet.
	for _, args := range [][]string{{}, {"--key-env", "NEW_KEY"}} {
		_, _, err := runProvider(t, deps, append([]string{"reauth", "never-added"}, args...)...)
		if !cascade.HasKind(err, cascade.KindNotFound) || !errors.Is(err, intake.ErrUnknownProvider) ||
			!strings.Contains(err.Error(), `unknown provider "never-added": run `+"`cascade provider add`"+` first`) {
			t.Fatalf("reauth %v: want the add-first not-found refusal, got %v", args, err)
		}
	}
}

func TestProviderReauthOAuthRefusedUnderNoInput(t *testing.T) {
	env := map[string]string{"CASCADE_NO_INPUT": "1"}
	deps, spy := reauthDeps(t, env, 200)
	if _, _, err := runProvider(t, deps, "add", "anthropic-main", "--key", "--no-verify"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	before, _ := durableState(t, deps, "anthropic-main")
	_, _, err := runProvider(t, deps, "reauth", "anthropic-main", "--oauth")
	if !errors.Is(err, intake.ErrNoInputInteractive) || !strings.Contains(err.Error(), "use --key or --key-env") || spy.starts.Load() != 0 {
		t.Fatalf("want CASCADE_NO_INPUT=1 to refuse --oauth before the broker, got %v (starts %d)", err, spy.starts.Load())
	}
	if after, _ := durableState(t, deps, "anthropic-main"); !reflect.DeepEqual(before, after) {
		t.Fatal("a refused --oauth changed the durable record (silent fallback)")
	}
}

func TestProviderReauthHelpDocumentsAllFlags(t *testing.T) {
	deps, _ := reauthDeps(t, nil, 200)
	stdout, _, err := runProvider(t, deps, "reauth", "--help")
	for _, want := range []string{"--key", "--key-env", "--oauth", "--no-verify", "CASCADE_NO_INPUT", "no credential flag", "lane available"} {
		if err != nil || !strings.Contains(stdout, want) {
			t.Errorf("provider reauth --help (err %v) does not document %q:\n%s", err, want, stdout)
		}
	}
}

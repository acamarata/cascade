//go:build !windows

// Purpose: the gateway branch of Resolve (P1-GWY-08): the admitted account
// supplies base URL, header style and token; an unadmitted account, a base
// URL mismatch (also tampered in a real providers.db) and a failing helper
// send nothing; the canary never leaks. No network, vault or keychain.
// SPORT: internal/providers/dispatch gateway/ADD (P1-GWY-08).

package dispatch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/anthropic"
	_ "modernc.org/sqlite" // the providers.db driver the daemon uses
)

const gwBaseURL = "https://gateway.invalid"

// gwTransport records every request's URL and headers.
type gwTransport struct {
	mu      sync.Mutex
	urls    []string
	headers []map[string]string
}

func (g *gwTransport) Send(_ context.Context, _, url string, headers map[string]string, _ []byte) (int, map[string][]string, io.ReadCloser, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.urls, g.headers = append(g.urls, url), append(g.headers, headers)
	return 200, nil, io.NopCloser(strings.NewReader(anthropicOK)), nil
}

func (g *gwTransport) calls() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.urls) }

// countingVault is the base CredentialSource; a gateway ref must never reach it.
type countingVault struct{ n int }

func (c *countingVault) Resolve(context.Context, string) (string, error) {
	c.n++
	return "", cascade.New(cascade.KindUnavailable, "test: no grant")
}

// gwRig is one gateway provider behind a real Resolver.
type gwRig struct {
	dir, canary, helper string
	store               *laneStore
	tr                  *gwTransport
	vault               *countingVault
	logs                *bytes.Buffer
	res                 *Resolver
}

// newCanary returns a fresh per-test token.
func newCanary(t *testing.T) string {
	t.Helper()
	b := make([]byte, 20)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

// writeHelper writes a fake helper printing canary (split, so the file never
// holds it whole) or exiting 1, and returns its path and digest.
func writeHelper(t *testing.T, dir, canary string, fail bool) (string, string) {
	t.Helper()
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nprintf '%%s\\n' '%s'\n", canary[:10], canary[10:])
	if fail {
		body = "#!/bin/sh\nexit 1\n"
	}
	p := filepath.Join(dir, "fake-helper")
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return p, hex.EncodeToString(sum[:])
}

// scriptRuns reports whether this environment may exec a script from a temp
// dir (some sandboxes forbid it); when not, the env source stands in.
func scriptRuns(t *testing.T, path string) bool {
	t.Helper()
	var ee *exec.ExitError
	err := exec.Command(path).Run() //nolint:gosec // test-written script
	if err != nil && !errors.As(err, &ee) {
		t.Logf("cannot exec a temp-dir script here (%v); the env credential source stands in", err)
		return false
	}
	return true
}

// gwAccount is the admitted account "acct" over helper.
func gwAccount(helper, sum string, header gateway.HeaderStyle) gateway.Account {
	return gateway.Account{Name: "acct", ID: gwBaseURL, BaseURL: gwBaseURL, Header: header, Models: []string{"model-a"},
		Credential: gateway.Credential{Kind: gateway.CredentialHelper, HelperPath: helper, HelperSHA256: sum}, TermsSensitive: true}
}

// gwRecord is the gateway provider record as `provider add --gateway` writes it.
func gwRecord(base string) registry.ProviderRecord {
	return registry.ProviderRecord{Name: "gw1", Driver: registry.DriverAnthropic, BaseURL: base,
		Auth: registry.AuthGateway, AuthRef: registry.VaultKeyRef("gateway:acct"), KnownModels: []string{"model-a"},
		AccountKind: registry.AccountPersonal, Tier: registry.TierMid, HealthStatus: registry.HealthUnknown}
}

// newGWRig builds a rig over lookup (nil: an in-memory lane store holding rec)
// with admitted as the admitted accounts.
func newGWRig(t *testing.T, rec registry.ProviderRecord, lookup RegistryLookup, fail bool, admitted func(gateway.Account) []gateway.Account) *gwRig {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	g := &gwRig{dir: dir, canary: newCanary(t), helper: filepath.Join(dir, "fake-helper"), tr: &gwTransport{}, vault: &countingVault{}, logs: &bytes.Buffer{}}
	helper, sum := writeHelper(t, dir, g.canary, fail)
	acct, src := gwAccount(helper, sum, gateway.HeaderBearer), gateway.Source{Home: dir, User: "test-user"}
	if !fail && !scriptRuns(t, helper) {
		acct.Credential = gateway.Credential{Kind: gateway.CredentialEnv, EnvVar: "GW_TEST_TOKEN"}
		src.Getenv = func(k string) string { return map[string]string{"GW_TEST_TOKEN": g.canary}[k] }
	}
	g.store = &laneStore{providers: map[string]registry.ProviderRecord{rec.Name: rec},
		lanes: []registry.LaneRecord{{LaneName: "lane-1", ProviderName: rec.Name, State: registry.LaneStateAvailable, Capacity: registry.CapacityAPICredit}}}
	if lookup == nil {
		lookup = g.store
	}
	res, err := NewResolver(lookup, g.vault, gwClock(), g.tr, WithGatewayAccounts(admitted(acct), src))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	res.log, g.res = slog.New(slog.NewTextHandler(g.logs, nil)), res
	return g
}

// gwClock is a clock frozen at the harness instant.
func gwClock() *runtime.FixedClock { return runtime.NewFixedClock(laneHarnessNow) }

func admitOne(a gateway.Account) []gateway.Account { return []gateway.Account{a} }
func admitNone(gateway.Account) []gateway.Account  { return nil }

// call resolves the lane and runs one Chat; it returns the first error.
func (g *gwRig) call(t *testing.T) (provider.ModelProvider, error) {
	t.Helper()
	p, err := g.res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1", Model: "model-a"})
	if err != nil {
		return nil, err
	}
	_, err = p.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	return p, err
}

// leakText is everything the rig produced that must not hold the canary.
func (g *gwRig) leakText(t *testing.T, err error) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "%v|%+v|%s", err, g.store.lanes, g.logs.String())
	_ = filepath.WalkDir(g.dir, func(p string, d os.DirEntry, werr error) error {
		if werr == nil && !d.IsDir() {
			raw, _ := os.ReadFile(p)
			b.Write(raw)
		}
		return nil
	})
	return b.String()
}

// wantErr asserts err is a *cascade.Error of kind whose message contains msg.
func wantErr(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || !cascade.HasKind(err, kind) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("err = %v, want %v naming %q", err, kind, msg)
	}
}

func TestResolveGatewayUsesAdmittedAccount(t *testing.T) {
	g := newGWRig(t, gwRecord(gwBaseURL), nil, false, admitOne)
	p, err := g.call(t)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if _, ok := p.(interface{ Unwrap() provider.ModelProvider }).Unwrap().(*anthropic.Driver); !ok {
		t.Fatalf("built driver is %T, want *anthropic.Driver", p)
	}
	if g.tr.calls() != 1 || !strings.HasPrefix(g.tr.urls[0], gwBaseURL+"/") {
		t.Fatalf("sends = %v, want one to the account base URL", g.tr.urls)
	}
	if h := g.tr.headers[0]; h["authorization"] != "Bearer "+g.canary || h["x-api-key"] != "" || g.vault.n != 0 {
		t.Fatalf("request lacked exactly the source's bearer token, or the vault was read %d times", g.vault.n)
	}
}

func TestResolveGatewayRefusesUnadmitted(t *testing.T) {
	g := newGWRig(t, gwRecord(gwBaseURL), nil, false, admitNone)
	// Refused by Resolve itself, before any driver exists.
	p, err := g.res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1", Model: "model-a"})
	if p != nil {
		t.Fatal("Resolve built a driver for an unadmitted account")
	}
	var ce *cascade.Error
	want := "gateway account acct is not admitted; run `cascade config accept` after reviewing [gateways.acct]"
	if !errors.As(err, &ce) || ce.Kind != cascade.KindPermissionDenied || ce.Msg != want {
		t.Fatalf("err = %v, want KindPermissionDenied %q", err, want)
	}
	if g.tr.calls() != 0 || g.vault.n != 0 {
		t.Fatalf("sends = %d, vault reads = %d, want 0 and 0", g.tr.calls(), g.vault.n)
	}
}

func TestResolveGatewayRefusesBaseURLMismatch(t *testing.T) {
	g := newGWRig(t, gwRecord("https://other.gateway.invalid"), nil, false, admitOne)
	_, err := g.call(t)
	wantErr(t, err, cascade.KindConflict, `provider "gw1"`)
	if g.tr.calls() != 0 {
		t.Fatalf("sends = %d, want 0", g.tr.calls())
	}
}

func TestResolveGatewayHelperFailure(t *testing.T) {
	g := newGWRig(t, gwRecord(gwBaseURL), nil, true, admitOne)
	before := append([]registry.LaneRecord(nil), g.store.lanes...)
	_, err := g.call(t)
	wantErr(t, err, cascade.KindUnavailable, g.helper)
	if after, n := g.store.lane(t); !reflect.DeepEqual([]registry.LaneRecord{after}, before) || n != 0 {
		t.Fatalf("lane row changed (%d upserts): %+v -> %+v", n, before, after)
	}
	if g.tr.calls() != 0 || g.vault.n != 0 {
		t.Fatalf("sends = %d, vault reads = %d, want 0 and 0", g.tr.calls(), g.vault.n)
	}
}

func TestGatewayCanaryAbsentEverywhere(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		fail       bool
		admit      func(gateway.Account) []gateway.Account
	}{
		{"admitted", gwBaseURL, false, admitOne},
		{"unadmitted", gwBaseURL, false, admitNone},
		{"mismatch", "https://other.gateway.invalid", false, admitOne},
		{"helper-failure", gwBaseURL, true, admitOne},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGWRig(t, gwRecord(tc.base), nil, tc.fail, tc.admit)
			_, err := g.call(t)
			if strings.Contains(g.leakText(t, err), g.canary) {
				t.Fatal("canary found in an error, lane row, log line or file")
			}
			if tc.name == "admitted" && (g.tr.calls() != 1 || g.tr.headers[0]["authorization"] != "Bearer "+g.canary) {
				t.Fatal("the canary never reached the transport; the scan proves nothing")
			}
		})
	}
}

func TestResolveGatewayTamperedBaseURLSendsNothing(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "providers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, gwClock(), "", ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := registry.NewRegistry(db, gwClock())
	if err := errors.Join(reg.UpsertProvider(ctx, gwRecord(gwBaseURL)), reg.UpsertLane(ctx, registry.LaneRecord{LaneName: "lane-1",
		ProviderName: "gw1", Weight: 1, Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable})); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE provider_records SET base_url = ? WHERE name = ?`, "https://tampered.invalid", "gw1"); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	g := newGWRig(t, gwRecord(gwBaseURL), reg, false, admitOne)
	_, err = g.call(t)
	wantErr(t, err, cascade.KindConflict, `provider "gw1"`)
	if g.tr.calls() != 0 {
		t.Fatalf("sends = %d after a tampered base URL, want 0", g.tr.calls())
	}
}

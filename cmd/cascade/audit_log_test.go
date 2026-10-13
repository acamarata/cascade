// Purpose: proof that the one audit-log helper redacts what it stores and
//
//	refuses when it cannot. Every assertion reads the PERSISTED record bytes
//	straight from the store, never the Event the test handed Append.
//
// Constraints: file-vault custody under a throwaway data dir (the TestMain
//
//	selector; never the host keychain), frozen clock, in-memory audit store.
//
// SPORT: cmd/cascade/audit_log (ADD, tests, P1-SEC-06).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// auditTestEpoch is the instant the frozen clocks in this file start at.
var auditTestEpoch = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

// auditTestPaths gives a test its own data dir and a clean process
// firewall, restored when the test ends, so a test that opens an audit log
// is never refused for a data dir another test bound.
func auditTestPaths(t *testing.T) runtime.PathProvider {
	t.Helper()
	resetProcessFirewall(t)
	return firewallTestPaths(t)
}

// auditVault is the vault custody a test seeds and breaks: the process
// firewall is bound over it by auditVaultFixture.
type auditVault struct {
	secrets.Custody
	listErr error
}

func (v *auditVault) List(ctx context.Context) ([]string, error) {
	if v.listErr != nil {
		return nil, v.listErr
	}
	return v.Custody.List(ctx)
}

// auditVaultFixture binds the process firewall over a file vault the test
// can write to and make fail, and returns the paths and that vault.
func auditVaultFixture(t *testing.T) (runtime.PathProvider, *auditVault) {
	t.Helper()
	paths := auditTestPaths(t)
	vault := &auditVault{}
	next := selectProcessCustody
	selectProcessCustody = func(cfg secrets.Config) (secrets.Custody, error) {
		custody, err := next(cfg)
		vault.Custody = custody
		return vault, err
	}
	if _, err := processFirewallFor(paths); err != nil {
		t.Fatalf("binding the process firewall: %v", err)
	}
	return paths, vault
}

// storedAuditRecords returns every record's persisted bytes, joined.
func storedAuditRecords(t *testing.T, store provider.Store) (joined string, count int) {
	t.Helper()
	ctx := context.Background()
	it, err := store.Scan(ctx, string(storage.DomainAudit), "rec:")
	if err != nil {
		t.Fatalf("scanning audit records: %v", err)
	}
	defer func() { _ = it.Close() }()
	var b strings.Builder
	for it.Next(ctx) {
		b.Write(it.Value())
		count++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("scanning audit records: %v", err)
	}
	return b.String(), count
}

func redactionEvent(action string, explain map[string]string) audit.Event {
	body, _ := json.Marshal(explain)
	return audit.Event{
		Kind: audit.KindPolicyDecide, Actor: "user", Action: action,
		ParamsHash: audit.HashParams([]byte(action)), RiskLevel: "low", Verdict: "allow",
		Explain: body, Outcome: "applied",
	}
}

func TestAuditAppendRedactsVaultValue(t *testing.T) {
	const canary = "canary-plain-value-7731" // shapeless: no detector rule matches it
	ctx := context.Background()
	paths, vault := auditVaultFixture(t)
	if err := vault.Set(ctx, "canary-token", []byte(canary)); err != nil {
		t.Fatalf("seeding the vault: %v", err)
	}
	clock := testkit.NewFrozenClock(auditTestEpoch)
	store := storetest.NewMemStore()
	log, err := openAuditLog(store, clock, nil, paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	if _, err := log.Append(ctx, redactionEvent("write "+canary, map[string]string{"note": "value " + canary})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	stored, n := storedAuditRecords(t, store)
	if n != 1 || strings.Contains(stored, canary) {
		t.Fatalf("stored %d records; the canary survived in the persisted bytes: %s", n, stored)
	}
	if got := strings.Count(stored, "CANARY_TOKEN"); got != 2 {
		t.Fatalf("persisted bytes name the vault tag %d times, want 2 (action and explain): %s", got, stored)
	}

	// Stub-passing inputs: a redactor that returns content unchanged, and
	// the detector alone (an empty vault), both store the canary.
	for name, redactor := range map[string]audit.Redactor{
		"identity":      identityRedactor{},
		"detector only": auditRedactor{vault: emptyAuditVault{}},
	} {
		leaky := storetest.NewMemStore()
		l := audit.NewWithRedactor(leaky, clock, nil, redactor)
		if _, err := l.Append(ctx, redactionEvent("write "+canary, map[string]string{"note": canary})); err != nil {
			t.Fatalf("%s: Append: %v", name, err)
		}
		if stored, _ := storedAuditRecords(t, leaky); !strings.Contains(stored, canary) {
			t.Fatalf("%s redactor hid the canary; the assertion above cannot tell a real pass from a stub", name)
		}
	}
}

// identityRedactor returns content unchanged: the stub that must fail.
type identityRedactor struct{}

func (identityRedactor) Redact(_ context.Context, content []byte) ([]byte, error) {
	return content, nil
}

// emptyAuditVault holds no values: the detector-only configuration.
type emptyAuditVault struct{}

func (emptyAuditVault) List(context.Context) ([]string, error)      { return nil, nil }
func (emptyAuditVault) Get(context.Context, string) ([]byte, error) { return nil, nil }

func TestAuditAppendRedactsDetectedShape(t *testing.T) {
	ctx := context.Background()
	paths, _ := auditVaultFixture(t)
	token := "ghp_" + strings.Repeat("aB3dE5", 6) // a GitHub-shaped token that is not in the vault
	store := storetest.NewMemStore()
	log, err := openAuditLog(store, testkit.NewFrozenClock(auditTestEpoch), nil, paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	if _, err := log.Append(ctx, redactionEvent("push with "+token, map[string]string{"auth": token})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	stored, _ := storedAuditRecords(t, store)
	if strings.Contains(stored, token) || strings.Contains(stored, "ghp_") {
		t.Fatalf("the detected token survived in the persisted bytes: %s", stored)
	}
}

func TestAuditAppendRefusesWhenRedactorErrors(t *testing.T) {
	ctx := context.Background()
	paths, vault := auditVaultFixture(t)
	store := storetest.NewMemStore()
	log, err := openAuditLog(store, testkit.NewFrozenClock(auditTestEpoch), nil, paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	if _, err := log.Append(ctx, redactionEvent("first", map[string]string{"n": "1"})); err != nil {
		t.Fatalf("Append while the vault is readable: %v", err)
	}
	_, before := storedAuditRecords(t, store)

	vault.listErr = errors.New("vault list failed")
	_, err = log.Append(ctx, redactionEvent("second", map[string]string{"n": "2"}))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Append with a failing vault = %v, want KindIntegrity", err)
	}
	if _, after := storedAuditRecords(t, store); after != before {
		t.Fatalf("a refused append left %d records, had %d", after, before)
	}
}

func TestOpenAuditLogRefusesWithoutCustody(t *testing.T) {
	paths := auditTestPaths(t)
	working := selectProcessCustody
	cause := secrets.ErrNoCustodyAvailable()
	selectProcessCustody = func(secrets.Config) (secrets.Custody, error) { return nil, cause }
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(auditTestEpoch)

	log, err := openAuditLog(store, clock, nil, paths)
	if log != nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("openAuditLog without custody = (%v, %v), want (nil, KindUnavailable)", log, err)
	}
	if _, memoized := auditLogMemo.logs[store]; memoized {
		t.Fatal("a failed open left a log in the memo")
	}
	if _, err := openEffectLog(store, clock, nil, paths); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("openEffectLog without custody = %v, want KindUnavailable", err)
	}

	selectProcessCustody = working
	if log, err = openAuditLog(store, clock, nil, paths); err != nil || log == nil {
		t.Fatalf("openAuditLog once custody returned = (%v, %v); the failure must not be cached", log, err)
	}
}

func TestOpenAuditLogMemoizedPerStore(t *testing.T) {
	paths := auditTestPaths(t)
	clock := testkit.NewFrozenClock(auditTestEpoch)
	storeA, storeB := storetest.NewMemStore(), storetest.NewMemStore()

	first, err := openAuditLog(storeA, clock, nil, paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	again, err := openAuditLog(storeA, testkit.NewFrozenClock(auditTestEpoch), nil, paths)
	if err != nil || again != first {
		t.Fatalf("second open of one store = (%p, %v), want the first log %p", again, err, first)
	}
	other, err := openAuditLog(storeB, clock, nil, paths)
	if err != nil || other == first {
		t.Fatalf("open of another store = (%p, %v), want a distinct log", other, err)
	}
}

func TestOpenEffectLogVerifiesOncePerLog(t *testing.T) {
	ctx := context.Background()
	paths := auditTestPaths(t)
	clock := testkit.NewFrozenClock(auditTestEpoch)
	store := storetest.NewMemStore()

	el, err := openEffectLog(store, clock, nil, paths)
	if err != nil {
		t.Fatalf("openEffectLog: %v", err)
	}
	if again, err := openEffectLog(store, clock, nil, paths); err != nil || again != el {
		t.Fatalf("second openEffectLog = (%p, %v), want %p", again, err, el)
	}
	h, err := el.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: "agent:test",
		Action: "clipboard.clear", Key: "clipboard.clear:t-1", ParamsHash: audit.HashParams([]byte("t-1"))})
	if err != nil {
		t.Fatalf("BeginEffect: %v", err)
	}
	if _, err := el.ConfirmEffect(ctx, h, "cleared"); err != nil {
		t.Fatalf("ConfirmEffect: %v", err)
	}

	// A log whose effect index was damaged is refused before any effect is
	// recorded over it.
	damaged := storetest.NewMemStore()
	log, err := openAuditLog(damaged, clock, nil, paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	if _, err := log.Append(ctx, redactionEvent("seed", map[string]string{"n": "1"})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := damaged.Put(ctx, string(storage.DomainAudit), "eff:stray", []byte(`{"phase":"intent","intent_seq":1}`)); err != nil {
		t.Fatalf("planting a stray effect row: %v", err)
	}
	if _, err := openEffectLog(damaged, clock, nil, paths); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("openEffectLog over a damaged index = %v, want KindIntegrity", err)
	}
	if err := openAuditVerifier(damaged).Verify(ctx); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("openAuditVerifier(damaged).Verify = %v, want KindIntegrity", err)
	}
}

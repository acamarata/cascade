// Purpose: the audit redaction pass against values encoding/json rewrites,
//
//	and the helper's memo, binding and verifier edges.
//
// SPORT: cmd/cascade/audit_log (ADD, tests, P1-SEC-06).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// rawOnlyRedactor is the pass without the JSON-escaped forms: the stub that
// must fail the escaped-value test below.
type rawOnlyRedactor struct{ vault egress.Vault }

func (r rawOnlyRedactor) Redact(ctx context.Context, content []byte) ([]byte, error) {
	detector, err := secrets.ProcessDetector()
	if err != nil {
		return nil, err
	}
	return egress.SubstitutionPass(ctx, r.vault, detector, secrets.NewRewriter(), content)
}

func TestAuditAppendRedactsJSONEscapedVaultValue(t *testing.T) {
	ctx := context.Background()
	paths, vault := auditVaultFixture(t)
	clock := testkit.NewFrozenClock(auditTestEpoch)
	// Each value changes under JSON escaping; none has a shape the detector knows.
	values := map[string]string{
		"quote-pass":  `pass"word-1234`,
		"slash-pass":  `pa\ss\word-1234`,
		"angle-pass":  `pass<word>-1234`,
		"amp-pass":    `pass&word-12345`,
		"newline-key": "pass\nword-12345",
	}
	for name, value := range values {
		if err := vault.Set(ctx, name, []byte(value)); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
	forms := func(value string) []string {
		html, plain := jsonEscapeForms(t, value)
		return []string{value, html, plain}
	}

	store := storetest.NewMemStore()
	log, err := openAuditLog(store, clock, nil, paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	for name, value := range values {
		if _, err := log.Append(ctx, redactionEvent("write "+value, map[string]string{"note": value})); err != nil {
			t.Fatalf("%s: Append: %v", name, err)
		}
	}
	stored, n := storedAuditRecords(t, store)
	if n != len(values) {
		t.Fatalf("stored %d records, want %d", n, len(values))
	}
	for name, value := range values {
		for _, form := range forms(value) {
			if strings.Contains(stored, form) {
				t.Errorf("%s: persisted bytes still hold the form %q", name, form)
			}
		}
	}

	requireRawOnlyPassLeaks(t, paths, values["quote-pass"])
}

// requireRawOnlyPassLeaks is the stub-passing input: the raw-only pass leaves
// the escaped explain form in the stored bytes, so the assertions above can
// tell a real pass from a stub.
func requireRawOnlyPassLeaks(t *testing.T, paths runtime.PathProvider, value string) {
	t.Helper()
	fw, _ := processFirewallFor(paths)
	leaky := storetest.NewMemStore()
	l := audit.NewWithRedactor(leaky, testkit.NewFrozenClock(auditTestEpoch), nil, rawOnlyRedactor{vault: fw.vault})
	if _, err := l.Append(context.Background(), redactionEvent("write "+value, map[string]string{"note": value})); err != nil {
		t.Fatalf("raw-only Append: %v", err)
	}
	html, _ := jsonEscapeForms(t, value)
	if leakedText, _ := storedAuditRecords(t, leaky); !strings.Contains(leakedText, html) {
		t.Fatal("the raw-only pass hid the escaped form; the assertions cannot tell a real pass from a stub")
	}
}

func TestOpenAuditLogChecksPathsOnAMemoHit(t *testing.T) {
	pathsA := auditTestPaths(t)
	clock := testkit.NewFrozenClock(auditTestEpoch)
	store := storetest.NewMemStore()
	first, err := openAuditLog(store, clock, nil, pathsA)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	if again, err := openAuditLog(store, clock, nil, pathsA); err != nil || again != first {
		t.Fatalf("same paths, same store = (%p, %v), want %p", again, err, first)
	}
	if log, err := openAuditLog(store, clock, nil, firewallTestPaths(t)); log != nil || !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("memo hit under another data dir = (%v, %v), want (nil, KindConflict)", log, err)
	}
}

// unhashableStore is a comparable struct type whose interface field holds a
// slice, so using it as a map key panics.
type unhashableStore struct {
	provider.Store
	extra any
}

func TestOpenAuditLogSurvivesAnUnhashableStore(t *testing.T) {
	paths := auditTestPaths(t)
	store := unhashableStore{Store: storetest.NewMemStore(), extra: []int{1}}
	a, err := openAuditLog(store, testkit.NewFrozenClock(auditTestEpoch), nil, paths)
	if err != nil || a == nil {
		t.Fatalf("openAuditLog over an unhashable store = (%v, %v)", a, err)
	}
	if b, _ := openAuditLog(store, testkit.NewFrozenClock(auditTestEpoch), nil, paths); b == a {
		t.Fatal("an unhashable store got a shared log")
	}
}

// scanFailStore fails every scan, as a store that went away mid-check does.
type scanFailStore struct{ provider.Store }

func (*scanFailStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, cascade.New(cascade.KindUnavailable, "test: scan failed")
}

func TestOpenEffectLogKeepsTheKindOfAStoreFailure(t *testing.T) {
	paths := auditTestPaths(t)
	store := &scanFailStore{Store: storetest.NewMemStore()}
	if _, err := openEffectLog(store, testkit.NewFrozenClock(auditTestEpoch), nil, paths); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("openEffectLog over a failing store = %v, want KindUnavailable (not rewrapped as integrity)", err)
	}
}

func TestAuditVerifierCannotAppend(t *testing.T) {
	if _, ok := openAuditVerifier(storetest.NewMemStore()).(audit.Writer); ok {
		t.Fatal("the read-only verifier is an audit.Writer")
	}
}

// jsonEscapeForms returns the inside of the JSON string literals
// encoding/json writes for s, with and without HTML escaping.
func jsonEscapeForms(t *testing.T, s string) (html, plain string) {
	t.Helper()
	h, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		t.Fatal(err)
	}
	p := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return string(h[1 : len(h)-1]), string(p[1 : len(p)-1])
}

// The escaped forms follow the exact-value floor of the egress pass: a value
// of seven bytes is matched in neither form, one of eight in both.
func TestAuditRedactionFloorMatchesEgress(t *testing.T) {
	ctx := context.Background()
	paths, vault := auditVaultFixture(t)
	short, long := `ab"cd<e`, `ab"cd<ef`
	for name, value := range map[string]string{"short-val": short, "long-val": long} {
		if err := vault.Set(ctx, name, []byte(value)); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
	store := storetest.NewMemStore()
	log, err := openAuditLog(store, testkit.NewFrozenClock(auditTestEpoch), nil, paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	e := redactionEvent("write "+short+" and "+long, nil)
	e.Explain = json.RawMessage(marshalJSON(t, map[string]string{"s": short, "l": long}, true))
	if _, err := log.Append(ctx, e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	stored, _ := storedAuditRecords(t, store)
	if strings.Contains(stored, `ab\"cd\u003cef`) || strings.Contains(stored, long) {
		t.Errorf("the 8-byte value survived in clear or escaped: %s", stored)
	}
	if !strings.Contains(stored, short) && !strings.Contains(stored, `ab\"cd\u003ce`) {
		t.Errorf("the 7-byte value was redacted although the exact pass ignores it: %s", stored)
	}
}

// An unshared log (nil store) is refused by openEffectLog and leaves nothing
// behind in the effect memo.
func TestOpenEffectLogNilStoreLeavesNoMemo(t *testing.T) {
	paths := auditTestPaths(t)
	auditLogMemo.mu.Lock()
	before := len(auditLogMemo.effects)
	auditLogMemo.mu.Unlock()
	if _, err := openEffectLog(nil, testkit.NewFrozenClock(auditTestEpoch), nil, paths); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("openEffectLog(nil store) = %v, want KindUnavailable", err)
	}
	if err := openAuditVerifier(nil).Verify(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("openAuditVerifier(nil).Verify = %v, want KindUnavailable", err)
	}
	auditLogMemo.mu.Lock()
	defer auditLogMemo.mu.Unlock()
	if len(auditLogMemo.effects) != before {
		t.Fatalf("effect memo grew from %d to %d for an unshared log", before, len(auditLogMemo.effects))
	}
}

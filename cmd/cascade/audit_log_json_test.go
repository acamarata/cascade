// Purpose: the audit redaction pass against JSON carried inside JSON, JSON
//
//	quoted in an action, and an explain written without HTML escaping.
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
)

// allJSONForms returns v and its JSON-escaped forms up to three levels, with
// and without HTML escaping, the way encoding/json writes them.
func allJSONForms(t *testing.T, v string) []string {
	t.Helper()
	forms, level := []string{v}, []string{v}
	for d := 0; d < 3; d++ { // fixed, not jsonEscapeDepth: a lowered constant must not hide the leak
		var next []string
		for _, f := range level {
			html, plain := jsonEscapeForms(t, f)
			next = append(next, html, plain)
		}
		forms, level = append(forms, next...), next
	}
	return forms
}

// marshalJSON encodes v with HTML escaping on or off.
func marshalJSON(t *testing.T, v any, escapeHTML bool) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// seedVault stores each name/value pair in the test vault.
func seedVault(t *testing.T, vault *auditVault, values map[string]string) {
	t.Helper()
	for name, value := range values {
		if err := vault.Set(context.Background(), name, []byte(value)); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
}

// appendAll appends every event through the process log over a fresh store
// and returns the persisted record bytes.
func appendAll(t *testing.T, paths runtime.PathProvider, events map[string]audit.Event) string {
	t.Helper()
	store := storetest.NewMemStore()
	log, err := openAuditLog(store, testkit.NewFrozenClock(auditTestEpoch), nil, paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	for name, event := range events {
		if _, err := log.Append(context.Background(), event); err != nil {
			t.Fatalf("%s: Append: %v", name, err)
		}
	}
	stored, n := storedAuditRecords(t, store)
	if n != len(events) {
		t.Fatalf("stored %d records, want %d", n, len(events))
	}
	return stored
}

// requireRedacted fails if any JSON form (up to three levels) of any value is
// still in stored, or if a tag name is missing.
func requireRedacted(t *testing.T, stored string, values []string, tagNames ...string) {
	t.Helper()
	for _, value := range values {
		for _, form := range allJSONForms(t, value) {
			if strings.Contains(stored, form) {
				t.Errorf("persisted bytes still hold the form %q", form)
			}
		}
	}
	for _, tag := range tagNames {
		if !strings.Contains(stored, tag) {
			t.Errorf("persisted bytes lack the tag name %s: the match was dropped instead of tagged", tag)
		}
	}
}

// eventWith returns a policy-decision event with the given action and explain.
func eventWith(action, explain string) audit.Event {
	e := redactionEvent(action, nil)
	e.Explain = json.RawMessage(explain)
	return e
}

// JSON carried inside a JSON string (one, two and three levels), JSON quoted
// in an action, and an explain written without HTML escaping all hold a vault
// value in a form a one-level escape does not match.
func TestAuditAppendRedactsNestedAndUnescapedJSON(t *testing.T) {
	paths, vault := auditVaultFixture(t)
	quote, angle := `pass"word-1234`, `pa"ss<w>-1234`
	seedVault(t, vault, map[string]string{"quote-pass": quote, "angle-pass": angle})
	l1 := marshalJSON(t, map[string]string{"t": quote}, true)
	l2 := marshalJSON(t, map[string]string{"b": l1}, true)
	l3 := marshalJSON(t, map[string]string{"c": l2}, true)
	stored := appendAll(t, paths, map[string]audit.Event{
		"one level":      eventWith("plain action", marshalJSON(t, map[string]string{"body": l1}, true)),
		"two levels":     eventWith("plain action", marshalJSON(t, map[string]string{"body": l2}, true)),
		"three levels":   eventWith("plain action", marshalJSON(t, map[string]string{"body": l3}, true)),
		"action":         eventWith("call "+l1, `{"n":1}`),
		"no html escape": eventWith("plain action", marshalJSON(t, map[string]string{"t": angle}, false)),
	})
	requireRedacted(t, stored, []string{quote, angle}, "QUOTE_PASS", "ANGLE_PASS")
}

// A vault value that embeds another value's escaped form is tagged whole: the
// rewriter picks the largest span across values and forms, so the shorter
// value cannot cut the longer one and leave its remainder in clear.
func TestAuditAppendRedactsEmbeddingValueWhole(t *testing.T) {
	paths, vault := auditVaultFixture(t)
	inner, plainInner := `tok"en-12345`, "plainpass123"
	outer := `{"user":"svc-PRIVATEPART-998877","password":"tok\"en-12345"}`
	outer2 := `{"user":"svc-OTHERPART-112233","password":"plainpass123"}`
	seedVault(t, vault, map[string]string{"inner-pass": inner, "outer-blob": outer, "plain-inner": plainInner, "outer-two": outer2})
	stored := appendAll(t, paths, map[string]audit.Event{
		"quoted outer":   eventWith("use "+outer, marshalJSON(t, map[string]string{"cfg": outer}, true)),
		"plain embedded": eventWith("use "+outer2, marshalJSON(t, map[string]string{"cfg": outer2}, true)),
	})
	for _, rest := range []string{"PRIVATEPART", "998877", "OTHERPART", "112233"} {
		if strings.Contains(stored, rest) {
			t.Errorf("the remainder %q of an embedding vault value is still in the persisted bytes", rest)
		}
	}
	requireRedacted(t, stored, []string{inner, plainInner, outer, outer2}, "OUTER_BLOB", "OUTER_TWO")
}

// The labels the hits carry are the ones the egress exact-value pass writes,
// for names the tag grammar has to normalise.
func TestAuditHitLabelsMatchEgressPass(t *testing.T) {
	ctx := context.Background()
	_, vault := auditVaultFixture(t)
	values := map[string]string{"canary-token": "canary-value-0001", "9lives": "nine-lives-0002", "dot.name_x": "dotted-name-0003"}
	seedVault(t, vault, values)
	snap, err := secrets.NewSnapshotVault(ctx, vault)
	if err != nil {
		t.Fatal(err)
	}
	detector, err := secrets.ProcessDetector()
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, v := range values {
		text.WriteString("x " + v + " y ")
	}
	want, err := egress.SubstitutionPass(ctx, snap, detector, secrets.NewRewriter(), []byte(text.String()))
	if err != nil {
		t.Fatal(err)
	}
	rewritten, err := secrets.NewRewriter().Rewrite([]byte(text.String()), snap.ExactHits([]byte(text.String()), 0))
	if err != nil {
		t.Fatal(err)
	}
	if string(rewritten.Text) != string(want) {
		t.Fatalf("hits rewrite to %q, the egress pass writes %q", rewritten.Text, want)
	}
}

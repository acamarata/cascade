package secrets

// Purpose: the rehydrate direction's behaviour tests - round trip against
//   the real rewriter, the fail-closed refusals, the Zero obligation, and
//   the compile-time proof that the type renders nowhere.
// Constraints: hermetic vault (in-memory custody, no keychain, no
//   network); the elevation gate in the fixture REFUSES, so any test that
//   passes proves rehydration did not take the elevated path.
// SPORT: REHYDRATOR: ADD (tests).

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
)

// refusingGate refuses every elevated verb. The rehydrator is wired
// behind it on purpose: R-21.229 requires rehydration to resolve tags
// without raising the elevated path, and a gate that says yes could not
// tell the two apart.
type refusingGate struct{ calls int }

func (g *refusingGate) Authorize(context.Context, string) error {
	g.calls++
	return errors.New("elevation refused")
}

// newRehydratorFixture returns a rehydrator over an in-memory vault
// preloaded with entries, plus the gate that must never be consulted.
func newRehydratorFixture(t *testing.T, entries map[string]string) (*Rehydrator, *refusingGate) {
	t.Helper()
	custody := newMemCustody()
	for name, value := range entries {
		if err := custody.Set(context.Background(), name, []byte(value)); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
	gate := &refusingGate{}
	broker, err := NewBroker(custody, gate)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	r, err := NewRehydrator(broker)
	if err != nil {
		t.Fatalf("NewRehydrator: %v", err)
	}
	return r, gate
}

func TestRehydrateRoundTrip(t *testing.T) {
	const value = "s3cret-passphrase-value"
	original := []byte("run --key " + value + " now")
	r, gate := newRehydratorFixture(t, map[string]string{"DEPLOY_KEY": value})

	rewritten, err := NewRewriter().Rewrite(original, []DetectionHit{{
		Class: ClassAPIKey, Pattern: "vault-exact", Offset: 10, Len: len(value),
		Confidence: ConfidenceProven, SuggestedName: "DEPLOY_KEY",
	}})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if bytes.Contains(rewritten.Text, []byte(value)) {
		t.Fatalf("the rewriter left the raw value in the turn")
	}

	rc, err := r.Rehydrate(context.Background(), rewritten.Text)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	defer rc.Zero()
	if !bytes.Equal(rc.Data, original) {
		t.Fatalf("round trip mismatch:\n got %q\nwant %q", rc.Data, original)
	}
	if gate.calls != 0 {
		t.Fatalf("rehydration consulted the elevation gate %d times; it must use the unelevated read", gate.calls)
	}
}

func TestRehydrateUnknownName(t *testing.T) {
	r, _ := newRehydratorFixture(t, map[string]string{"KNOWN": "value-one"})
	rc, err := r.Rehydrate(context.Background(), []byte("a <apikey>MISSING</apikey> b"))
	if !errors.Is(err, ErrVaultKeyNotFound) {
		t.Fatalf("want ErrVaultKeyNotFound, got %v", err)
	}
	if rc != nil {
		t.Fatalf("an unknown name must return no content, got %+v", rc)
	}
}

func TestRehydrateSecondNameMissingLeaksNothing(t *testing.T) {
	const first = "first-secret-value"
	r, _ := newRehydratorFixture(t, map[string]string{"FIRST": first})
	content := []byte("<apikey>FIRST</apikey> then <apikey>SECOND</apikey>")
	rc, err := r.Rehydrate(context.Background(), content)
	if !errors.Is(err, ErrVaultKeyNotFound) {
		t.Fatalf("want ErrVaultKeyNotFound, got %v", err)
	}
	if rc != nil {
		t.Fatalf("a failed second lookup must return no partial content, got %q", rc.Data)
	}
	if bytes.Contains([]byte(fmt.Sprintf("%v", err)), []byte(first)) {
		t.Fatalf("the refusal carries the first tag's value")
	}
}

func TestRehydrateMalformedTagRefuses(t *testing.T) {
	// Transcribed from the grammar in tags.go, not from the scanner: a
	// run that opens like a tag and does not satisfy the production is a
	// refusal on this direction, never prose.
	cases := map[string]string{
		"truncated":        "before <password>NAME",
		"mismatched close": "<password>NAME</apikey>",
		"illegal name":     "<token>not upper snake</token>",
		"pii missing kind": `<pii>SSN_FIELD</pii>`,
		"pii unknown kind": `<pii kind="passport">FIELD</pii>`,
		"empty name":       "<connstr></connstr>",
	}
	r, _ := newRehydratorFixture(t, map[string]string{"NAME": "value-one"})
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			rc, err := r.Rehydrate(context.Background(), []byte(content))
			if !errors.Is(err, ErrMalformedTag) {
				t.Fatalf("want ErrMalformedTag, got %v", err)
			}
			if rc != nil {
				t.Fatalf("a malformed tag must return no content, got %q", rc.Data)
			}
		})
	}
}

func TestRehydrateNilAndEmptyInput(t *testing.T) {
	r, _ := newRehydratorFixture(t, nil)
	for _, in := range [][]byte{nil, {}} {
		rc, err := r.Rehydrate(context.Background(), in)
		if err != nil || rc != nil {
			t.Fatalf("empty input must be (nil, nil), got (%v, %v)", rc, err)
		}
	}
}

func TestRehydrateUntaggedContentIsCopied(t *testing.T) {
	r, _ := newRehydratorFixture(t, nil)
	rc, err := r.Rehydrate(context.Background(), []byte("plain prose </password> only"))
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	defer rc.Zero()
	if string(rc.Data) != "plain prose </password> only" {
		t.Fatalf("untagged content changed: %q", rc.Data)
	}
}

func TestRehydrateRefusesNonUTF8(t *testing.T) {
	r, _ := newRehydratorFixture(t, nil)
	if _, err := r.Rehydrate(context.Background(), []byte{0xff, 0xfe}); !errors.Is(err, ErrMalformedTag) {
		t.Fatalf("want ErrMalformedTag for invalid UTF-8, got %v", err)
	}
}

func TestRehydrateNeedsABroker(t *testing.T) {
	if _, err := NewRehydrator(nil); err == nil {
		t.Fatalf("a rehydrator with no broker must be refused")
	}
	var zero *Rehydrator
	if _, err := zero.Rehydrate(context.Background(), []byte("x")); err == nil {
		t.Fatalf("a nil rehydrator must refuse rather than proceed")
	}
}

func TestRehydratedContentZero(t *testing.T) {
	const value = "zero-me-completely"
	r, _ := newRehydratorFixture(t, map[string]string{"ZERO_ME": value})
	rc, err := r.Rehydrate(context.Background(), []byte("<token>ZERO_ME</token>"))
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	backing := rc.Data[:cap(rc.Data)]
	rc.Zero()
	for i, b := range backing {
		if b != 0 {
			t.Fatalf("byte %d of the backing array survived Zero: %q", i, b)
		}
	}
	rc.Zero()
	var nilContent *RehydratedContent
	nilContent.Zero()
}

// TestRehydratedContentRendersNowhere is the compile-time half of the
// anti-logging invariant: the five interfaces below are every standard
// way a value gets turned into text by something that was handed it.
func TestRehydratedContentRendersNowhere(t *testing.T) {
	var v any = &RehydratedContent{Data: []byte("x")}
	if _, ok := v.(fmt.Stringer); ok {
		t.Fatal("RehydratedContent must not implement fmt.Stringer")
	}
	if _, ok := v.(fmt.GoStringer); ok {
		t.Fatal("RehydratedContent must not implement fmt.GoStringer")
	}
	if _, ok := v.(encoding.TextMarshaler); ok {
		t.Fatal("RehydratedContent must not implement encoding.TextMarshaler")
	}
	if _, ok := v.(json.Marshaler); ok {
		t.Fatal("RehydratedContent must not implement json.Marshaler")
	}
	if _, ok := v.(slog.LogValuer); ok {
		t.Fatal("RehydratedContent must not implement slog.LogValuer")
	}
}

package egress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSubstitutionExactValueShapeless is the property the exact-value
// pass exists for: a stored secret with no credential shape, no prefix
// and entropy below any sane floor is still substituted, because the
// operator stored it and that is the only signal that matters.
func TestSubstitutionExactValueShapeless(t *testing.T) {
	const shapeless = "correct-horse-battery-staple"
	engine := newTestEngine(t, map[string][]byte{"WIFI_PASSWORD": []byte(shapeless)})

	out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal,
		[]byte("the passphrase is "+shapeless+" for now"))
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	if strings.Contains(string(out), shapeless) {
		t.Fatalf("the shapeless stored value survived substitution: %q", string(out))
	}
	if !strings.Contains(string(out), "<apikey>WIFI_PASSWORD</apikey>") {
		t.Fatalf("no vault-reference tag in %q", string(out))
	}
}

// TestSubstitutionDetectorPassCatchesUnstoredSecret proves the second
// pass still runs: a credential the vault has never seen is caught by the
// detector this package composes rather than by a detector of its own.
func TestSubstitutionDetectorPassCatchesUnstoredSecret(t *testing.T) {
	const unstored = "sk-live-AAAA1234567890BBBBccccDDDD"
	engine := newTestEngine(t, nil)
	out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal,
		[]byte(`{"api_key":"`+unstored+`"}`))
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	if strings.Contains(string(out), unstored) {
		t.Fatalf("an unstored credential survived substitution: %q", string(out))
	}
}

// TestSubstitutionIsIdempotent runs the firewall over its own output and
// asserts the second pass changes nothing.
func TestSubstitutionIsIdempotent(t *testing.T) {
	const stored = "correct-horse-battery-staple"
	engine := newTestEngine(t, map[string][]byte{"WIFI_PASSWORD": []byte(stored)})
	ctx := context.Background()
	first, err := engine.InterceptClass(ctx, EgressClassMCP, TierInternal,
		[]byte("key sk-live-AAAA1234567890BBBBccccDDDD and phrase "+stored))
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	second, err := engine.InterceptClass(ctx, EgressClassMCP, TierInternal, first)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("substitution is not idempotent:\nfirst:  %q\nsecond: %q", string(first), string(second))
	}
	third, err := engine.InterceptClass(ctx, EgressClassMCP, TierInternal, second)
	if err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if string(second) != string(third) {
		t.Fatalf("substitution is not idempotent at the third pass: %q", string(third))
	}
}

// TestSubstitutionIsDeterministic asserts identical input gives identical
// bytes, including when the vault lists its names in a different order.
func TestSubstitutionIsDeterministic(t *testing.T) {
	values := map[string][]byte{
		"ALPHA_SECRET": []byte("alpha-value-0123456789"),
		"BETA_SECRET":  []byte("beta-value-9876543210"),
		"WRAPPING":     []byte("prefix-alpha-value-0123456789-suffix"),
	}
	content := []byte("prefix-alpha-value-0123456789-suffix and beta-value-9876543210")
	var want string
	for i := 0; i < 8; i++ {
		engine := newTestEngine(t, values)
		out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal, content)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if i == 0 {
			want = string(out)
			continue
		}
		if string(out) != want {
			t.Fatalf("run %d differs:\nwant %q\ngot  %q", i, want, string(out))
		}
	}
	if strings.Contains(want, "alpha-value-0123456789") {
		t.Fatalf("the enclosed value survived: %q", want)
	}
}

// TestSubstitutionFailsClosedOnVaultError proves a vault that cannot be
// read stops the write rather than degrading to the detector alone.
func TestSubstitutionFailsClosedOnVaultError(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "vault unreachable")
	for _, vault := range []Vault{&mapVault{listErr: boom}, &mapVault{values: map[string][]byte{"A": []byte("x")}, getErr: boom}} {
		engine := newEngineOn(t, DefaultRegistry(), vault)
		out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal, []byte("payload"))
		if err == nil {
			t.Fatal("a failing vault did not stop the write")
		}
		if out != nil {
			t.Fatalf("a failing vault returned %d bytes to write", len(out))
		}
	}
}

// TestSubstitutionFailsClosedOnUnparseableContent proves content the
// rewriter cannot parse is refused rather than passed through. Invalid
// UTF-8 is the unparseable case the rewriter names.
func TestSubstitutionFailsClosedOnUnparseableContent(t *testing.T) {
	engine := newTestEngine(t, nil)
	out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal,
		[]byte{'o', 'k', 0xff, 0xfe, 'z'})
	if err == nil {
		t.Fatal("content the rewriter cannot parse was admitted")
	}
	if out != nil {
		t.Fatalf("unparseable content returned %d bytes to write", len(out))
	}
}

// TestSubstitutionPassRequiresItsCollaborators covers the constructor
// error paths that would otherwise leave a pass silently skipped.
func TestSubstitutionPassRequiresItsCollaborators(t *testing.T) {
	ctx := context.Background()
	if _, err := SubstitutionPass(ctx, nil, newDetector(t), nil, []byte("x")); !errors.Is(err, ErrNoVault) {
		t.Fatalf("a nil vault gave %v, want ErrNoVault", err)
	}
	if _, err := SubstitutionPass(ctx, &mapVault{}, nil, nil, []byte("x")); err == nil {
		t.Fatal("a nil detector was accepted")
	}
	if _, err := NewEngine(nil, &mapVault{}, newDetector(t)); err == nil {
		t.Fatal("NewEngine accepted a nil registry")
	}
	if _, err := NewEngine(NewRegistry(), nil, newDetector(t)); !errors.Is(err, ErrNoVault) {
		t.Fatalf("NewEngine with no vault gave %v, want ErrNoVault", err)
	}
	if _, err := NewEngine(NewRegistry(), &mapVault{}, nil); err == nil {
		t.Fatal("NewEngine accepted a nil detector")
	}
}

// TestVaultRefNameNormalises pins the reference-name rule: the output is
// always a legal tag NAME and never carries anything about the value.
func TestVaultRefNameNormalises(t *testing.T) {
	cases := map[string]string{
		"WIFI_PASSWORD": "WIFI_PASSWORD",
		"wifi-password": "WIFI_PASSWORD",
		"9lives":        "V_9LIVES",
		"":              "V_",
		"a.b/c":         "A_B_C",
	}
	for in, want := range cases {
		if got := vaultRefName(in); got != want {
			t.Errorf("vaultRefName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestShortStoredValuesAreNotExactMatched documents the stated gap: a
// stored value below the length floor is not matched by the exact pass.
// It is asserted rather than left implicit so the gap cannot quietly grow.
func TestShortStoredValuesAreNotExactMatched(t *testing.T) {
	engine := newTestEngine(t, map[string][]byte{"SHORT": []byte("abc")})
	out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal, []byte("abc def"))
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	if string(out) != "abc def" {
		t.Fatalf("a below-floor value was matched; the floor is %d bytes, got %q", minExactValueBytes, string(out))
	}
}

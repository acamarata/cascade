package cascade

import (
	"errors"
	"strings"
	"testing"
)

// TestNewIDShape asserts a minted identifier is exactly IDLength
// characters drawn from the Crockford alphabet, checked against a LITERAL
// alphabet written out here rather than against the package's own
// constant.
func TestNewIDShape(t *testing.T) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for i := 0; i < 64; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if len(id) != 26 {
			t.Fatalf("NewID returned %d characters, want 26", len(id))
		}
		for _, c := range id {
			if !strings.ContainsRune(alphabet, c) {
				t.Fatalf("NewID emitted %q, which is outside Crockford base32", c)
			}
		}
		if !id.Valid() {
			t.Fatalf("a freshly minted id %q did not validate", id)
		}
	}
}

// TestNewIDIsUnique proves the mint does not repeat itself over a run of
// calls. A predictable identifier is a security defect, not a cosmetic
// one: the approval model uses these as nonces.
func TestNewIDIsUnique(t *testing.T) {
	seen := map[ID]bool{}
	for i := 0; i < 4096; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q within one run", id)
		}
		seen[id] = true
	}
}

// TestParseIDRefusesMalformed is the fail-closed door. Every rejected form
// is spelled out, including the four ambiguous letters Crockford excludes,
// so a future alphabet change that admits them fails here.
func TestParseIDRefusesMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"one character short", "0123456789ABCDEFGHJKMNPQR"},
		{"one character long", "0123456789ABCDEFGHJKMNPQRST"},
		{"lower case", "0123456789abcdefghjkmnpqrs"},
		{"contains I", "I123456789ABCDEFGHJKMNPQRS"},
		{"contains L", "L123456789ABCDEFGHJKMNPQRS"},
		{"contains O", "O123456789ABCDEFGHJKMNPQRS"},
		{"contains U", "U123456789ABCDEFGHJKMNPQRS"},
		{"a path separator", "0123456789ABCDEFGHJKMNPQ/S"},
		{"a null byte", "0123456789ABCDEFGHJKMNPQR\x00"},
		{"multi-byte runes", strings.Repeat("é", 26)},
	} {
		got, err := ParseID(tc.in)
		if err == nil {
			t.Errorf("%s: ParseID(%q) = %q, nil; want a refusal", tc.name, tc.in, got)
		}
		if got != "" {
			t.Errorf("%s: ParseID returned %q alongside its refusal", tc.name, got)
		}
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: ParseID error is %v; want an invalid-input refusal", tc.name, err)
		}
	}
}

// TestParseIDAcceptsWellFormed proves the door is not simply shut: a
// well-formed value round-trips through ParseID and String unchanged.
func TestParseIDAcceptsWellFormed(t *testing.T) {
	minted, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	parsed, err := ParseID(minted.String())
	if err != nil {
		t.Fatalf("ParseID on a minted id: %v", err)
	}
	if parsed != minted {
		t.Errorf("ParseID(%q) = %q; want the same id back", minted, parsed)
	}
}

// TestZeroIDIsNotValid asserts the zero value refuses, so a struct field
// nobody set cannot read as a real identifier.
func TestZeroIDIsNotValid(t *testing.T) {
	var zero ID
	if zero.Valid() {
		t.Error("the zero ID validated")
	}
	if zero.String() != "" {
		t.Errorf("the zero ID renders as %q", zero.String())
	}
}

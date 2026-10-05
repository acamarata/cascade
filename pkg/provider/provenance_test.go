package provider_test

// Purpose (this file): the closed Provenance parse and its spelling.
// Inputs: literal provenance strings.
// Outputs: assertions only.
// Constraints: refusals are checked by Kind AND message.
// SPORT: pkg.provider.untrusted-provenance/ADD (P1-SEC-19).

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestParseProvenanceClosed pins the closed parse: the two exact members
// parse to themselves, "" is untrusted-source with no error, and every
// other spelling is untrusted-source WITH an error naming the value.
func TestParseProvenanceClosed(t *testing.T) {
	for _, name := range []string{"trusted", "untrusted-source"} {
		got, err := provider.ParseProvenance(name)
		if err != nil || string(got) != name || !got.Valid() {
			t.Fatalf("ParseProvenance(%q) = %q, %v; want the member", name, got, err)
		}
	}
	got, err := provider.ParseProvenance("")
	if err != nil || got != provider.ProvenanceUntrustedSource {
		t.Fatalf(`ParseProvenance("") = %q, %v; want untrusted-source, nil`, got, err)
	}
	for _, bad := range []string{"Trusted", "TRUSTED", "trusted ", "untrusted", "untrusted_source", "verified"} {
		got, err := provider.ParseProvenance(bad)
		if got != provider.ProvenanceUntrustedSource {
			t.Errorf("ParseProvenance(%q) = %q, want untrusted-source", bad, got)
		}
		requireInvalidInput(t, err, `"`+bad+`"`, "is not a provenance")
	}
	if s := provider.Provenance("verified").String(); s != "invalid" {
		t.Fatalf(`Provenance("verified").String() = %q, want "invalid"`, s)
	}
	if provider.Provenance("").Valid() {
		t.Fatal("the zero Provenance must not be valid")
	}
}

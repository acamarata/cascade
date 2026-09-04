package secrets

// Purpose: properties of the pattern table itself - that it covers the
//
//	six named classes, that its order is stable and not observable
//	through a shared slice, that every class default name is a name the
//	vault would actually accept, and that the base64-JSON decode gate
//	refuses the blobs it is there to refuse.

import (
	"strings"
	"testing"
)

// TestDefaultRegistryCoversSixClasses pins the contract's "at least six
// named credential classes".
func TestDefaultRegistryCoversSixClasses(t *testing.T) {
	seen := map[Class]bool{}
	for _, pattern := range DefaultRegistry().Patterns() {
		if pattern.Expr == nil {
			t.Fatalf("pattern %q has no expression", pattern.Name)
		}
		if pattern.Weight <= 0 || pattern.Weight > 1 {
			t.Errorf("pattern %q carries weight %v, outside (0,1]", pattern.Name, pattern.Weight)
		}
		seen[pattern.Class] = true
	}
	for _, class := range allClasses {
		if !seen[class] {
			t.Errorf("the default registry has no pattern for %s", class)
		}
	}
}

// TestRegistryPatternsAreACopy: a caller must not be able to reorder the
// table and change which of two overlapping matches wins.
func TestRegistryPatternsAreACopy(t *testing.T) {
	registry := DefaultRegistry()
	first := registry.Patterns()
	first[0] = Pattern{Name: "tampered"}
	if registry.Patterns()[0].Name == "tampered" {
		t.Fatal("Patterns exposed the registry's own slice")
	}
}

// TestClassDefaultNamesAreUsable: every fallback name must pass the same
// validator the vault applies, or a promotion would fail at the last step.
func TestClassDefaultNamesAreUsable(t *testing.T) {
	classes := append(append([]Class(nil), allClasses...), ClassHighEntropy, Class("unknown-class"))
	for _, class := range classes {
		name := defaultNameFor(class)
		if name == "" {
			t.Fatalf("class %s has an empty default name", class)
		}
		if err := validateSecretName(name); err != nil {
			t.Errorf("default name %q for %s is not a usable vault name: %v", name, class, err)
		}
		if name != strings.ToUpper(name) {
			t.Errorf("default name %q is not UPPER_SNAKE", name)
		}
	}
}

// TestBase64JSONDecodeGate covers the decoder that keeps the base64 class
// from firing on every long base64 run in a document.
func TestBase64JSONDecodeGate(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"credential json", "eyJhcGlfa2V5IjoiWFhZWVpaLXNhbXBsZS12YWx1ZS0wMDAxIn0", true},
		{"json without a credential field", "eyJuYW1lIjoiZXhhbXBsZSIsImFnZSI6NDJ9", false},
		{"plain prose base64", "dGhpcyBpcyBqdXN0IG9yZGluYXJ5IHRleHQgd2l0aCBubyBzZWNyZXQ", false},
		{"not base64 at all", "%%%%not-base64-at-all%%%%", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodesToCredentialJSON(tc.input); got != tc.want {
				t.Fatalf("decodesToCredentialJSON(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestPatternsMatchTheirOwnClassOnly guards against a registry edit that
// makes one pattern shadow another: each corpus fixture must be claimed
// by the pattern written for it.
func TestPatternsMatchTheirOwnClassOnly(t *testing.T) {
	d := testDetector(t)
	for file, want := range map[string]Class{
		"api-key.txt": ClassAPIKey, "jwt.txt": ClassJWT, "pem.txt": ClassPEM,
		"conn-str.txt": ClassConnString, "bearer.txt": ClassBearer, "base64-json.txt": ClassBase64JSON,
	} {
		hits := d.Scan(readCorpus(t, file))
		if len(hits) != 1 || hits[0].Class != want {
			t.Errorf("%s: got %+v, want a single %s hit", file, hits, want)
		}
	}
}

// TestTruncatedPEMStillMatches: a private key pasted without its END line
// is still a private key, and the header-only fallback must catch it.
func TestTruncatedPEMStillMatches(t *testing.T) {
	hits := testDetector(t).Scan([]byte("-----BEGIN RSA PRIVATE KEY-----\nMIIEow==\n"))
	if len(hits) == 0 || hits[0].Class != ClassPEM {
		t.Fatalf("a truncated PEM block was not detected: %+v", hits)
	}
}

// TestNonSecretPEMIsIgnored: a certificate is armoured exactly like a
// private key and is not secret. Matching it would be the false positive
// that gets the detector switched off.
func TestNonSecretPEMIsIgnored(t *testing.T) {
	content := []byte("-----BEGIN CERTIFICATE-----\nMIIB1jCCAX\n-----END CERTIFICATE-----\n")
	for _, hit := range testDetector(t).Scan(content) {
		if hit.Class == ClassPEM {
			t.Fatalf("a certificate was reported as a private key: %+v", hit)
		}
	}
}

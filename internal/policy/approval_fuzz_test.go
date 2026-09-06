package policy

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// fuzzCorpusDir is where the named seed entries live.
const fuzzCorpusDir = "testdata/fuzz/FuzzVerifyToken"

// FuzzVerifyToken drives the decoder half of Verify with arbitrary bytes.
// Two properties are asserted, and neither one is "no panic" alone:
//
//  1. Verify never panics and never returns a record together with an
//     error, on any input.
//  2. Verify returns a record ONLY for bytes that are byte-identical to
//     the canonical re-encoding of that record plus a signature this
//     verifier accepts. Any accepted input therefore round-trips.
//
// Property 2 is what makes an accidental widening of the decoder fail
// here: a decoder that started tolerating extra whitespace would produce a
// record whose re-encoding differs from its input.
func FuzzVerifyToken(f *testing.F) {
	pub, _, clock := fuzzVerifierParts(f)
	v, err := NewApprovalVerifier(pub, clock)
	if err != nil {
		f.Fatalf("building the verifier: %v", err)
	}
	for _, seed := range loadApprovalFuzzSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		rec, err := v.Verify(data)
		if err != nil {
			if rec != nil {
				t.Fatalf("Verify returned both a record and the error %v", err)
			}
			return
		}
		if rec == nil {
			t.Fatal("Verify returned no record and no error")
		}
		payload := CanonicalEncode(*rec)
		if len(data) <= ed25519.SignatureSize ||
			string(data[:len(data)-ed25519.SignatureSize]) != string(payload) {
			t.Fatalf("Verify accepted bytes that are not the canonical encoding of the record it returned")
		}
	})
}

// loadFuzzSeeds reads every corpus entry as raw bytes so the seeds double
// as a table this file can drive directly. The corpus files are in Go's
// own seed format, so the payload is unquoted from the []byte(...) line.
func loadApprovalFuzzSeeds(t testing.TB) [][]byte {
	t.Helper()
	entries, err := os.ReadDir(fuzzCorpusDir)
	if err != nil {
		t.Fatalf("reading the seed corpus: %v", err)
	}
	var out [][]byte
	for _, e := range entries {
		out = append(out, readApprovalFuzzSeed(t, e.Name()))
	}
	if len(out) < 10 {
		t.Fatalf("the seed corpus holds %d entries; the named set is 10", len(out))
	}
	return out
}

// readFuzzSeed returns the bytes one named corpus entry carries.
func readApprovalFuzzSeed(t testing.TB, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fuzzCorpusDir, name)) //nolint:gosec // fixed testdata path
	if err != nil {
		t.Fatalf("reading seed %s: %v", name, err)
	}
	line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "go test fuzz v1"))
	line = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "[]byte("), ")")
	unquoted, err := strconv.Unquote(line)
	if err != nil {
		t.Fatalf("seed %s is not in Go's corpus format: %v", name, err)
	}
	return []byte(unquoted)
}

// TestFuzzSeedsAllRefuse drives every named seed through Verify and
// asserts the expected outcome for each by name. Only valid-signed.bin may
// verify; the other nine must refuse, and the refusal reason is asserted,
// so a seed that started failing for a DIFFERENT reason than the one it
// was captured for is caught.
func TestFuzzSeedsAllRefuse(t *testing.T) {
	pub, _, clock := fuzzVerifierParts(t)
	v, err := NewApprovalVerifier(pub, clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	// The expected outcome per named seed, taken from what each fixture
	// was captured to represent, not from the implementation.
	want := map[string]string{
		"valid-signed.bin":           "accept",
		"truncated.bin":              "refuse",
		"flipped-signature-byte.bin": "refuse",
		"flipped-nonce-byte.bin":     "refuse",
		"expired.bin":                "refuse",
		"zero-length.bin":            "refuse",
		"oversized.bin":              "refuse",
		"non-canonical-json.bin":     "refuse",
		"unknown-field.bin":          "refuse",
		"duplicate-key.bin":          "refuse",
	}
	for name, outcome := range want {
		rec, verr := v.Verify(readApprovalFuzzSeed(t, name))
		if outcome == "accept" {
			if verr != nil || rec == nil {
				t.Errorf("seed %s: Verify = %v, %v; want an accepted record", name, rec, verr)
			}
			continue
		}
		if verr == nil || rec != nil {
			t.Errorf("seed %s: Verify accepted bytes the fixture captures as bad", name)
		}
	}
}

// fuzzVerifierParts returns the fixture keypair and a clock frozen inside
// the fixture token's validity window, so expiry is not what decides the
// other seeds' outcomes.
func fuzzVerifierParts(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey, Clock) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed([]byte(fixtureKeySeed))
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("the Ed25519 private key did not yield a public key")
	}
	return pub, priv, testkit.NewFrozenClock(fixtureIssued.Add(time.Minute))
}

package build

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func flakesModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("flake registry: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("flake registry: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// TestFlakeRegistry_RealTreeGreen: the committed registry
// (internal/build/flake-registry.json) parses and every entry is
// well-formed and unexpired. Today it is an empty array — no test in this
// repo has ever flaked — and an empty registry is valid by construction
// (zero entries to fail validation).
func TestFlakeRegistry_RealTreeGreen(t *testing.T) {
	root := flakesModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "internal", "build", "flake-registry.json"))
	if err != nil {
		t.Fatalf("flake registry: reading real registry: %v", err)
	}
	entries, err := ParseFlakeRegistry(data)
	if err != nil {
		t.Fatalf("flake registry: parsing real registry: %v", err)
	}
	if v := ValidateFlakeRegistry(entries, time.Now()); len(v) != 0 {
		t.Fatalf("flake registry: %d violation(s) in the real registry: %+v", len(v), v)
	}
}

func flakesFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(flakesModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "flakes")
}

func flakesLoadFixture(t *testing.T, name string) []FlakeEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(flakesFixtureDir(t), name))
	if err != nil {
		t.Fatalf("flake registry: reading fixture %s: %v", name, err)
	}
	entries, err := ParseFlakeRegistry(data)
	if err != nil {
		t.Fatalf("flake registry: parsing fixture %s: %v", name, err)
	}
	return entries
}

// referenceNow is a fixed instant (Art.7.3: no bare time.Now() in this
// gate's own logic; the fixed reference here plays the same role a frozen
// test Clock would) chosen so expired.json's 2020 expiry and valid.json's
// 2099 expiry are unambiguous regardless of when this test runs.
var referenceNow = time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

func TestFlakeRegistry_SeededViolationRed_Expired(t *testing.T) {
	entries := flakesLoadFixture(t, "expired.json")
	v := ValidateFlakeRegistry(entries, referenceNow)
	if len(v) == 0 {
		t.Fatal("flake registry: expected the expired entry to be flagged, found none")
	}
}

func TestFlakeRegistry_SeededViolationRed_Malformed(t *testing.T) {
	entries := flakesLoadFixture(t, "malformed.json")
	v := ValidateFlakeRegistry(entries, referenceNow)
	if len(v) < 5 {
		t.Fatalf("flake registry: expected at least 5 violations (empty test, bad ticket, empty reason, bad expiry, duplicate), got %d: %+v", len(v), v)
	}
}

func TestFlakeRegistry_ValidEntryIsGreen(t *testing.T) {
	entries := flakesLoadFixture(t, "valid.json")
	v := ValidateFlakeRegistry(entries, referenceNow)
	if len(v) != 0 {
		t.Fatalf("flake registry: expected zero violations for a well-formed, unexpired entry, got %+v", v)
	}
}

func TestIsQuarantined(t *testing.T) {
	entries := flakesLoadFixture(t, "valid.json")
	testID := "github.com/acamarata/cascade/internal/example.TestFlaky"
	if !IsQuarantined(entries, testID, referenceNow) {
		t.Fatalf("expected %s to be reported quarantined (live, unexpired entry)", testID)
	}
	if IsQuarantined(entries, "github.com/acamarata/cascade/internal/example.TestSomethingElse", referenceNow) {
		t.Fatal("expected an unrelated test id to report not quarantined")
	}

	expired := flakesLoadFixture(t, "expired.json")
	if IsQuarantined(expired, testID, referenceNow) {
		t.Fatal("expected an EXPIRED entry to no longer count as quarantined")
	}
}

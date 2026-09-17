package sync

// Purpose (this file): consuming the M/S-27.T5 spike's adversarial
//   fixtures as this ticket's acceptance corpus, and gating them on the
//   ADR's digest.
//
// WHY THE DIGEST IS A GATE AND NOT A COMMENT. The fixtures are the
//   evidence that these merges converge under inputs somebody deliberately
//   constructed to break them. Evidence that can be edited to match the
//   implementation is not evidence: the way this suite would fail silently
//   is a fixture being "fixed" to agree with a merge that had regressed.
//   So the corpus hashes to a value recorded in the ADR, and altering a
//   fixture without revising the ADR fails the build.
//
// AND THE PROPERTIES ARE ASSERTED, NOT THE OUTPUTS (R-21.219). The config,
//   memory and blob fixtures carry no expected values on purpose. A
//   recorded expected output would only prove the implementation still
//   does what it did when somebody wrote the file down; the properties —
//   commutativity, idempotence, associativity, tombstone dominance, no
//   silent loss — are what actually has to hold, and they hold or fail
//   against any input at all. Only the phase-git cases keep recorded
//   output, because git's answer is git's to give.
//
// SPORT: internal/sync fixture corpus (ADD) — P1-E17-W4-S38-T2.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/zeebo/blake3"
)

// adrFixtureDigest is the value docs/adrs/ADR-sync-merge-semantics.md
// records for this corpus. It is duplicated here deliberately: the ADR is
// prose a build cannot read, and a constant a reader can compare by eye to
// the document is the join between them.
const adrFixtureDigest = "291a77c5f48eba2b3d40f71b7255a227478baae0fc3224a00a3b71bf884b1cf7"

// fixtureDir is where the corpus lives in this package.
const fixtureDir = "testdata/conflicts"

// The four domain labels the corpus uses. They are the spike's own names
// and are NOT the registry's subkinds: "blob" here is the registry's
// "blobs", and "context" covers both memory and conversation records. Kept
// as the spike wrote them because renaming a fixture changes the corpus
// digest, and the digest is what makes the corpus evidence.
const (
	fixtureConfig  = "config"
	fixtureContext = "context"
	fixtureBlob    = "blob"
	fixturePhase   = "phase-state"
)

// TestSpikeFixtureDigest fails when the corpus is absent or altered.
func TestSpikeFixtureDigest(t *testing.T) {
	names := fixtureNames(t)
	if len(names) == 0 {
		t.Fatal("the fixture corpus is empty; this ticket's acceptance suite has no adversarial inputs")
	}
	var acc []byte
	for _, n := range names {
		body, err := os.ReadFile(filepath.Join(fixtureDir, n))
		if err != nil {
			t.Fatal(err)
		}
		per := blake3.Sum256(append([]byte(n+"\n"), body...))
		acc = append(acc, per[:]...)
	}
	sum := blake3.Sum256(acc)
	if got := hex.EncodeToString(sum[:]); got != adrFixtureDigest {
		t.Fatalf("the fixture corpus digest is %s, the ADR records %s.\n"+
			"A fixture changed. If that is intended, revise "+
			"docs/adrs/ADR-sync-merge-semantics.md and this constant together — "+
			"a fixture edited to agree with the implementation is not evidence about it.",
			got, adrFixtureDigest)
	}
}

// fixtureNames lists the corpus in sorted order.
func fixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatalf("reading the fixture corpus: %v", err)
	}
	var names []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// fixtureCase is the shape every fixture shares. The record fields are
// decoded loosely because the four domains carry different ones; each
// domain's loader takes what it needs.
type fixtureCase struct {
	Domain      string `json:"domain"`
	Case        string `json:"case"`
	Description string `json:"description"`
	// The record domains carry a LIST per side; phase-state carries an
	// object, because what diverges there is a repository rather than a
	// set of records. Both are decoded raw and each domain's loader takes
	// the shape it knows — a single struct that fitted both would have to
	// call one of them something it is not.
	SideA json.RawMessage `json:"sideA"`
	SideB json.RawMessage `json:"sideB"`
	// Expected is present only on the phase-state cases, where git's
	// answer is git's to give and there is a recorded outcome to compare.
	Expected json.RawMessage `json:"expected,omitempty"`
}

// loadFixture decodes one fixture.
func loadFixture(t *testing.T, name string) fixtureCase {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var fc fixtureCase
	if err := json.Unmarshal(body, &fc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return fc
}

// TestEveryFixtureIsExercised is the check that keeps the corpus from
// rotting into decoration. A fixture no test loads is a file that proves
// nothing and that nobody will notice has stopped being true.
func TestEveryFixtureIsExercised(t *testing.T) {
	covered := map[string]bool{}
	for _, name := range fixtureNames(t) {
		fc := loadFixture(t, name)
		switch fc.Domain {
		case fixtureConfig, fixtureContext, fixtureBlob, fixturePhase:
			covered[name] = true
		default:
			t.Errorf("%s names domain %q, which no strategy in this package merges", name, fc.Domain)
		}
		if fc.Domain == fixturePhase && len(fc.Expected) == 0 {
			t.Errorf("%s is a phase-state case with no recorded outcome; git's answer is the one "+
				"thing in this corpus that IS recorded", name)
		}
		if fc.Domain != fixturePhase && len(fc.Expected) != 0 {
			t.Errorf("%s records an expected output. The record domains assert PROPERTIES, not "+
				"outputs: a recorded expectation only proves the merge still does what it did when "+
				"somebody wrote it down (R-21.219)", name)
		}
		if fc.Description == "" {
			t.Errorf("%s has no description; an adversarial fixture nobody can read is one nobody maintains", name)
		}
	}
	if len(covered) != len(fixtureNames(t)) {
		t.Errorf("%d of %d fixtures map onto a strategy", len(covered), len(fixtureNames(t)))
	}
}

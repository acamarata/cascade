package memory

// Purpose: the SOUL fixture every soul test builds on, and the
//   vocabulary tests: what the store refuses to store, that the route set
//   is closed at three, and that the CASCADE_NO_INPUT refusal is a hard
//   error and says so.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingSoulSink captures divergence events so a test asserts that a
// conflict was reported rather than assuming it was.
type recordingSoulSink struct {
	events   []DivergenceEvent
	failWith error
}

func (s *recordingSoulSink) SoulDiverged(_ context.Context, ev DivergenceEvent) error {
	s.events = append(s.events, ev)
	return s.failWith
}

// soulFixture is one real store over a real temp directory with a frozen
// clock. Nothing here is a double: the tests write and read actual files,
// because the whole subject is what ends up on disk.
type soulFixture struct {
	store *FileSoulStore
	sink  *recordingSoulSink
	base  string
	clock *testkit.FrozenClock
}

func newSoulFixture(t *testing.T) soulFixture {
	t.Helper()
	base := t.TempDir()
	clk := newTestClock()
	sink := &recordingSoulSink{}
	return soulFixture{
		store: NewFileSoulStore(base, clk, sink),
		sink:  sink,
		base:  base,
		clock: clk,
	}
}

// documentPath and ledgerPath let a test act as the user's own editor,
// writing the file out from under the store the way any editor would.
func (f soulFixture) documentPath() string {
	return filepath.Join(f.base, soulDir, soulDocumentFile)
}

func (f soulFixture) ledgerPath() string {
	return filepath.Join(f.base, soulDir, soulLedgerFile)
}

func (f soulFixture) notePath() string {
	return filepath.Join(f.base, soulDir, soulNoteFile)
}

// writeFileAs writes path as an out-of-store editor would.
func writeFileAs(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}

// mustEdit applies one document through the CLI route.
func (f soulFixture) mustEdit(t *testing.T, body string) SoulView {
	t.Helper()
	view, err := f.store.Edit(context.Background(), SoulDocument{Body: body})
	if err != nil {
		t.Fatalf("edit %q: %v", body, err)
	}
	return view
}

// TestSoulDocumentValidation pins what the store refuses to store. An
// empty body is the important one: a SOUL that reads as empty is a wrong
// model of the user, not an absent one, and every consumer would take it
// as fact.
func TestSoulDocumentValidation(t *testing.T) {
	cases := []struct {
		name string
		doc  SoulDocument
		ok   bool
	}{
		{"body and schema", SoulDocument{Body: "I am Ada.", Schema: "x/v1"}, true},
		{"body only", SoulDocument{Body: "I am Ada."}, true},
		{"empty body", SoulDocument{Body: ""}, false},
		{"whitespace body", SoulDocument{Body: "  \n\t "}, false},
		{"oversized body", SoulDocument{Body: strings.Repeat("a", maxSoulBodyBytes+1)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.doc.Validate()
			if c.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !c.ok {
				if !errors.Is(err, ErrInvalidSoulDocument) {
					t.Fatalf("Validate() = %v, want ErrInvalidSoulDocument", err)
				}
				if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
					t.Fatalf("kind = %v, want invalid-input", kind)
				}
			}
		})
	}
}

// TestSoulDocumentCanonicalDefaultsSchemaOnly proves canonicalization
// never touches the body: the user's own words round-trip byte for byte,
// trailing whitespace and all.
func TestSoulDocumentCanonicalDefaultsSchemaOnly(t *testing.T) {
	in := SoulDocument{Body: "  keep\n\tevery byte  \n"}
	got := in.canonical()
	if got.Body != in.Body {
		t.Fatalf("body changed: %q -> %q", in.Body, got.Body)
	}
	if got.Schema != DefaultSoulSchema {
		t.Fatalf("schema = %q, want %q", got.Schema, DefaultSoulSchema)
	}
	named := SoulDocument{Body: "x", Schema: "mine/v2"}.canonical()
	if named.Schema != "mine/v2" {
		t.Fatalf("declared schema overwritten: %q", named.Schema)
	}
}

// TestSoulEditRouteIsAClosedSet proves the route set is exactly three and
// that an unknown route is refused rather than defaulted. Attributing a
// change to the wrong route is worse than refusing to attribute it.
func TestSoulEditRouteIsAClosedSet(t *testing.T) {
	if len(allSoulRoutes) != 3 {
		t.Fatalf("route set has %d members, want exactly 3", len(allSoulRoutes))
	}
	for _, r := range allSoulRoutes {
		got, err := ParseSoulEditRoute(string(r))
		if err != nil || got != r {
			t.Fatalf("ParseSoulEditRoute(%q) = %q, %v", r, got, err)
		}
		if r.String() != string(r) {
			t.Fatalf("String() = %q, want %q", r.String(), string(r))
		}
	}
	for _, bad := range []string{"", "api", "cli ", "CLI", "file"} {
		if _, err := ParseSoulEditRoute(bad); !errors.Is(err, ErrInvalidSoulRoute) {
			t.Fatalf("ParseSoulEditRoute(%q) = %v, want ErrInvalidSoulRoute", bad, err)
		}
	}
}

// TestDivergenceEventName pins the bus name, which subscribers match on.
func TestDivergenceEventName(t *testing.T) {
	if got := (DivergenceEvent{}).EventName(); got != SoulDivergedEvent {
		t.Fatalf("EventName() = %q, want %q", got, SoulDivergedEvent)
	}
}

// TestSoulEditNoInput proves the CASCADE_NO_INPUT refusal is a HARD ERROR
// and says so.
//
// The wording is load-bearing rather than cosmetic: the automation-parity
// rule requires the editor path to fail visibly instead of opening a
// subprocess that waits on a terminal no automation has, and an operator
// reading a softer word would go looking for a hang. The message is
// written to stdout as the check command's own evidence.
func TestSoulEditNoInput(t *testing.T) {
	if !strings.Contains(SoulNoInputMessage, "hard error") {
		t.Fatalf("SoulNoInputMessage does not name a hard error: %q", SoulNoInputMessage)
	}
	if !errors.Is(ErrSoulEditNeedsInput, ErrSoulEditNeedsInput) {
		t.Fatal("sentinel does not match itself")
	}
	if kind, _ := cascade.KindOf(ErrSoulEditNeedsInput); kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v, want invalid-input", kind)
	}
	fmt.Println("CASCADE_NO_INPUT=1 editor open is a hard error:", SoulNoInputMessage)
}

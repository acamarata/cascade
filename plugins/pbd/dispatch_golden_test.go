package pbd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// Purpose (this file): the P9-dogfood golden parity check — the exact
//   conductor.execute params body the seam assembles for five REAL
//   planning tickets, one per model_class, replayed and compared byte for
//   byte. conductor.execute, not model.execute: pkg/provider's wrapper
//   dials the daemon's one registered door, and the golden locks the door
//   name alongside the body precisely so a rename cannot pass unnoticed.
//
// What the golden locks, and what it does not. These fixtures are a
//   STABILITY lock over real inputs, not a parity check against a foreign
//   implementation: the request shape is this repo's own, so there is no
//   external counterpart to capture it from. What makes the check
//   meaningful is that the INPUTS are real — harvested from the archived
//   P9 corpus, never authored here — so a change to request assembly,
//   the §5.18 mapping or the sensitivity stamp shows up against
//   non-trivial content rather than against a synthetic ticket shaped to
//   agree with the code.
//
// Regeneration is DELIBERATELY not automatic. There is no -update flag: a
//   golden that rewrites itself to match current output asserts nothing.
//   Changing one means editing it by hand and saying why in the ticket
//   journal.
//
// Constraints: no network (Art.7.2) — the seam runs against a recording
//   caller, and the recorded bytes are the assertion.
// SPORT: plugins/pbd tests (ADD) — P1-E14-W3-S30-T4.

// goldenFixture is one recorded case: the real ticket, and the request the
// seam must assemble from it.
type goldenFixture struct {
	Ticket struct {
		ID         string   `json:"id"`
		Title      string   `json:"title"`
		ModelClass string   `json:"model_class"`
		Tasks      []string `json:"tasks"`
		SpecRefs   []string `json:"spec_refs"`
	} `json:"ticket"`
	SourceFile string          `json:"source_file"`
	Request    json.RawMessage `json:"request,omitempty"`
}

// capturingCaller records the door and params it was handed without
// sending them anywhere.
type capturingCaller struct {
	method string
	params any
}

func (c *capturingCaller) Do(_ context.Context, method string, params, out any) error {
	c.method, c.params = method, params
	if resp, ok := out.(*provider.ModelResponse); ok {
		resp.JobID = "job-golden"
	}
	return nil
}

// goldenDir is where the P9-dogfood fixtures live.
func goldenDir() string { return filepath.Join("testdata", "p9-dogfood") }

// loadGolden reads one fixture.
func loadGolden(t *testing.T, name string) goldenFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir(), name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var fx goldenFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return fx
}

// ticketOf rebuilds the pews.Ticket a fixture records.
func ticketOf(fx goldenFixture) *pews.Ticket {
	return &pews.Ticket{
		ID:         fx.Ticket.ID,
		Title:      fx.Ticket.Title,
		ModelClass: pews.ModelClass(fx.Ticket.ModelClass),
		Tasks:      fx.Ticket.Tasks,
		SpecRefs:   fx.Ticket.SpecRefs,
	}
}

// assembledRequest runs the seam and returns the request it produced,
// checking on the way that it went to the one door this build has.
func assembledRequest(t *testing.T, fx goldenFixture) []byte {
	t.Helper()
	caller := &capturingCaller{}
	if _, err := NewConductorDispatcher(caller).Dispatch(context.Background(), ticketOf(fx)); err != nil {
		t.Fatalf("dispatch %s: %v", fx.Ticket.ID, err)
	}
	if caller.method != "conductor.execute" {
		t.Fatalf("%s went to door %q, want conductor.execute", fx.Ticket.ID, caller.method)
	}
	encoded, err := json.MarshalIndent(caller.params, "", "  ")
	if err != nil {
		t.Fatalf("encode request for %s: %v", fx.Ticket.ID, err)
	}
	return encoded
}

// goldenNames is the five fixtures, one per model_class.
var goldenNames = []string{"mech.json", "build.json", "heavy.json", "review.json", "arbiter.json"}

// TestP9DogfoodGoldenParity replays every fixture and asserts the request
// body is byte-identical to the recorded one.
func TestP9DogfoodGoldenParity(t *testing.T) {
	for _, name := range goldenNames {
		fx := loadGolden(t, name)
		if len(fx.Request) == 0 {
			t.Fatalf("%s carries no recorded request; the fixture was never captured", name)
		}
		got := assembledRequest(t, fx)

		var want, have any
		if err := json.Unmarshal(fx.Request, &want); err != nil {
			t.Fatalf("%s: recorded request is not JSON: %v", name, err)
		}
		if err := json.Unmarshal(got, &have); err != nil {
			t.Fatalf("%s: assembled request is not JSON: %v", name, err)
		}
		wantB, _ := json.Marshal(want)
		haveB, _ := json.Marshal(have)
		if string(wantB) != string(haveB) {
			t.Errorf("%s: request assembly changed%s", name, firstDifference(string(wantB), string(haveB)))
		}
	}
}

// TestEveryModelClassHasARealFixture is the coverage half: all five §5.18
// classes must be exercised on real corpus content, not just the ones that
// happened to be convenient.
func TestEveryModelClassHasARealFixture(t *testing.T) {
	seen := map[string]string{}
	for _, name := range goldenNames {
		fx := loadGolden(t, name)
		if fx.SourceFile == "" {
			t.Errorf("%s names no source ticket; its provenance is unverifiable", name)
		}
		if prev, dup := seen[fx.Ticket.ModelClass]; dup {
			t.Errorf("model_class %q appears in both %s and %s", fx.Ticket.ModelClass, prev, name)
		}
		seen[fx.Ticket.ModelClass] = name
		if len(fx.Ticket.Tasks) == 0 {
			t.Errorf("%s has no tasks; it would not exercise prompt assembly", name)
		}
	}
	got := make([]string, 0, len(seen))
	for k := range seen {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"arbiter", "build", "heavy", "mech", "review"}
	if len(got) != len(want) {
		t.Fatalf("fixtures cover %v, want all five §5.18 classes %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fixtures cover %v, want %v", got, want)
		}
	}
}

// firstDifference renders the point two request bodies diverge at.
//
// These bodies are multi-kilobyte planning prose; printing both in full for
// five fixtures buries the one character that moved. This prints the offset
// and a window around it instead.
func firstDifference(want, have string) string {
	i := 0
	for i < len(want) && i < len(have) && want[i] == have[i] {
		i++
	}
	window := func(s string) string {
		lo, hi := i-60, i+60
		if lo < 0 {
			lo = 0
		}
		if hi > len(s) {
			hi = len(s)
		}
		return s[lo:hi]
	}
	return fmt.Sprintf(" at byte %d\n recorded:  ...%s...\n assembled: ...%s...", i, window(want), window(have))
}

package context

import (
	"strings"
	"testing"
)

// The cross-writer conformance suite. Every harness serializer that ships
// is driven through the SAME assertions from the SAME registry the pipeline
// writes from (harnessWriters), so a fourth writer joins this suite by
// being added to that registry and cannot quietly opt out of it.
//
// The law under test is not "each writer works". It is that the three
// writers behave identically where behaviour is user-visible: the same
// tiers in the same order, the same content held back with the same visible
// notice, the same refusal to overwrite a hand edit, and the same bytes on
// a repeat run. A user who switches harnesses must not silently switch
// instructions.

// conformanceWriters names each registered writer for test output. The
// names are derived from the registry rather than listed beside it, so the
// table cannot fall out of step with what ships.
func conformanceWriters(t *testing.T) []struct {
	name string
	gen  HarnessGenerator
} {
	t.Helper()
	writers := harnessWriters()
	if len(writers) < 3 {
		t.Fatalf("the registry holds %d writers; this suite exists to compare at least three", len(writers))
	}
	out := make([]struct {
		name string
		gen  HarnessGenerator
	}, 0, len(writers))
	for _, w := range writers {
		out = append(out, struct {
			name string
			gen  HarnessGenerator
		}{name: writerName(w), gen: w})
	}
	return out
}

// writerName is the writer's Go type name, used only for test output.
func writerName(w HarnessGenerator) string {
	switch w.(type) {
	case *CCInstructionWriter:
		return "CC"
	case *CXInstructionWriter:
		return "CX"
	case *OCInstructionWriter:
		return "OC"
	default:
		return "unregistered"
	}
}

// TestHarnessWritersHaveDistinctNames catches a registry entry added by
// copy-paste: writerName falling through to "unregistered" would make every
// other assertion in this file report the wrong writer.
func TestHarnessWritersHaveDistinctNames(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range harnessWriters() {
		name := writerName(w)
		if name == "unregistered" {
			t.Fatalf("a registered writer has no name in writerName; add it there and to this suite")
		}
		if seen[name] {
			t.Fatalf("two registered writers both report the name %q", name)
		}
		seen[name] = true
	}
}

// TestHarnessWritersOrderTiersIdentically pins the ordering law across
// writers: every writer emits the same tiers, most general first.
func TestHarnessWritersOrderTiersIdentically(t *testing.T) {
	want := []TierRole{TierGCI, TierASI, TierPPI, TierPRI, TierPAI}
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			files, err := w.gen.Generate(mergeGoldenCorpus(t))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if len(files) != len(want) {
				t.Fatalf("got %d files, want %d (one per contributing tier)", len(files), len(want))
			}
			for i, f := range files {
				if f.Role != want[i] {
					t.Fatalf("file %d is tier %s, want %s", i, f.Role, want[i])
				}
			}
		})
	}
}

// TestHarnessWritersAreIdempotent renders each writer repeatedly and
// requires byte-identical output every time. A generator that is stable
// only on the first call produces a diff on every sync.
func TestHarnessWritersAreIdempotent(t *testing.T) {
	const renders = 25
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			first, err := w.gen.Generate(mergeGoldenCorpus(t))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			for i := 1; i < renders; i++ {
				again, err := w.gen.Generate(mergeGoldenCorpus(t))
				if err != nil {
					t.Fatalf("Generate (render %d): %v", i, err)
				}
				if len(again) != len(first) {
					t.Fatalf("render %d produced %d files, first produced %d", i, len(again), len(first))
				}
				for j := range again {
					if again[j].Name != first[j].Name || again[j].Role != first[j].Role {
						t.Fatalf("render %d file %d addresses %s/%s, first addressed %s/%s",
							i, j, again[j].Role, again[j].Name, first[j].Role, first[j].Name)
					}
					if string(again[j].Content) != string(first[j].Content) {
						t.Fatalf("render %d file %d (tier %s) differs from the first render",
							i, j, again[j].Role)
					}
				}
			}
		})
	}
}

// TestHarnessWritersHoldBackUnrenderableContent requires every writer to
// drop content that must not be committed AND to say on the page that it
// did. Silence here is the failure: a section the user believes is in force
// but is not.
func TestHarnessWritersHoldBackUnrenderableContent(t *testing.T) {
	mc := MergedContext{
		Sections: []MergedSection{
			{Heading: "Safe", Content: "## Safe\n\nplain guidance", Role: TierGCI, Ordinal: 0},
			{Heading: "Leaky", Content: "## Leaky\n\n" + canaryToken, Role: TierGCI, Ordinal: 0},
		},
		Provenance: map[string]TierRole{"Safe": TierGCI, "Leaky": TierGCI},
	}
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			files, err := w.gen.Generate(mc)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if len(files) != 1 {
				t.Fatalf("got %d files, want 1", len(files))
			}
			body := string(files[0].Content)
			if strings.Contains(body, canaryToken) {
				t.Fatalf("the planted credential reached the generated file")
			}
			if !strings.Contains(body, `"Leaky"`) {
				t.Errorf("the held-back section is not named in the file, so its loss is silent")
			}
			if !strings.Contains(body, "could not be rendered") {
				t.Errorf("the file carries no exclusion notice")
			}
			if !strings.Contains(body, "plain guidance") {
				t.Errorf("the renderable section was dropped along with the held-back one")
			}
		})
	}
}

// TestHarnessWritersAgreeOnContent is the anti-drift assertion proper:
// given one MergedContext, every writer's block for a given tier is the
// same bytes. The writers differ in where a file goes, never in what it
// says, so a change that alters one writer's wording alone fails here.
func TestHarnessWritersAgreeOnContent(t *testing.T) {
	writers := conformanceWriters(t)
	base, err := writers[0].gen.Generate(mergeGoldenCorpus(t))
	if err != nil {
		t.Fatalf("%s Generate: %v", writers[0].name, err)
	}
	for _, w := range writers[1:] {
		files, err := w.gen.Generate(mergeGoldenCorpus(t))
		if err != nil {
			t.Fatalf("%s Generate: %v", w.name, err)
		}
		for i := range files {
			if files[i].Role != base[i].Role {
				t.Fatalf("%s file %d is tier %s, %s emitted %s",
					w.name, i, files[i].Role, writers[0].name, base[i].Role)
			}
			if string(files[i].Content) != string(base[i].Content) {
				t.Errorf("%s and %s disagree on tier %s's content",
					w.name, writers[0].name, files[i].Role)
			}
		}
	}
}

// TestHarnessWritersMatchTheProvenancedGoldens ties the shared content to
// the provenance-stamped fixtures assembled for the first writer, so the
// agreement asserted above is agreement on the RIGHT bytes rather than on
// a shared mistake.
func TestHarnessWritersMatchTheProvenancedGoldens(t *testing.T) {
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			files, err := w.gen.Generate(mergeGoldenCorpus(t))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			for _, f := range files {
				golden, ok := ccGoldenFiles[f.Role]
				if !ok {
					t.Fatalf("tier %s has no golden fixture", f.Role)
				}
				if want := loadCCGolden(t, golden); string(f.Content) != want {
					t.Errorf("tier %s: content does not match %s", f.Role, golden)
				}
			}
		})
	}
}

// TestHarnessWritersRefuseMalformedInput requires every writer to refuse
// the same malformed inputs with the same typed kind, and to accept an
// empty cascade as the legitimate state it is.
func TestHarnessWritersRefuseMalformedInput(t *testing.T) {
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			for _, tc := range append(refusedShapeCases(), refusedOrdinalCases()...) {
				files, err := w.gen.Generate(tc.mc)
				if err == nil {
					t.Fatalf("%s: Generate accepted a malformed context and returned %d files",
						tc.name, len(files))
				}
				assertKindInvalidInput(t, err)
			}
			files, err := w.gen.Generate(MergedContext{Provenance: map[string]TierRole{}})
			if err != nil {
				t.Fatalf("an empty cascade must not be an error: %v", err)
			}
			if len(files) != 0 {
				t.Fatalf("an empty cascade rendered %d files", len(files))
			}
		})
	}
}

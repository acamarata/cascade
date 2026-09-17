package context

// Purpose: the cross-harness conformance suite (P1-E16-W4-S35-T4) — one
//   test body run over harnessWriters(), asserting the conventions every
//   harness serializer must share, plus the committed golden corpus that
//   pins their real output.
// Constraints: the suite is driven from harnessWriters(), NOT from a list
//   written here: adding a writer there opts it into every assertion
//   below, which is the only way a conformance suite stays honest as the
//   set grows. Art.7.1 — every path is a t.TempDir(); no network, no real
//   home directory.
// SPORT: internal/context cross-harness conformance (ADD) — P1-E16-W4-S35-T4.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// crossHarnessFixture builds the two-tier project every test here renders:
// a global tier under a fake home and a repo tier under the project. Two
// tiers rather than one because the harnesses' file names only DIVERGE at
// the global tier — a one-tier fixture would miss the collision this suite
// exists to police.
func crossHarnessFixture(t *testing.T) (project string, mc MergedContext, roots map[TierRole]string) {
	t.Helper()
	project = t.TempDir()
	home := t.TempDir()
	writeTier(t, filepath.Join(home, ".cascade"), "# Global\n\nShort sentences.\n")
	writeTier(t, filepath.Join(project, ".cascade"), "# Repo Instructions\n\nRun the tests.\n")

	records, err := Discover(context.Background(), project, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if mc, err = MergeTiers(records); err != nil {
		t.Fatalf("MergeTiers: %v", err)
	}
	return project, mc, rootsByRole(records)
}

// writeTier seeds one tier source file.
func writeTier(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CASCADE.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("seeding %s: %v", dir, err)
	}
}

// TestCrossHarnessSchemaVersion asserts every writer brackets its output
// with the SAME managed-block markers.
//
// The markers are the schema: they are how a later run finds the block it
// owns, how a hand edit is detected, and how a file written by v1 is
// recognised rather than duplicated. A writer that spelled them even
// slightly differently would silently append a second block on every run,
// forever, and each run would look successful.
func TestCrossHarnessSchemaVersion(t *testing.T) {
	_, mc, _ := crossHarnessFixture(t)

	for _, w := range harnessWriters() {
		name := harnessName(w)
		files, err := w.Generate(mc)
		if err != nil {
			t.Fatalf("%s: Generate: %v", name, err)
		}
		if len(files) == 0 {
			t.Fatalf("%s rendered no files from a two-tier merge", name)
		}
		for _, f := range files {
			body := string(f.Content)
			if !strings.Contains(body, markerOpenPrefix) {
				t.Errorf("%s/%s: no opening marker", name, f.Name)
			}
			if !strings.Contains(body, digestAttr) {
				t.Errorf("%s/%s: opening marker carries no digest, so a hand edit cannot be detected", name, f.Name)
			}
			if !strings.Contains(body, markerClose) {
				t.Errorf("%s/%s: no closing marker", name, f.Name)
			}
		}
	}
}

// TestCrossHarnessBodyIsHarnessIndependent pins the convention that makes
// one file able to serve two harnesses at all: for a given tier, every
// writer renders the SAME bytes, and only the file NAME differs.
//
// If that ever stops being true, the two harnesses that share AGENTS.md
// start overwriting each other's content on every sync, and the surface
// that would report it (drift) would report the file fresh immediately
// after each write. So this is the assertion the sharing rests on.
func TestCrossHarnessBodyIsHarnessIndependent(t *testing.T) {
	_, mc, _ := crossHarnessFixture(t)

	byRole := map[TierRole]map[string]string{}
	for _, w := range harnessWriters() {
		files, err := w.Generate(mc)
		if err != nil {
			t.Fatalf("%s: Generate: %v", harnessName(w), err)
		}
		for _, f := range files {
			if byRole[f.Role] == nil {
				byRole[f.Role] = map[string]string{}
			}
			byRole[f.Role][harnessName(w)] = string(f.Content)
		}
	}
	for role, bodies := range byRole {
		var first, firstName string
		for name, body := range bodies {
			if firstName == "" {
				first, firstName = body, name
				continue
			}
			if body != first {
				t.Errorf("tier %d: %s and %s render different bytes; a shared file cannot serve both",
					role, firstName, name)
			}
		}
	}
}

// TestCrossHarnessPathIsolation is the assertion that found a real defect.
//
// Two of the three harnesses read `AGENTS.md` at the same project path, so
// one file on disk serves both. The drift check reports that file ONCE —
// reporting it twice under two names would tell a caller two files are
// stale when one is. But the second harness must not disappear: a consumer
// that keys drift by harness then finds no entry for it and reports it in
// sync. `cascade context harness list` did exactly that for opencode.
//
// So: every path is reported at most once, AND every harness that renders
// a path appears somewhere on that path's entry.
func TestCrossHarnessPathIsolation(t *testing.T) {
	_, mc, roots := crossHarnessFixture(t)

	rendersPath := writersByPath(t, mc, roots)

	drift, err := DriftCheck(context.Background(), mc, roots)
	if err != nil {
		t.Fatalf("DriftCheck: %v", err)
	}

	reported := map[string]map[string]bool{}
	for _, d := range drift {
		if _, dup := reported[d.Path]; dup {
			t.Errorf("%s is reported twice; one file on disk is one row", d.Path)
		}
		reported[d.Path] = map[string]bool{d.Harness: true}
		for _, also := range d.AlsoServes {
			reported[d.Path][also] = true
		}
	}

	shared := 0
	for path, writers := range rendersPath {
		if len(writers) > 1 {
			shared++
		}
		got := reported[path]
		if got == nil {
			t.Errorf("%s is rendered by %v but appears in no drift entry", path, sortedKeys(writers))
			continue
		}
		for name := range writers {
			if !got[name] {
				t.Errorf("%s is rendered by %s, but its drift entry names only %v — "+
					"a consumer keying by harness reads %s as in sync",
					path, name, sortedKeys(got), name)
			}
		}
	}
	// Without a genuinely shared path this test asserts nothing, so the
	// fixture's own shape is part of the contract.
	if shared == 0 {
		t.Fatal("no path is rendered by more than one harness; this fixture cannot detect the defect")
	}
}

// writersByPath maps each rendered on-disk path to the set of harnesses
// that render it.
func writersByPath(t *testing.T, mc MergedContext, roots map[TierRole]string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	for _, w := range harnessWriters() {
		files, err := w.Generate(mc)
		if err != nil {
			t.Fatalf("%s: Generate: %v", harnessName(w), err)
		}
		for _, f := range files {
			root := roots[f.Role]
			if root == "" {
				continue
			}
			path := filepath.Join(root, filepath.FromSlash(f.Name))
			if out[path] == nil {
				out[path] = map[string]bool{}
			}
			out[path][harnessName(w)] = true
		}
	}
	return out
}

// sortedKeys renders a set for a failure message.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestCrossHarnessMalformedInputIsStructured requires a MergedContext that
// could not have come from MergeTiers to produce a typed refusal from
// every writer — not a panic, and not a partial render.
func TestCrossHarnessMalformedInputIsStructured(t *testing.T) {
	malformed := MergedContext{
		Sections: []MergedSection{{Heading: "X", Content: "## X\n\nY", Role: TierGCI, Ordinal: 0}},
		// Provenance intentionally nil: MergeTiers always sets it.
	}
	for _, w := range harnessWriters() {
		name := harnessName(w)
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s panicked on a malformed MergedContext: %v", name, r)
				}
			}()
			files, err := w.Generate(malformed)
			if err == nil {
				t.Fatalf("%s accepted a MergedContext that did not come from MergeTiers", name)
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("%s: err = %v, want KindInvalidInput", name, err)
			}
			if len(files) != 0 {
				t.Errorf("%s returned %d file(s) alongside a refusal", name, len(files))
			}
		})
	}
}

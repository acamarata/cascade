// Purpose: unit tests for provenance-ref loading and Resolver.Resolve's
// done/planned/unresolved classification against real replacement files.
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeReplacement(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve_NoReplacementIsUnresolved(t *testing.T) {
	r := &Resolver{Root: t.TempDir()}
	disp, err := r.Resolve(Term{Term: "x", Class: "command", State: ""})
	if err != nil || disp != DispositionUnresolved {
		t.Fatalf("disp=%v err=%v, want UNRESOLVED", disp, err)
	}
}

func TestResolve_DoneRequiresExistingReplacement(t *testing.T) {
	r := &Resolver{Root: t.TempDir()}
	term := Term{Term: "x", Class: "command", Replacement: []string{"internal/x/x.go"}, Anchor: "func X", State: "done"}
	disp, err := r.Resolve(term)
	if err == nil {
		t.Fatalf("disp=%v: expected an error for a missing replacement file, got nil", disp)
	}
	// The failure must be the missing file itself, not a later anchor
	// check that happens to fail on an empty read.
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "reading replacement internal/x/x.go") {
		t.Fatalf("err=%v, want a not-exist error reading replacement internal/x/x.go", err)
	}
}

func TestResolve_DoneRequiresAnchor(t *testing.T) {
	root := t.TempDir()
	writeReplacement(t, root, "internal/x/x.go", "package x\nfunc Y() {}\n")
	r := &Resolver{Root: root}
	term := Term{Term: "x", Class: "command", Replacement: []string{"internal/x/x.go"}, Anchor: "func X", State: "done"}
	if _, err := r.Resolve(term); err == nil {
		t.Fatal("expected an error for a missing anchor, got nil")
	}
}

func TestResolve_DoneWithAnchorPresent(t *testing.T) {
	root := t.TempDir()
	writeReplacement(t, root, "internal/x/x.go", "package x\nfunc X() {}\n")
	r := &Resolver{Root: root}
	term := Term{Term: "x", Class: "command", Replacement: []string{"internal/x/x.go"}, Anchor: "func X", State: "done"}
	disp, err := r.Resolve(term)
	if err != nil || disp != DispositionDone {
		t.Fatalf("disp=%v err=%v, want DONE", disp, err)
	}
}

func TestResolve_PlannedIsOpen(t *testing.T) {
	root := t.TempDir()
	r := &Resolver{Root: root}
	term := Term{Term: "x", Class: "command", Replacement: []string{"internal/x/x.go"}, Anchor: "func X", State: "planned"}
	disp, err := r.Resolve(term)
	if err != nil || disp != DispositionOpen {
		t.Fatalf("disp=%v err=%v, want OPEN", disp, err)
	}
}

func TestResolve_PlannedAllPresentIsStale(t *testing.T) {
	root := t.TempDir()
	writeReplacement(t, root, "internal/x/x.go", "package x\nfunc X() {}\n")
	r := &Resolver{Root: root}
	term := Term{Term: "x", Class: "command", Replacement: []string{"internal/x/x.go"}, Anchor: "func X", State: "planned"}
	if _, err := r.Resolve(term); err == nil {
		t.Fatal("expected a stale-planned error, got nil")
	}
}

// --- LoadProvenanceRefs ---

func TestLoadProvenanceRefs_ValidatesShape(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"missing":      "",
		"malformed":    "{not json",
		"missing path": `{"refs":[{"path":"","term":"x","reason":"y"}]}`,
		"missing term": `{"refs":[{"path":"a.go","term":"","reason":"y"}]}`,
		"empty reason": `{"refs":[{"path":"a.go","term":"x","reason":""}]}`,
		"blank reason": `{"refs":[{"path":"a.go","term":"x","reason":"   "}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name+".json")
			if content != "" {
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadProvenanceRefs(p); err == nil {
				t.Fatalf("case %q: expected an error, got nil", name)
			}
		})
	}
}

func TestLoadProvenanceRefs_EmptyRefsIsFine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "refs.json")
	if err := os.WriteFile(p, []byte(`{"refs":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := LoadProvenanceRefs(p)
	if err != nil || len(refs) != 0 {
		t.Fatalf("refs=%v err=%v", refs, err)
	}
}

// --- integration: Run() end to end over the resolver ---

func TestSweepProvenanceRefsNotUnresolved(t *testing.T) {
	f := newFixture(t)
	f.source("internal/x.go", "// zz-fixture-provenance-term provenance note\n")
	f.source("internal/y.go", "// zz-fixture-provenance-term unrelated mention\n")
	f.writeTerms([]Term{{Term: "zz-fixture-provenance-term", Class: "marker"}})
	f.writeProvenance([]ProvenanceRef{{Path: "internal/x.go", Term: "zz-fixture-provenance-term", Reason: "design provenance"}})
	list := f.filesList("internal/x.go", "internal/y.go")
	if rc := f.run(list, "--dry-run", "--fail-on-unresolved"); rc != 3 {
		t.Fatalf("rc=%d, want 3 (internal/y.go's identical term stays UNRESOLVED)", rc)
	}
}

func TestSweepStaleProvenanceRefExits2(t *testing.T) {
	f := newFixture(t)
	f.source("internal/x.go", "no mention here\n")
	f.writeTerms([]Term{{Term: "zz-fixture-provenance-term", Class: "marker"}})
	f.writeProvenance([]ProvenanceRef{{Path: "internal/x.go", Term: "zz-fixture-provenance-term", Reason: "design provenance"}})
	list := f.filesList("internal/x.go")
	if rc := f.run(list, "--dry-run"); rc != 2 {
		t.Fatalf("rc=%d, want 2 (stale provenance ref, no matching hit)", rc)
	}
}

func TestResolveDoneMutationTarget(t *testing.T) {
	root := t.TempDir()
	writeReplacement(t, root, "internal/x/x.go", "package x\nfunc X() {}\n")
	r := &Resolver{Root: root}
	term := Term{Term: "x", Class: "command", Replacement: []string{"internal/x/x.go"}, Anchor: "func X", State: "done"}
	disp, err := r.Resolve(term)
	if err != nil {
		t.Fatal(err)
	}
	if disp != DispositionDone {
		t.Fatalf("disp=%v, want DONE", disp)
	}
}

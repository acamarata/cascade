package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/plugins/codex"
	"github.com/acamarata/cascade/plugins/opencode"
)

// Purpose: default-lane coverage for the cascade-codex/cascade-opencode
//   generator wiring (resolveHarnessFiles, harnessGenerator,
//   harnessGeneratorOC, and the init() that wires them): registry_test.go
//   already covers Load/Get/List/Grants/RPCMethodName/NewCobraCommand, but
//   nothing in the default (untagged) lane drove this file's real
//   internal/context bridge, leaving it at 0-12.5% while the spike-tagged
//   suites that would have covered it never run in the default lane.
// Constraints: package-private (resolveHarnessFiles/harnessGenerator/
//   harnessGeneratorOC are unexported, so these are in-package white-box
//   tests, confined to this _test.go file per Art.1); fakeHarnessGen below
//   drives resolveHarnessFiles' own error branches directly rather than
//   relying on a real writer to happen to produce them.
// SPORT: internal/plugins harness-generator-wiring tests (ADD).

// fakeHarnessGen is a minimal casctx.HarnessGenerator test double confined
// to this file, used only to drive resolveHarnessFiles' branches a real
// writer cannot reach on demand (a Generate error, and a role with no
// resolved directory).
type fakeHarnessGen struct {
	files []casctx.HarnessFile
	err   error
}

func (f fakeHarnessGen) Generate(casctx.MergedContext) ([]casctx.HarnessFile, error) {
	return f.files, f.err
}

// writeTierFile creates dir/.cascade/CASCADE.md with body, so Discover
// resolves a real, non-absent PRI tier at dir.
func writeTierFile(t *testing.T, dir, body string) {
	t.Helper()
	sub := filepath.Join(dir, ".cascade")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", sub, err)
	}
	if err := os.WriteFile(filepath.Join(sub, "CASCADE.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestResolveHarnessFiles_RealWriter drives the full production pipeline
// (Discover -> MergeTiers -> a real HarnessGenerator -> path resolution)
// against a real filesystem tier, over the un-exported resolveHarnessFiles
// registry.go's own two public adapters both call.
func TestResolveHarnessFiles_RealWriter(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nSome repo-tier content.\n")

	files, err := resolveHarnessFiles(context.Background(), dir, &casctx.CXInstructionWriter{})
	if err != nil {
		t.Fatalf("resolveHarnessFiles(...) error = %v, want nil", err)
	}
	if len(files) == 0 {
		t.Fatal("resolveHarnessFiles(...) returned 0 files, want at least one (PRI tier had real content)")
	}
	for _, f := range files {
		if f.path == "" {
			t.Error("generatedFile.path is empty, want a resolved filesystem path")
		}
		if len(f.content) == 0 {
			t.Error("generatedFile.content is empty, want the rendered tier block")
		}
		if !filepath.IsAbs(f.path) {
			t.Errorf("generatedFile.path = %q, want an absolute path under %q", f.path, dir)
		}
	}
}

// TestResolveHarnessFiles_GenerateError asserts a real error from the
// injected HarnessGenerator's Generate is propagated unchanged, not
// swallowed or wrapped into a different one.
func TestResolveHarnessFiles_GenerateError(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent.\n")

	wantErr := errors.New("fake generator: deliberate failure")
	_, err := resolveHarnessFiles(context.Background(), dir, fakeHarnessGen{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveHarnessFiles(...) error = %v, want %v", err, wantErr)
	}
}

// TestResolveHarnessFiles_MissingRoot asserts a generator that emits a file
// for a tier discovery gave no directory is REFUSED with a clear error,
// never silently dropped or written to a wrong/empty path. A non-git
// t.TempDir() with no strictly-nested child directory always resolves
// TierPAI to "" (cwd == the PRI anchor, so pai never gets a candidate —
// see internal/context/discover.go's tierDirs), which is exactly the
// resolved-directory-less role this asserts against.
func TestResolveHarnessFiles_MissingRoot(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent.\n")

	gen := fakeHarnessGen{files: []casctx.HarnessFile{
		{Name: "x.md", Content: []byte("y"), Role: casctx.TierPAI},
	}}
	_, err := resolveHarnessFiles(context.Background(), dir, gen)
	if err == nil {
		t.Fatal("resolveHarnessFiles(...) error = nil, want non-nil (TierPAI has no resolved directory here)")
	}
}

// TestResolveHarnessFiles_DiscoverError asserts a real casctx.Discover
// failure (an unreadable tier file, not a fake) propagates unchanged out of
// resolveHarnessFiles: only Generate errors and the no-root case were
// covered before this, leaving Discover's own error return (the first of
// resolveHarnessFiles' three fallible calls) unexercised in the default
// lane.
func TestResolveHarnessFiles_DiscoverError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not model this on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent.\n")
	file := filepath.Join(dir, ".cascade", "CASCADE.md")
	if err := os.Chmod(file, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })

	_, err := resolveHarnessFiles(context.Background(), dir, &casctx.CXInstructionWriter{})
	if err == nil {
		t.Fatal("resolveHarnessFiles(...) error = nil, want non-nil (unreadable tier file)")
	}
}

// TestResolveHarnessFiles_MergeTiersError asserts a real casctx.MergeTiers
// failure propagates unchanged: Discover has no content-validity opinion
// (it reads raw bytes), so a tier file with invalid UTF-8 passes Discover
// and is rejected only once MergeTiers parses it, exactly matching
// internal/context/merge.go's validateContent contract.
func TestResolveHarnessFiles_MergeTiersError(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, ".cascade")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	invalidUTF8 := []byte{0xff, 0xfe, 0xfd}
	if err := os.WriteFile(filepath.Join(sub, "CASCADE.md"), invalidUTF8, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := resolveHarnessFiles(context.Background(), dir, &casctx.CXInstructionWriter{})
	if err == nil {
		t.Fatal("resolveHarnessFiles(...) error = nil, want non-nil (invalid UTF-8 tier content)")
	}
}

// TestHarnessGenerator_Success drives harnessGenerator's own adapter
// closure end to end: a real filesystem tier in, a codex.GeneratedFile out.
func TestHarnessGenerator_Success(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent for codex.\n")

	gen := harnessGenerator(&casctx.CXInstructionWriter{})
	files, err := gen(context.Background(), dir)
	if err != nil {
		t.Fatalf("harnessGenerator(...)(...) error = %v, want nil", err)
	}
	if len(files) == 0 {
		t.Fatal("harnessGenerator(...)(...) returned 0 files, want at least one")
	}
	if files[0].Path == "" || len(files[0].Content) == 0 {
		t.Errorf("codex.GeneratedFile = %+v, want a non-empty Path and Content", files[0])
	}
}

// TestHarnessGenerator_Error asserts harnessGenerator propagates
// resolveHarnessFiles' error rather than masking it behind a nil slice.
func TestHarnessGenerator_Error(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent.\n")

	wantErr := errors.New("fake generator: codex path")
	gen := harnessGenerator(fakeHarnessGen{err: wantErr})
	files, err := gen(context.Background(), dir)
	if !errors.Is(err, wantErr) {
		t.Fatalf("harnessGenerator(...)(...) error = %v, want %v", err, wantErr)
	}
	if files != nil {
		t.Errorf("harnessGenerator(...)(...) files = %v, want nil on error", files)
	}
}

// TestHarnessGeneratorOC_Success mirrors TestHarnessGenerator_Success for
// the opencode adapter: the two are separate functions (not a shared
// generic helper) by deliberate design, per harnessGeneratorOC's doc
// comment, so each needs its own direct coverage.
func TestHarnessGeneratorOC_Success(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent for opencode.\n")

	gen := harnessGeneratorOC(&casctx.OCInstructionWriter{})
	files, err := gen(context.Background(), dir)
	if err != nil {
		t.Fatalf("harnessGeneratorOC(...)(...) error = %v, want nil", err)
	}
	if len(files) == 0 {
		t.Fatal("harnessGeneratorOC(...)(...) returned 0 files, want at least one")
	}
	if files[0].Path == "" || len(files[0].Content) == 0 {
		t.Errorf("opencode.GeneratedFile = %+v, want a non-empty Path and Content", files[0])
	}
}

// TestHarnessGeneratorOC_Error mirrors TestHarnessGenerator_Error.
func TestHarnessGeneratorOC_Error(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent.\n")

	wantErr := errors.New("fake generator: opencode path")
	gen := harnessGeneratorOC(fakeHarnessGen{err: wantErr})
	files, err := gen(context.Background(), dir)
	if !errors.Is(err, wantErr) {
		t.Fatalf("harnessGeneratorOC(...)(...) error = %v, want %v", err, wantErr)
	}
	if files != nil {
		t.Errorf("harnessGeneratorOC(...)(...) files = %v, want nil on error", files)
	}
}

// TestInit_WiresRealProductionEntryPoints calls the ACTUAL production
// callers this package's init() wires at process boot (codex.Generate and
// opencode.Generate — see registry.go's init doc comment), rather than
// only the unexported adapters, proving the wiring reaches a real caller
// and not just its own construction. init() runs once for this whole test
// binary (Go's init-before-any-Test guarantee), so by the time this test
// body runs, codex.Generate and opencode.Generate are already the real
// adapters: were the wiring absent, both would still be unwiredGenerator
// and return its "instruction generator not wired" error instead of
// succeeding here.
func TestInit_WiresRealProductionEntryPoints(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nContent for the wired entry points.\n")
	ctx := context.Background()

	cxFiles, err := codex.Generate(ctx, dir)
	if err != nil {
		t.Fatalf("codex.Generate(...) error = %v, want nil (init() must wire a real generator)", err)
	}
	if len(cxFiles) == 0 {
		t.Error("codex.Generate(...) returned 0 files, want at least one")
	}

	ocFiles, err := opencode.Generate(ctx, dir)
	if err != nil {
		t.Fatalf("opencode.Generate(...) error = %v, want nil (init() must wire a real generator)", err)
	}
	if len(ocFiles) == 0 {
		t.Error("opencode.Generate(...) returned 0 files, want at least one")
	}
}

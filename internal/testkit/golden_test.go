package testkit

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: tests for the golden-fixture helper, including this ticket's AC
//   artifact — a sample golden test proving the helper's round-trip — and
//   its error paths (missing golden, corrupt/mismatched golden).
// SPORT: build/testkit (ADD, placeholder per T-4 sport_updates).

// TestGolden_SampleRoundTrip is the T-4 AC artifact: testkit proving its
// own helper round-trip against a real, committed golden fixture
// (testdata/goldens/sample.golden) with deterministic input.
func TestGolden_SampleRoundTrip(t *testing.T) {
	got := MustRenderLine("answer", 42)
	want := Golden(t, "sample", got)
	if string(want) != string(got) {
		t.Fatalf("Golden round-trip mismatch: want %q, got %q", want, got)
	}
}

// TestGolden_MismatchReported proves the helper's error path for a corrupt
// golden: rendered output that disagrees with the committed fixture must
// fail the test via t.Errorf, not silently pass.
func TestGolden_MismatchReported(t *testing.T) {
	rt := &recordingT{TB: t}
	Golden(rt, "sample", MustRenderLine("answer", 41))
	if !rt.errored {
		t.Fatal("Golden: expected a mismatch failure for wrong input, got none")
	}
}

// TestGolden_MissingFileFails proves the helper's error path for a golden
// that does not exist: it must fail loudly (t.Fatalf) rather than treat a
// missing file as an empty-and-passing comparison.
func TestGolden_MissingFileFails(t *testing.T) {
	rt := &recordingT{TB: t}
	Golden(rt, "does-not-exist-"+t.Name(), []byte("anything"))
	if !rt.fataled {
		t.Fatal("Golden: expected a Fatalf on a missing golden file, got none")
	}
}

// TestGolden_UpdateRefusedInCI proves the CI guard: update mode plus a set
// CI env var must refuse to write, even though both env vars in isolation
// would otherwise be accepted.
func TestGolden_UpdateRefusedInCI(t *testing.T) {
	// Art.7.1: run from a t.TempDir(), not the real package directory — if
	// the CI guard this test exercises ever regressed, the write it would
	// otherwise perform must land in a throwaway directory, never the repo.
	t.Chdir(t.TempDir())
	t.Setenv(updateEnvVar, "1")
	t.Setenv(ciEnvVar, "true")

	path := goldenPath("update-refused-" + t.Name())

	rt := &recordingT{TB: t}
	Golden(rt, "update-refused-"+t.Name(), []byte("should never be written"))
	if !rt.fataled {
		t.Fatal("Golden: expected a Fatalf refusing to update a golden under CI, got none")
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
		t.Fatalf("Golden: wrote %s despite CI being set — the CI guard did not hold", path)
	}
}

// TestGolden_UpdateWritesOutsideCI proves the update mechanism itself
// works: with CI unset, UPDATE=1 writes got to a fresh golden path under
// t.TempDir(), which this test then cleans up (Art.7.1: this test's own
// writes stay under t.TempDir(); the golden helper's write path is the
// thing under test).
func TestGolden_UpdateWritesOutsideCI(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	t.Setenv(updateEnvVar, "1")
	t.Setenv(ciEnvVar, "")

	got := MustRenderLine("fresh", 7)
	wrote := Golden(t, "fresh", got)
	if string(wrote) != string(got) {
		t.Fatalf("Golden update: returned %q, want %q", wrote, got)
	}
	read, err := os.ReadFile(filepath.Join(dir, "testdata", "goldens", "fresh.golden"))
	if err != nil {
		t.Fatalf("Golden update: expected file to exist after update: %v", err)
	}
	if string(read) != string(got) {
		t.Fatalf("Golden update: wrote %q, want %q", read, got)
	}
}

// TestGolden_UpdateMkdirAllFails proves the update-mode MkdirAll error path:
// "testdata" existing as a plain FILE (not a directory) makes MkdirAll fail
// deterministically, without needing to mock the filesystem.
func TestGolden_UpdateMkdirAllFails(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("testdata", []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("test setup: %v", err)
	}
	t.Setenv(updateEnvVar, "1")
	t.Setenv(ciEnvVar, "")

	rt := &recordingT{TB: t}
	Golden(rt, "x", []byte("y"))
	if !rt.fataled {
		t.Fatal("Golden: expected a Fatalf when MkdirAll cannot create testdata/goldens, got none")
	}
}

// TestGolden_UpdateWriteFileFails proves the update-mode WriteFile error
// path: a directory sitting at the golden's own path makes os.WriteFile
// fail deterministically ("is a directory"), without mocking.
func TestGolden_UpdateWriteFileFails(t *testing.T) {
	t.Chdir(t.TempDir())
	name := "blocked"
	if err := os.MkdirAll(goldenPath(name), 0o755); err != nil {
		t.Fatalf("test setup: %v", err)
	}
	t.Setenv(updateEnvVar, "1")
	t.Setenv(ciEnvVar, "")

	rt := &recordingT{TB: t}
	Golden(rt, name, []byte("y"))
	if !rt.fataled {
		t.Fatal("Golden: expected a Fatalf when WriteFile targets a path that is a directory, got none")
	}
}

// TestGolden_ReadGenericErrorFails proves the compare-mode read-error path
// that is NOT os.ErrNotExist: a directory at the golden's path makes
// os.ReadFile fail with "is a directory", which Golden must still report
// via Fatalf even though errors.Is(err, os.ErrNotExist) is false for it.
func TestGolden_ReadGenericErrorFails(t *testing.T) {
	t.Chdir(t.TempDir())
	name := "blocked-read"
	if err := os.MkdirAll(goldenPath(name), 0o755); err != nil {
		t.Fatalf("test setup: %v", err)
	}

	rt := &recordingT{TB: t}
	Golden(rt, name, []byte("y"))
	if !rt.fataled {
		t.Fatal("Golden: expected a Fatalf on a non-NotExist read error, got none")
	}
}

// recordingT wraps a real testing.TB (satisfying the interface — including
// its unexported method — purely via embedding) so Golden's error paths
// can be exercised without actually failing the outer test. Fatalf/Errorf
// only record; they deliberately do NOT call the embedded TB's own
// Fatalf/Errorf (which would fail this package's own test run) or
// runtime.Goexit (which would abort the calling goroutine before this
// file's own assertions ran). golden.go's Fatalf call sites each have an
// explicit `return` immediately after them for exactly this reason: a
// fake, non-halting TB must not fall through into code that assumed
// Fatalf never returns.
type recordingT struct {
	testing.TB
	fataled bool
	errored bool
}

func (r *recordingT) Helper() {}

func (r *recordingT) Logf(string, ...any) {}

func (r *recordingT) Fatalf(string, ...any) { r.fataled = true }

func (r *recordingT) Errorf(string, ...any) { r.errored = true }

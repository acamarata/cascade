package memory

// Purpose: the test support the consolidation and staleness suites share —
//   a whole-tree snapshot, so a test can assert that a run changed NOTHING
//   rather than merely reporting that it changed nothing, and a
//   file-system double that fails at a chosen write, so an interruption
//   can be placed at an exact point. Split from consolidation_errors_test.go
//   under the 300-line file cap.
// Constraints: unexported and _test.go only, so no shipped path can reach
//   the failing file system (Art.1).

import (
	"os"
	"path/filepath"
	"testing"
)

// treeSnapshot records every file under root and its bytes, so a test can
// assert that a run changed nothing at all rather than merely reporting
// that it changed nothing.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // a path Walk produced under t.TempDir
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotting %s: %v", root, err)
	}
	return out
}

// assertTreeUnchanged fails with the specific difference when two
// snapshots disagree.
func assertTreeUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, want := range before {
		got, present := after[path]
		if !present {
			t.Errorf("%s disappeared", path)
			continue
		}
		if got != want {
			t.Errorf("%s was rewritten", path)
		}
	}
	for path := range after {
		if _, present := before[path]; !present {
			t.Errorf("%s appeared", path)
		}
	}
}

// countingFS wraps a real-file-backed map and fails the Nth write, so a
// test can interrupt a consolidation at an exact point.
type countingFS struct {
	inner    *failingFS
	failAt   int
	writes   int
	tombs    map[string]bool
	failWith error
}

func newCountingFS(failAt int) *countingFS {
	return &countingFS{inner: newFailingFS(), failAt: failAt, tombs: map[string]bool{}}
}

func (c *countingFS) ReadFile(path string) ([]byte, error) { return c.inner.ReadFile(path) }
func (c *countingFS) Remove(path string) error             { return c.inner.Remove(path) }
func (c *countingFS) Exists(path string) (bool, error)     { return c.inner.Exists(path) }
func (c *countingFS) ReadDirNames(dir string) ([]string, error) {
	return c.inner.ReadDirNames(dir)
}

func (c *countingFS) WriteAtomic(path string, data []byte) error {
	c.writes++
	if c.failAt > 0 && c.writes >= c.failAt {
		if c.failWith != nil {
			return c.failWith
		}
		return errInjected
	}
	return c.inner.WriteAtomic(path, data)
}

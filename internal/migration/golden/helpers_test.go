package main

// Purpose: shared fixtures for the harvester tests: a scratch module root
// seeded with a copy of the committed input set, a sealed HOME, a fixed
// clock, and helpers that run the harvester and read back what it wrote.
// Constraints: every test writes only under t.TempDir(); nothing here
// prints an input value.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

// realPinnedDir is the committed input set, relative to this package.
const realPinnedDir = "../v1/testdata/v1-goldens"

// testInstant is the fixed clock every test run uses.
var testInstant = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// sealEnv points HOME, USERPROFILE and the XDG dirs into the test's temp
// dir, disables the session bus, and routes harvester scratch there too.
func sealEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "disabled:")
	scratch := t.TempDir()
	prevScratch, prevClock := scratchParent, clockFor
	scratchParent = scratch
	clockFor = func() runtime.Clock { return runtime.NewFixedClock(testInstant) }
	t.Cleanup(func() { scratchParent, clockFor = prevScratch, prevClock })
}

// newModule returns a scratch module root holding a copy of the committed
// input set (with its INPUTS.sha256) and points moduleRootFor at it.
func newModule(t *testing.T) string {
	t.Helper()
	sealEnv(t)
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), []byte("module example.test/golden\n"))
	pinned := pinnedOf(root)
	for _, domain := range inputDomains {
		copyTree(t, filepath.Join(realPinnedDir, string(domain)), filepath.Join(pinned, string(domain)))
	}
	data, err := os.ReadFile(filepath.Join(realPinnedDir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(pinned, manifestName), data)
	prev := moduleRootFor
	moduleRootFor = func() (string, error) { return root, nil }
	t.Cleanup(func() { moduleRootFor = prev })
	return root
}

// pinnedOf is the pinned input dir inside a scratch module.
func pinnedOf(root string) string { return filepath.Join(root, filepath.FromSlash(pinnedInputDir)) }

// copyTree copies every regular file under src to dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		writeTestFile(t, filepath.Join(dst, rel), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// writeTestFile writes data, creating parent dirs.
func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// rewriteManifest regenerates INPUTS.sha256 over every non-README file in
// the scratch module's four input dirs.
func rewriteManifest(t *testing.T, root string) {
	t.Helper()
	pinned := pinnedOf(root)
	var lines []string
	for _, domain := range inputDomains {
		_ = filepath.WalkDir(filepath.Join(pinned, string(domain)), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() == "README.md" {
				return err
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				t.Fatal(rerr)
			}
			rel, _ := filepath.Rel(pinned, p)
			sum := sha256.Sum256(data)
			lines = append(lines, hex.EncodeToString(sum[:])+"  "+filepath.ToSlash(rel))
			return nil
		})
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i][66:] < lines[j][66:] })
	writeTestFile(t, filepath.Join(pinned, manifestName), []byte(strings.Join(lines, "\n")+"\n"))
}

// runHarvest runs the CLI core and captures both streams.
func runHarvest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runArgs(args, output.New(&stdout, &stderr, false, false, false, true))
	return code, stdout.String(), stderr.String()
}

// outputFile is one file found under the four output dirs.
type outputFile struct {
	data  []byte
	mtime time.Time
}

// readOutputs returns every file under root's four output dirs, keyed by
// module-relative slash path.
func readOutputs(t *testing.T, root string) map[string]outputFile {
	t.Helper()
	out := map[string]outputFile{}
	for _, domain := range inputDomains {
		dir := filepath.Join(root, filepath.FromSlash(outputDirs[domain]))
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			p := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			out[outputDirs[domain]+"/"+entry.Name()] = outputFile{data: data, mtime: info.ModTime()}
		}
	}
	return out
}

// inputFor returns the verified pinned input rel from the real input set.
func inputFor(t *testing.T, rel string) inputFile {
	t.Helper()
	files, err := loadPinned(realPinnedDir)
	if err != nil {
		t.Fatalf("loading the committed input set: %v", err)
	}
	for _, file := range files {
		if file.Rel == rel {
			return file
		}
	}
	t.Fatalf("no pinned input %s", rel)
	return inputFile{}
}

// failingImporter is an importer that always fails.
type failingImporter struct{ err error }

func (f failingImporter) Import(context.Context, v1.Request) (v1.DryRunResult, error) {
	return v1.DryRunResult{}, f.err
}

// recordingRunner is a custody command runner that records every call and
// pretends a default keychain exists at path.
type recordingRunner struct {
	path  string
	calls []string
}

func (r *recordingRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	for _, arg := range args {
		if arg == "default-keychain" {
			return []byte(`"` + r.path + `"`), nil
		}
	}
	return nil, nil
}

// newRecordingRunner returns a runner whose keychain path really exists.
func newRecordingRunner(t *testing.T) *recordingRunner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "login.keychain-db")
	writeTestFile(t, path, []byte("not a keychain"))
	return &recordingRunner{path: path}
}

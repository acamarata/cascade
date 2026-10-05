package tripwire

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var testDirs = []string{
	"internal/memory/testdata/v1-goldens/migration",
	"internal/secrets/testdata/v1-goldens/migration",
	"internal/providers/registry/testdata/v1-goldens/migration",
	"internal/runtime/testdata/v1-goldens/migration",
}

const testReference = "internal/migration/testdata/golden-checksums.sha256"

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureModule(t *testing.T) (string, []string) {
	t.Helper()
	root := t.TempDir()
	put(t, filepath.Join(root, "go.mod"), "module github.com/acamarata/cascade\n")
	var roots, lines []string
	for _, dir := range testDirs {
		roots = append(roots, filepath.Join(root, filepath.FromSlash(dir)))
		put(t, filepath.Join(root, filepath.FromSlash(dir), "fixture.json"), "{}\n")
		lines = append(lines, fmt.Sprintf("%x  %s/fixture.json", sha256.Sum256([]byte("{}\n")), dir))
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i][66:] < lines[j][66:] })
	put(t, filepath.Join(root, testReference), "# harvester format 1\n"+strings.Join(lines, "\n")+"\n")
	return root, roots
}

func TestComputeDigest_DetectsAddRemoveChange(t *testing.T) {
	for _, op := range []string{"add", "remove", "change", "rename", "nested", "crlf"} {
		t.Run(op, func(t *testing.T) {
			root, roots := fixtureModule(t)
			before, err := ComputeDigest(roots)
			if err != nil {
				t.Fatal(err)
			}
			rel := testDirs[0] + "/fixture.json"
			switch op {
			case "add", "nested":
				rel = testDirs[0] + "/new/sub.json"
				put(t, filepath.Join(root, rel), "{}\n")
			case "remove":
				if err := os.Remove(filepath.Join(root, rel)); err != nil {
					t.Fatal(err)
				}
			case "rename":
				if err := os.Rename(filepath.Join(root, rel), filepath.Join(root, testDirs[0], "renamed.json")); err != nil {
					t.Fatal(err)
				}
			case "crlf":
				put(t, filepath.Join(root, rel), "{}\r\n")
			default:
				put(t, filepath.Join(root, rel), "changed\n")
			}
			after, err := ComputeDigest(roots)
			if err != nil || before.SHA256 == after.SHA256 {
				t.Fatalf("digest did not change: %v", err)
			}
			report, err := VerifyTripwire(root, testReference)
			if err == nil || !strings.Contains(strings.Join(report.Paths, " "), rel) || !strings.Contains(err.Error(), rel) {
				t.Fatalf("missing named failure for %s: %+v, %v", rel, report, err)
			}
		})
	}
}

func TestVerifyTripwire_EmptySetRefuses(t *testing.T) {
	for _, mode := range []string{"empty-reference", "missing-reference", "no-roots", "empty-fixtures"} {
		t.Run(mode, func(t *testing.T) {
			root, roots := fixtureModule(t)
			switch mode {
			case "empty-reference":
				put(t, filepath.Join(root, testReference), "# nothing\n")
			case "missing-reference":
				if err := os.Remove(filepath.Join(root, testReference)); err != nil {
					t.Fatal(err)
				}
			case "no-roots":
				if _, err := ComputeDigest(nil); err == nil {
					t.Fatal("empty roots accepted")
				}
				return
			default:
				for _, dir := range roots {
					if err := os.Remove(filepath.Join(dir, "fixture.json")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := VerifyTripwire(root, testReference); err == nil {
				t.Fatal("empty or missing checksum set accepted")
			}
		})
	}
}

func TestTripwireStable(t *testing.T) {
	root, ok := ModuleRoot(".")
	if !ok {
		t.Fatal("module root missing")
	}
	if report, err := VerifyTripwire(root, testReference); err != nil {
		t.Fatalf("%+v: %v", report, err)
	}
}

func TestTripwireDetectsChange(t *testing.T) {
	root, rel := copyCommittedFixtures(t)
	put(t, filepath.Join(root, rel), "different\n")
	report, err := VerifyTripwire(root, testReference)
	if err == nil || !strings.Contains(strings.Join(report.Paths, " "), rel) {
		t.Fatalf("%+v: %v", report, err)
	}
}

func TestTripwireWindowsDigest(t *testing.T) {
	root, _ := copyCommittedFixtures(t)
	var roots []string
	for _, dir := range testDirs {
		roots = append(roots, filepath.Join(root, filepath.FromSlash(dir)))
	}
	digest, err := ComputeDigest(roots)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	data, err := os.ReadFile(filepath.Join(root, testReference))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "# ") {
			continue
		}
		sum, path, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatal("malformed committed reference")
		}
		lines = append(lines, path+"\t"+sum+"\n")
	}
	sort.Strings(lines)
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(lines, ""))))
	if digest.SHA256 != want {
		t.Fatalf("digest %s, want %s", digest.SHA256, want)
	}
	if _, err := VerifyTripwire(root, testReference); err != nil {
		t.Fatal(err)
	}
}

// copyCommittedFixtures gives byte-mutation tests their own fixture checkout.
func copyCommittedFixtures(t *testing.T) (string, string) {
	t.Helper()
	source, ok := ModuleRoot(".")
	if !ok {
		t.Fatal("source module missing")
	}
	root := t.TempDir()
	put(t, filepath.Join(root, "go.mod"), "module github.com/acamarata/cascade\n")
	first := ""
	for _, dir := range append(append([]string{}, testDirs...), testReference) {
		err := filepath.WalkDir(filepath.Join(source, filepath.FromSlash(dir)), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			put(t, filepath.Join(root, rel), string(data))
			if first == "" && strings.HasSuffix(rel, ".json") {
				first = filepath.ToSlash(rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if first == "" {
		t.Fatal("no golden JSON fixtures copied")
	}
	return root, first
}

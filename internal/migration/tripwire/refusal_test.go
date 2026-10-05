package tripwire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeDigest_RefusesSymlinkAndEscape(t *testing.T) {
	for _, mode := range []string{"file-link", "root-link", "parent-link", "escape", "missing", "file-root"} {
		t.Run(mode, func(t *testing.T) {
			root, roots := fixtureModule(t)
			outside := t.TempDir()
			switch mode {
			case "escape":
				roots = append(roots, outside)
			case "missing":
				roots = append(roots, filepath.Join(root, "missing"))
			case "file-root":
				roots = append(roots, filepath.Join(roots[0], "fixture.json"))
			default:
				link, target := filepath.Join(roots[0], "link"), filepath.Join(roots[0], "fixture.json")
				if mode != "file-link" {
					link, target = filepath.Join(root, "link"), roots[0]
				}
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				if mode == "root-link" {
					roots = append(roots, link)
				}
				if mode == "parent-link" {
					if err := os.Mkdir(filepath.Join(target, "sub"), 0o700); err != nil {
						t.Fatal(err)
					}
					roots = append(roots, filepath.Join(link, "sub"))
				}
			}
			if _, err := ComputeDigest(roots); err == nil {
				t.Fatal("unsafe root or file accepted")
			}
		})
	}
}

func TestModuleRoot(t *testing.T) {
	root, roots := fixtureModule(t)
	if got, ok := ModuleRoot(roots[0]); !ok || got != root {
		t.Fatalf("root = %q, %v", got, ok)
	}
	for _, module := range []string{"example.test/other", "github.com/acamarata/cascade-extra"} {
		put(t, filepath.Join(roots[0], "go.mod"), "module "+module+"\n")
		if got, ok := ModuleRoot(roots[0]); ok || got != "" {
			t.Fatalf("foreign module accepted: %q", got)
		}
	}
	if _, ok := ModuleRoot(""); ok {
		t.Fatal("empty working directory accepted")
	}
	if _, ok := ModuleRoot(t.TempDir()); ok {
		t.Fatal("outside checkout accepted")
	}
	if _, err := ComputeDigest([]string{t.TempDir()}); err == nil {
		t.Fatal("no module accepted")
	}
}

func TestReferenceRefusals(t *testing.T) {
	for _, row := range []string{"bogus", strings.Repeat("g", 64) + "  x", strings.Repeat("a", 64) + "  ../escape", strings.Repeat("a", 64) + "  " + testDirs[0] + "/../escape", strings.Repeat("a", 64) + "  " + testDirs[0] + "/back\\slash"} {
		root, _ := fixtureModule(t)
		put(t, filepath.Join(root, testReference), row+"\n")
		if _, err := VerifyTripwire(root, testReference); err == nil {
			t.Fatalf("bad row accepted: %q", row)
		}
	}
	root, _ := fixtureModule(t)
	data, err := os.ReadFile(filepath.Join(root, testReference))
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, testReference), string(data)+string(data))
	if _, err := VerifyTripwire(root, testReference); err == nil {
		t.Fatal("duplicate rows accepted")
	}
}

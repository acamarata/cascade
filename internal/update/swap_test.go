package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func stageFrom(t *testing.T, installDir string, files map[string]string) []string {
	t.Helper()
	src := t.TempDir()
	va := VerifiedArchive{Dir: src}
	for _, base := range executableBases {
		if body, ok := files[base]; ok {
			put(t, src, base, []byte(body))
			va.Executables = append(va.Executables, base)
		}
	}
	staged, err := Stage(va, installDir)
	must(t, err)
	return staged
}

// prevReceipt is a valid receipt for installDir, the one Swap keeps.
func prevReceipt(installDir string) Receipt {
	return Receipt{SchemaVersion: 1, Channel: "script", ReleaseTag: "v2.0.0", ArchiveName: "a",
		ArchiveSHA256: hexSum([]byte("a")), InstallDir: installDir, InstalledAt: "2026-01-01T00:00:00Z",
		Files: []ReceiptFile{{Name: exeName("cascade", receiptGOOS), SHA256: hexSum([]byte("cascade old"))}}}
}

func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	must(t, err)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		must(t, err)
		out[e.Name()] = string(b)
	}
	return out
}

// TestSwapFailureRestoresEverything is acceptance [1]: a rename failure on
// cascade after the helper and plugin swapped restores every file
// byte-identical and keeps the previous dir.
func TestSwapFailureRestoresEverything(t *testing.T) {
	install, prev := t.TempDir(), filepath.Join(t.TempDir(), "previous")
	old := map[string]string{"cascade-elevate-helper": "helper old", "cascade-github": "github old", "cascade": "cascade old"}
	for n, b := range old {
		put(t, install, n, []byte(b))
	}
	staged := stageFrom(t, install, map[string]string{"cascade-elevate-helper": "helper new", "cascade-github": "github new", "cascade": "cascade new"})
	orig := renameFile
	t.Cleanup(func() { renameFile = orig })
	var order []string
	failed := false
	renameFile = func(src, dst string) error {
		order = append(order, filepath.Base(dst))
		if filepath.Base(dst) == "cascade" && !failed {
			failed = true
			return errors.New("injected rename failure")
		}
		return orig(src, dst)
	}
	_, err := Swap(staged, install, prev, prevReceipt(install))
	wantKind(t, err, cascade.KindUnavailable, "swap cascade")
	if strings.Join(order[:3], ",") != "cascade-elevate-helper,cascade-github,cascade" {
		t.Fatalf("rename order = %v, want helper, plugin, cascade", order)
	}
	if got := readDir(t, install); len(got) != 3 || got["cascade"] != old["cascade"] ||
		got["cascade-github"] != old["cascade-github"] || got["cascade-elevate-helper"] != old["cascade-elevate-helper"] {
		t.Fatalf("install dir after a failed swap = %v, want %v", got, old)
	}
	if got := readDir(t, prev); len(got) != 4 || got["cascade"] != old["cascade"] || got["cascade-elevate-helper"] != old["cascade-elevate-helper"] || got[previousReceiptName] == "" {
		t.Fatalf("previous dir = %v", got)
	}
}

// TestSwapKeepsOnePreviousGeneration: the second swap's previous dir holds
// only the first swap's result, nothing older.
func TestSwapKeepsOnePreviousGeneration(t *testing.T) {
	install, prev := t.TempDir(), filepath.Join(t.TempDir(), "previous")
	put(t, install, "cascade", []byte("gen0"))
	put(t, install, "cascade-elevate-helper", []byte("helper gen0"))
	_, err := Swap(stageFrom(t, install, map[string]string{"cascade-elevate-helper": "helper gen1", "cascade": "gen1"}), install, prev, prevReceipt(install))
	must(t, err)
	rec, err := Swap(stageFrom(t, install, map[string]string{"cascade-github": "github gen2", "cascade": "gen2"}), install, prev, prevReceipt(install))
	must(t, err)
	if got := readDir(t, prev); len(got) != 2 || got["cascade"] != "gen1" || got[previousReceiptName] == "" {
		t.Fatalf("previous dir = %v, want exactly cascade gen1 and its receipt", got)
	}
	if got := readDir(t, install); got["cascade"] != "gen2" || got["cascade-github"] != "github gen2" || got["cascade-elevate-helper"] != "helper gen1" {
		t.Fatalf("install dir = %v", got)
	}
	must(t, Restore(rec))
	if got := readDir(t, install); got["cascade"] != "gen1" || got["cascade-github"] != "" {
		t.Fatalf("after restore = %v, want cascade gen1 and no plugin", got)
	}
}

// TestUpdateArchiveWithoutHelperLeavesDirUntouched is acceptance [15]: a
// windows-shaped archive with no helper installs no helper.
func TestUpdateArchiveWithoutHelperLeavesDirUntouched(t *testing.T) {
	key, dist, work, install := newTestKey(3), t.TempDir(), t.TempDir(), t.TempDir()
	win := Platform{OS: "windows", Arch: "amd64"}
	writeDist(t, dist, distSpec{version: "2.0.1", key: key, platform: win, members: []member{
		{name: "cascade-github.exe", body: "github"}, {name: "cascade.exe", body: "cascade"}, {name: "LICENSE", body: "l"},
	}})
	va, err := FetchVerified(context.Background(), DirFetcher{Dir: dist}, Source{Dist: dist}, "v2.0.1", win, key.pub(t), work)
	must(t, err)
	if strings.Join(va.Executables, ",") != "cascade-github.exe,cascade.exe" {
		t.Fatalf("executables = %v", va.Executables)
	}
	staged, err := Stage(va, install)
	must(t, err)
	_, err = Swap(staged, install, filepath.Join(t.TempDir(), "previous"), prevReceipt(install))
	must(t, err)
	var names []string
	for n := range readDir(t, install) {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "cascade-github.exe,cascade.exe" {
		t.Fatalf("install dir = %v, want no helper and no staged leftovers", names)
	}
}

// TestStageRefusesSymlinkTarget: a symlink at a target refuses before any write.
func TestStageRefusesSymlinkTarget(t *testing.T) {
	install, src := t.TempDir(), t.TempDir()
	put(t, src, "cascade", []byte("new"))
	must(t, os.Symlink(filepath.Join(src, "cascade"), filepath.Join(install, "cascade")))
	_, err := Stage(VerifiedArchive{Dir: src, Executables: []string{"cascade"}}, install)
	wantKind(t, err, cascade.KindIntegrity, "not a regular file")
	_, err = Swap([]string{filepath.Join(install, "cascade")}, install, t.TempDir(), prevReceipt(install))
	wantKind(t, err, cascade.KindInvalidInput, "not a staged executable")
}

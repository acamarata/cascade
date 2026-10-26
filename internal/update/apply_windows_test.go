//go:build windows

package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestApplyWindowsRenameOld is acceptance [9] (windows): the old files are
// renamed to <name>.old, the new ones take their place, and no daemon is
// drained or signalled.
func TestApplyWindowsRenameOld(t *testing.T) {
	win := Platform{OS: "windows", Arch: "amd64"}
	f := newFixture(t, "2.0.0", "2.0.0")
	for _, n := range []string{"cascade-elevate-helper", "cascade-github", "cascade"} {
		must(t, os.Remove(filepath.Join(f.installDir, n)))
	}
	useReceiptGOOS(t, "windows") // from here the receipt lists .exe names
	var files []ReceiptFile
	for _, n := range []string{"cascade-github.exe", "cascade.exe"} {
		body := []byte(n + " 2.0.0")
		put(t, f.installDir, n, body)
		files = append(files, ReceiptFile{Name: n, SHA256: hexSum(body)})
	}
	must(t, WriteReceipt(f.receipt, Receipt{SchemaVersion: 1, Channel: "script", ReleaseTag: "v2.0.0",
		ArchiveName: archiveNameFor("2.0.0", win), ArchiveSHA256: hexSum([]byte("old")), InstallDir: f.installDir,
		InstalledAt: "2026-01-01T00:00:00Z", Files: files}))
	must(t, os.RemoveAll(f.dist))
	writeDist(t, f.dist, distSpec{version: "2.0.1", key: f.key, platform: win, members: []member{
		{name: "cascade-github.exe", body: "cascade-github.exe 2.0.1"}, {name: "cascade.exe", body: "cascade.exe 2.0.1"},
	}})
	d := f.deps("2.0.0", nil)
	d.Platform = win
	res, err := Apply(context.Background(), d)
	must(t, err)
	if !res.Updated || res.DaemonSignalled {
		t.Fatalf("apply = %+v", res)
	}
	for _, n := range []string{"cascade-github.exe", "cascade.exe"} {
		if got := f.read(n); got != n+" 2.0.1" {
			t.Fatalf("%s = %q", n, got)
		}
		if got := f.read(n + ".old"); got != n+" 2.0.0" {
			t.Fatalf("%s.old = %q, want the renamed old file", n, got)
		}
	}
}

// TestRestoreWindowsRenamesOldBack: after a rename-old swap, Restore moves
// the new image aside and renames <name>.old back by renames only, so the
// image a running process holds is never deleted or overwritten.
func TestRestoreWindowsRenamesOldBack(t *testing.T) {
	install, src, prev := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "previous")
	put(t, install, "cascade.exe", []byte("cascade old"))
	put(t, src, "cascade.exe", []byte("cascade new"))
	staged, err := Stage(VerifiedArchive{Dir: src, Executables: []string{"cascade.exe"}}, install)
	must(t, err)
	rec, err := Swap(staged, install, prev, prevReceipt(install))
	must(t, err)
	if got := readDir(t, install); got["cascade.exe"] != "cascade new" || got["cascade.exe.old"] != "cascade old" {
		t.Fatalf("after the swap = %v", got)
	}
	must(t, Restore(rec))
	got := readDir(t, install)
	if got["cascade.exe"] != "cascade old" || got["cascade.exe.failed-1"] != "cascade new" {
		t.Fatalf("after restore = %v, want the old image back and the new one aside", got)
	}
	if _, ok := got["cascade.exe.old"]; ok {
		t.Fatalf("cascade.exe.old still present after restore: %v", got)
	}
}

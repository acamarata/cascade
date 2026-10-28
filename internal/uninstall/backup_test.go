package uninstall

// Purpose: acceptance [3]: no backup is ever removed, at any depth, by any
//   item, even with --purge-data.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestUninstallNeverTouchesBackups(t *testing.T) {
	f := newFixture(t, "linux")
	data := f.paths.DataDir()
	deep := filepath.Join(data, "backups", "snap-1", "objects", "obj-1")
	nested := filepath.Join(data, "storage", "local", "backups", "pre-migrate.db")
	target := filepath.Join(data, "offsite-mirror", "repo", "chunk-1")
	for _, p := range []string{deep, nested, target} {
		mustWrite(t, p, "fake backup bytes\n")
	}
	f.deps.BackupTargets = func() ([]string, error) { return []string{filepath.Join(data, "offsite-mirror")}, nil }
	digests := map[string]string{}
	for _, p := range []string{filepath.Join(data, "backups"), filepath.Dir(filepath.Dir(nested)), filepath.Join(data, "offsite-mirror")} {
		digests[p] = treeDigest(t, p)
	}
	if _, err := Run(t.Context(), mustInventory(t, f.deps), Options{PurgeData: true}); err != nil {
		t.Fatalf("purge run: %v", err)
	}
	for p, want := range digests {
		if !exists(p) || treeDigest(t, p) != want {
			t.Fatalf("backup %s changed by a purge", p)
		}
	}
	if exists(filepath.Join(data, "cascade.db")) || exists(f.paths.ConfigPath()) {
		t.Fatal("purge kept a non-backup data item")
	}
	for _, p := range []string{deep, nested, target, filepath.Join(data, "BACKUPS", "x")} {
		if !IsBackupPath(p, f.deps) {
			t.Fatalf("IsBackupPath(%s) = false", p)
		}
	}
	if IsBackupPath(filepath.Join(data, "cascade.db"), f.deps) || IsBackupPath("plugin:backups", f.deps) {
		t.Fatal("IsBackupPath flagged a non-backup")
	}
	crafted := fileItem{kind: KindConfig, locator: deep, d: f.deps}
	d, err := crafted.Remove(t.Context(), false)
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindIntegrity || d.Action != ActionKept || !exists(deep) {
		t.Fatalf("crafted backup item = %+v, %v", d, err)
	}
}

func TestBackupGuardFollowsSymlinksAndFailsClosed(t *testing.T) {
	f := newFixture(t, "linux")
	data := f.paths.DataDir()
	obj := filepath.Join(data, "backups", "obj")
	mustWrite(t, obj, "fake backup bytes\n")
	link := filepath.Join(f.top, "alias")
	if err := os.Symlink(filepath.Join(data, "backups"), link); err == nil {
		if !IsBackupPath(filepath.Join(link, "obj"), f.deps) {
			t.Fatal("a symlinked alias of the backup dir escaped the guard")
		}
	}
	f.deps.BackupTargets = func() ([]string, error) { return nil, errors.New("fake unreadable target store") }
	if !IsBackupPath(f.paths.ConfigPath(), f.deps) {
		t.Fatal("an unreadable target list did not fail closed")
	}
	if _, err := Inventory(t.Context(), f.deps); err == nil {
		t.Fatal("Inventory proceeded without the backup target list")
	}
}

func TestPurgeLeavesNoDataBehindWithoutBackups(t *testing.T) {
	f := newFixture(t, "linux")
	mustWrite(t, filepath.Join(f.paths.DataDir(), elevationKeyFile), "fake key\n")
	var helperCalls int
	f.deps.HelperDelete = func(context.Context) error {
		helperCalls++
		if helperCalls > 1 {
			return cascade.New(cascade.KindNotFound, "fake: no enrolled key")
		}
		return nil
	}
	items := mustInventory(t, f.deps)
	if _, err := Run(t.Context(), items, Options{PurgeData: true}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if exists(f.paths.DataDir()) || exists(f.paths.ConfigPath()) || helperCalls != 1 {
		t.Fatalf("data dir %v, config %v, helper calls %d", exists(f.paths.DataDir()), exists(f.paths.ConfigPath()), helperCalls)
	}
	rep, err := Run(t.Context(), mustInventory(t, f.deps), Options{PurgeData: true})
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	for _, d := range rep.Items {
		if d.Action != ActionAbsent {
			t.Fatalf("second purge %+v; want absent", d)
		}
	}
}

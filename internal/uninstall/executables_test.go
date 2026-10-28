package uninstall

// Purpose: acceptance [5], [10] and [11]: executables go only on a
//   matching digest from a receipt whose install_dir is the running
//   binary's real directory and whose names are the fixed set.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/update"
	"github.com/acamarata/cascade/pkg/cascade"
)

// deltaFor finds the delta with locator.
func deltaFor(t *testing.T, rep Report, locator string) Delta {
	t.Helper()
	for _, d := range rep.Items {
		if d.Locator == locator {
			return d
		}
	}
	t.Fatalf("no delta for %s in %+v", locator, rep.Items)
	return Delta{}
}

func TestExecutablesRemovedOnlyWhenDigestMatches(t *testing.T) {
	f := newFixture(t, "linux")
	gh := filepath.Join(f.installDir, "cascade-github")
	mustWrite(t, gh, "fake binary replaced by hand\n")
	rep, err := Run(t.Context(), mustInventory(t, f.deps), Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if d := deltaFor(t, rep, gh); d.Action != ActionKept || !strings.Contains(d.Note, "modified since install") || !exists(gh) {
		t.Fatalf("modified binary = %+v (exists %v); want kept, modified since install", d, exists(gh))
	}
	for _, n := range []string{"cascade", "cascade-elevate-helper"} {
		if p := filepath.Join(f.installDir, n); exists(p) || deltaFor(t, rep, p).Action != ActionRemoved {
			t.Fatalf("matching %s was not removed", n)
		}
	}
	for _, ch := range []string{"brew", "manual"} {
		t.Run(ch, func(t *testing.T) {
			f := newFixture(t, "linux")
			f.receipt.Channel = ch
			rep, err := Run(t.Context(), mustInventory(t, f.deps), Options{})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			for _, rf := range f.receipt.Files {
				p := filepath.Join(f.installDir, rf.Name)
				d := deltaFor(t, rep, p)
				if !exists(p) || d.Action != ActionKept || d.Note != channelRemedy[ch] {
					t.Fatalf("%s binary %s = %+v; want kept with the channel remedy", ch, rf.Name, d)
				}
			}
		})
	}
	t.Run("no receipt", func(t *testing.T) {
		f := newFixture(t, "linux")
		f.deps.Receipt = func() (update.Receipt, bool, error) { return update.Receipt{}, false, nil }
		rep, _ := Run(t.Context(), mustInventory(t, f.deps), Options{})
		exe := filepath.Join(f.installDir, "cascade")
		if d := deltaFor(t, rep, exe); d.Action != ActionKept || !strings.Contains(d.Note, "by hand") || !exists(exe) {
			t.Fatalf("receipt-less binary = %+v", d)
		}
	})
}

// assertNoExecutableRemoved runs uninstall and checks the receipt refusal.
func assertNoExecutableRemoved(t *testing.T, f *fixture) {
	t.Helper()
	rep, err := Run(t.Context(), mustInventory(t, f.deps), Options{})
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindIntegrity {
		t.Fatalf("run = %v; want a KindIntegrity refusal", err)
	}
	for _, b := range executableBases {
		if !exists(filepath.Join(f.installDir, b)) {
			t.Fatalf("%s was removed under a refused receipt", b)
		}
	}
	if !exists(filepath.Join(f.paths.Root(), receiptFileName)) {
		t.Fatal("the refused receipt was removed")
	}
	for _, d := range rep.Items {
		if d.Kind == KindExecutable && d.Action != ActionKept {
			t.Fatalf("executable delta %+v under a refused receipt", d)
		}
	}
}

func TestUninstallRefusesForeignInstallDir(t *testing.T) {
	t.Run("not the running binary's dir", func(t *testing.T) {
		f := newFixture(t, "linux")
		f.deps.Executable = func() (string, error) { return filepath.Join(f.top, "elsewhere", "cascade"), nil }
		mustWrite(t, filepath.Join(f.top, "elsewhere", "cascade"), "fake other binary\n")
		assertNoExecutableRemoved(t, f)
	})
	t.Run("relative", func(t *testing.T) {
		f := newFixture(t, "linux")
		f.receipt.InstallDir = filepath.Join("home", "bin")
		assertNoExecutableRemoved(t, f)
	})
	t.Run("symlinked", func(t *testing.T) {
		f := newFixture(t, "linux")
		link := filepath.Join(f.top, "linked-bin")
		if err := os.Symlink(f.installDir, link); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
		f.receipt.InstallDir = link
		assertNoExecutableRemoved(t, f)
	})
	t.Run("symlink planted at an executable", func(t *testing.T) {
		f := newFixture(t, "linux")
		victim := filepath.Join(f.top, "victim")
		mustWrite(t, victim, "fake binary cascade-github\n")
		gh := filepath.Join(f.installDir, "cascade-github")
		_ = os.Remove(gh)
		if err := os.Symlink(victim, gh); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
		rep, err := Run(t.Context(), mustInventory(t, f.deps), Options{})
		if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindIntegrity {
			t.Fatalf("run = %v; want KindIntegrity for the planted link", err)
		}
		if d := deltaFor(t, rep, gh); d.Action != ActionKept || !exists(victim) || !exists(gh) {
			t.Fatalf("planted link = %+v; victim %v link %v", d, exists(victim), exists(gh))
		}
		receipt := filepath.Join(f.paths.Root(), receiptFileName)
		if d := deltaFor(t, rep, receipt); d.Action != ActionKept || !exists(receipt) {
			t.Fatalf("receipt = %+v; want kept while a listed path is refused", d)
		}
	})
}

func TestUninstallRefusesUnsafeReceiptNames(t *testing.T) {
	for _, name := range []string{"/etc/hosts", "../x", "cascade-evil", "cascade"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "linux")
			// "cascade" twice: a duplicate entry is refused as well.
			f.receipt.Files = append(f.receipt.Files, update.ReceiptFile{Name: name, SHA256: digestOf("x")})
			assertNoExecutableRemoved(t, f)
		})
	}
	t.Run("receipt read error", func(t *testing.T) {
		f := newFixture(t, "linux")
		f.deps.Receipt = func() (update.Receipt, bool, error) {
			return update.Receipt{}, false, cascade.New(cascade.KindIntegrity, "fake corrupt receipt")
		}
		assertNoExecutableRemoved(t, f)
	})
}

func TestUninstallMixedInstallRemovesOnlyMatchingFiles(t *testing.T) {
	f := newFixture(t, "linux")
	helper := filepath.Join(f.installDir, "cascade-elevate-helper")
	gh := filepath.Join(f.installDir, "cascade-github")
	mustWrite(t, helper, "fake helper from an interrupted newer install\n")
	mustWrite(t, gh, "fake plugin from an interrupted newer install\n")
	rep, err := Run(t.Context(), mustInventory(t, f.deps), Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exists(filepath.Join(f.installDir, "cascade")) {
		t.Fatal("the matching cascade binary was kept")
	}
	for _, p := range []string{helper, gh} {
		d := deltaFor(t, rep, p)
		if !exists(p) || d.Action != ActionKept || !strings.Contains(d.Note, "rerun install.sh, then cascade uninstall") {
			t.Fatalf("drifted %s = %+v; want kept with the remedy", p, d)
		}
	}
	if d := deltaFor(t, rep, filepath.Join(f.paths.Root(), receiptFileName)); d.Action != ActionKept {
		t.Fatalf("receipt = %+v; want kept while files it lists remain", d)
	}
}

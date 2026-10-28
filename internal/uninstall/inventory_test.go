package uninstall

// Purpose: acceptance [0], [1] and [2]: the inventory is typed, ordered and
//   read-only; a dry run removes nothing; only indexed vault names go.

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type wantItem struct {
	kind    Kind
	class   Class
	locator string
}

func TestInventoryIsTypedAndReadOnly(t *testing.T) {
	f := newVaultFixture(t, "darwin")
	before := treeDigest(t, f.top)
	items := mustInventory(t, f.deps)
	if after := treeDigest(t, f.top); after != before {
		t.Fatal("Inventory changed the tree")
	}
	want := []wantItem{
		{KindDaemonUnit, ClassInstall, daemonUnitPath("darwin", f.home)},
		{KindWidgetAgent, ClassInstall, filepath.Join(f.home, "Library", "LaunchAgents", "com.acamarata.cascade.widget.plist")},
		{KindPlugin, ClassInstall, "plugin:cascade-github"},
		{KindPlugin, ClassInstall, "plugin:cascade-pa"},
		{KindVaultEntry, ClassInstall, "vault:FAKE_TOKEN_A"},
		{KindVaultEntry, ClassInstall, "vault:FAKE_TOKEN_B"},
		{KindExecutable, ClassInstall, filepath.Join(f.installDir, "cascade-elevate-helper")},
		{KindExecutable, ClassInstall, filepath.Join(f.installDir, "cascade-github")},
		{KindExecutable, ClassInstall, filepath.Join(f.installDir, "cascade")},
		{KindInstallReceipt, ClassInstall, filepath.Join(f.paths.Root(), receiptFileName)},
		{KindElevationKey, ClassData, filepath.Join(f.paths.DataDir(), elevationKeyFile)},
		{KindConfig, ClassData, f.paths.ConfigPath()},
		{KindDataDir, ClassData, f.paths.DataDir()},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %v", len(items), len(want), locators(items))
	}
	for i, w := range want {
		it := items[i]
		if it.Kind() != w.kind || it.Class() != w.class || it.Locator() != w.locator || it.Owner() == "" {
			t.Errorf("item %d = %s/%s %s (owner %q), want %s/%s %s", i, it.Kind(), it.Class(), it.Locator(), it.Owner(), w.kind, w.class, w.locator)
		}
	}
}

func TestDryRunRemovesNothing(t *testing.T) {
	f := newVaultFixture(t, "darwin")
	before := treeDigest(t, f.top)
	items := mustInventory(t, f.deps)
	rep, err := Run(t.Context(), items, Options{DryRun: true, PurgeData: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if after := treeDigest(t, f.top); after != before {
		t.Fatal("a dry run changed the tree")
	}
	if len(rep.Items) != len(items) {
		t.Fatalf("dry run reported %d of %d items", len(rep.Items), len(items))
	}
	for _, d := range rep.Items {
		if d.Kind == KindElevationKey {
			continue // the fixture has no file-tier key
		}
		if d.Action != ActionWouldRemove {
			t.Errorf("%s %s: action %q, want would-remove", d.Kind, d.Locator, d.Action)
		}
	}
	if len(f.log.ops) != 0 {
		t.Fatalf("a dry run called a remover: %v", f.log.ops)
	}
}

func TestUninstallPurgesOnlyIndexedEntries(t *testing.T) {
	f := newVaultFixture(t, "darwin")
	if _, err := Run(t.Context(), mustInventory(t, f.deps), Options{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	left, err := f.custody.List(t.Context())
	if err != nil || len(left) != 0 {
		t.Fatalf("cascade vault after uninstall = %v, %v; want empty", left, err)
	}
	other, err := f.other.List(t.Context())
	if err != nil || !slices.Equal(other, []string{"FAKE_OTHER_ENTRY"}) {
		t.Fatalf("second-service vault = %v, %v; want its entry untouched", other, err)
	}
	if !exists(f.paths.ConfigPath()) || !exists(f.paths.DataDir()) {
		t.Fatal("a default uninstall removed data-class items")
	}
}

func TestVaultItemNeverDeletesAnUnindexedName(t *testing.T) {
	f := newFixture(t, "darwin")
	item := vaultItem{name: "FAKE_OTHER_ENTRY", custody: f.custody}
	d, err := item.Remove(t.Context(), false)
	if err != nil || d.Action != ActionAbsent {
		t.Fatalf("unindexed name = %+v, %v; want absent", d, err)
	}
	if got, _ := f.other.List(t.Context()); len(got) != 1 {
		t.Fatalf("second-service vault changed: %v", got)
	}
}

func TestRunContinuesPastAFailedItem(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "fake failure")
	items := []Item{
		keptItem{delta: Delta{Kind: KindPlugin, Locator: "plugin:x", Action: ActionKept}, err: boom},
		keptItem{delta: Delta{Kind: KindPlugin, Locator: "plugin:y", Action: ActionAbsent}},
	}
	rep, err := Run(t.Context(), items, Options{})
	if len(rep.Items) != 2 || err == nil || !strings.Contains(err.Error(), "plugin:x") || strings.Contains(err.Error(), "plugin:y") {
		t.Fatalf("Run = %+v, %v", rep, err)
	}
	if k, _ := cascade.KindOf(err); k != cascade.KindUnavailable {
		t.Fatalf("joined kind = %v", k)
	}
}

func locators(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, string(it.Kind())+" "+it.Locator())
	}
	return out
}

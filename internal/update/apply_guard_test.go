package update

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestDowngradeRuleRefusesUnversionedReceipt: a receipt whose release_tag
// is empty or not a version cannot vouch for the installed version, so the
// "older than installed" check refuses instead of being skipped.
func TestDowngradeRuleRefusesUnversionedReceipt(t *testing.T) {
	for _, tag := range []string{"", "local", "2.0.0", "v2.0.0-01"} {
		_, err := downgradeRule("2.0.1", "2.0.0", Receipt{ReleaseTag: tag})
		wantKind(t, err, cascade.KindIntegrity, "is not a version; nothing was changed")
	}
	cmp, err := downgradeRule("2.0.1", "2.0.0", Receipt{ReleaseTag: "v2.0.0"})
	if err != nil || cmp != 1 {
		t.Fatalf("valid receipt: cmp=%d err=%v, want 1, nil", cmp, err)
	}
}

// TestApplyUnlockedScriptPlanRefused: when the channel resolves to script
// only after Apply took the unlocked branch, the update is refused with
// KindConflict and nothing is written or signalled.
func TestApplyUnlockedScriptPlanRefused(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	before := treeSum(t, f.home)
	d := f.deps("2.0.0", nil)
	signals := 0
	d.Daemon.Status = func(context.Context) (bool, int, error) { return true, 7, nil }
	d.Daemon.Signal = func(int) error { signals++; return nil }
	res, err := applyLocked(context.Background(), d, false)
	wantKind(t, err, cascade.KindConflict, "install channel changed to script")
	if res.Updated || signals != 0 {
		t.Fatalf("updated=%v signals=%d, want no update and no signal", res.Updated, signals)
	}
	if after := treeSum(t, f.home); after != before {
		t.Fatal("the unlocked refusal changed the install tree")
	}
}

// TestApplyEqualToInstalledReceiptIsNone: another updater already installed
// the latest version (the receipt names it) while this binary still runs
// the older one: nothing to do, even when the served archive digest differs
// from the receipt's, and nothing is written or signalled.
func TestApplyEqualToInstalledReceiptIsNone(t *testing.T) {
	f := newFixture(t, "2.0.1", "2.0.1")
	before := treeSum(t, f.home)
	d := f.deps("2.0.0", nil)
	signals := 0
	d.Daemon.Status = func(context.Context) (bool, int, error) { return true, 7, nil }
	d.Daemon.Signal = func(int) error { signals++; return nil }
	res, err := Apply(context.Background(), d)
	must(t, err)
	if res.Action != ActionNone || res.Updated || signals != 0 {
		t.Fatalf("apply = %+v signals=%d, want none and no signal", res, signals)
	}
	if res.Latest.ArchiveSHA256 == hexSum([]byte("archive 2.0.1")) {
		t.Fatal("fixture: the served digest must differ from the receipt's")
	}
	if treeSum(t, f.home) != before {
		t.Fatal("an equal-to-installed no-op wrote to the home tree")
	}
}

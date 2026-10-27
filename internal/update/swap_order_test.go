package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// injectRename replaces renameFile for one test.
func injectRename(t *testing.T, fn func(orig func(string, string) error, src, dst string) error) {
	t.Helper()
	orig := renameFile
	t.Cleanup(func() { renameFile = orig })
	renameFile = func(src, dst string) error { return fn(orig, src, dst) }
}

// TestApplyKeepsPreviousBeforeFirstRename: the previous files and the
// previous receipt are in <dataDir>/update/previous before the first
// rename, so an interrupted swap still has its way back on disk.
func TestApplyKeepsPreviousBeforeFirstRename(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	prev := filepath.Join(f.dataDir, "update", "previous")
	var seen map[string]string
	injectRename(t, func(orig func(string, string) error, src, dst string) error {
		if seen == nil && strings.HasSuffix(src, ".new") {
			seen = map[string]string{}
			entries, err := os.ReadDir(prev)
			must(t, err)
			for _, e := range entries {
				b, err := os.ReadFile(filepath.Join(prev, e.Name()))
				must(t, err)
				seen[e.Name()] = string(b)
			}
		}
		return orig(src, dst)
	})
	res, err := Apply(context.Background(), f.deps("2.0.0", nil))
	must(t, err)
	if !res.Updated {
		t.Fatalf("apply = %+v", res)
	}
	for _, n := range executableBases {
		if want := defaultMembers("2.0.0")[swapRank(n)].body; seen[n] != want {
			t.Fatalf("previous %s at the first rename = %q, want %q", n, seen[n], want)
		}
	}
	r, err := parseReceipt([]byte(seen["receipt.json"]))
	if err != nil || r.ReleaseTag != "v2.0.0" {
		t.Fatalf("previous receipt at the first rename = %+v, %v", r, err)
	}
}

// TestApplySwapRestoreFailureNamesInstallSh: a rename failure inside the
// swap whose restore also fails names the install.sh remedy, as a failure
// after the swap does.
func TestApplySwapRestoreFailureNamesInstallSh(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	injectRename(t, func(orig func(string, string) error, src, dst string) error {
		// Restore publishes through ".<name>.restore" on unix; on windows it renames
		// "<name>.old" back, so that is the step to fail there.
		if filepath.Base(dst) == "cascade" || strings.HasSuffix(src, ".restore") ||
			(goruntime.GOOS == "windows" && strings.HasSuffix(src, ".old")) {
			return errors.New("injected rename failure")
		}
		return orig(src, dst)
	})
	_, err := Apply(context.Background(), f.deps("2.0.0", nil))
	wantKind(t, err, cascade.KindUnavailable, "swap cascade")
	if !strings.Contains(err.Error(), "restoring the previous files failed; rerun install.sh") {
		t.Fatalf("a failed restore inside the swap does not name the remedy: %v", err)
	}
}

package update

// Purpose: pin the receipt naming convention for the package's tests to the
//   platform they model. The fixtures install and release a testPlatform
//   (linux/amd64) tree, whose executables carry bare names; receiptGOOS
//   defaults to the HOST's GOOS, which on a windows host would demand ".exe"
//   names and refuse every fixture receipt. Tests that model windows
//   (apply_windows_test.go) set receiptGOOS themselves.

import (
	"os"
	"path/filepath"
	"testing"
)

// absInstall is an absolute install dir on every host ("/abs" is relative on
// windows, which has no root without a drive).
var absInstall = filepath.Join(os.TempDir(), "abs")

func TestMain(m *testing.M) {
	receiptGOOS = testPlatform.OS
	os.Exit(m.Run())
}

// useReceiptGOOS sets the receipt naming convention for one test.
func useReceiptGOOS(t *testing.T, goos string) {
	t.Helper()
	prev := receiptGOOS
	receiptGOOS = goos
	t.Cleanup(func() { receiptGOOS = prev })
}

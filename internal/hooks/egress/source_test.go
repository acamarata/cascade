package egress

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// readSourceFile reads one file from this package's own directory,
// located from the compiled test binary rather than from the working
// directory, so the drift tests do not depend on where they are run.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(self), name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

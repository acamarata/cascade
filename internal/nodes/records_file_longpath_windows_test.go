//go:build windows

package nodes

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestFileRecordBackend_LongPathRoundTrips: a data dir pushing
// devices.json past 300 characters must still Save then Load the stored
// record. Without readWithFallback, the raw-path CreateFile reports
// not-exist on a process without long-path support and Load returns an
// empty fleet.
func TestFileRecordBackend_LongPathRoundTrips(t *testing.T) {
	dataDir := t.TempDir()
	for len(dataDir) < 300 {
		dataDir = filepath.Join(dataDir, strings.Repeat("d", 40))
	}
	path := filepath.Join(dataDir, "nodes", "devices.json")
	if len(path) < 300 {
		t.Fatalf("precondition: path is %d chars, want 300+", len(path))
	}
	if err := NewFileRecordBackend(dataDir).Save(map[string]DeviceRecord{"n1": {NodeID: "n1"}}); err != nil {
		t.Fatalf("Save at %d chars: %v", len(path), err)
	}
	got, err := NewFileRecordBackend(dataDir).Load()
	if err != nil {
		t.Fatalf("Load at %d chars: %v", len(path), err)
	}
	if _, ok := got["n1"]; !ok {
		t.Fatalf("Load at %d chars = %v, want record n1 (an empty fleet here is the de-enrollment bug)", len(path), got)
	}
}

// TestRecordFileLock_CaseFoldedOnWindows: two casings of one path name
// one file on Windows and must share one lock.
func TestRecordFileLock_CaseFoldedOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Nodes", "Devices.json")
	if recordFileLock(path) != recordFileLock(strings.ToLower(path)) {
		t.Fatal("lower- and mixed-case spellings of one path got different locks")
	}
}

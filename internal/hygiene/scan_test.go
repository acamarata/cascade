package hygiene

import "testing"

// TestScanFilesReturnsEmptyForCleanFile verifies that ScanFiles reports no
// violations and no error for a clean input file.
func TestScanFilesReturnsEmptyForCleanFile(t *testing.T) {
	violations, err := ScanFiles([]string{"clean.go"})
	if err != nil {
		t.Fatalf("ScanFiles returned unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("ScanFiles returned %d violations, want 0", len(violations))
	}
}

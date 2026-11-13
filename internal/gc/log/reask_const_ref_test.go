package log

import "testing"

// TestReaskContractConstants pins the re-ask budget and note a later slice uses.
func TestReaskContractConstants(t *testing.T) {
	if MaxReasks != 2 {
		t.Fatalf("MaxReasks = %d, want 2", MaxReasks)
	}
	if PlainProseNote == "" {
		t.Fatal("PlainProseNote is empty")
	}
}

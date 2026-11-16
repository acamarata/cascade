package supervise

import "testing"

// TestNeedsRebuild verifies that NeedsRebuild correctly determines when a rebuild is necessary
// based on tree hash comparison rather than file mtime.
func TestNeedsRebuild(t *testing.T) {
	tests := []struct {
		name        string
		currentHash TreeHash
		storedHash  TreeHash
		wantRebuild bool
	}{
		{
			name:        "different hashes require rebuild",
			currentHash: "abc123",
			storedHash:  "def456",
			wantRebuild: true,
		},
		{
			name:        "same hashes do not require rebuild",
			currentHash: "abc123",
			storedHash:  "abc123",
			wantRebuild: false,
		},
		{
			name:        "empty stored hash requires rebuild",
			currentHash: "abc123",
			storedHash:  "",
			wantRebuild: true,
		},
		{
			name:        "both empty hashes do not require rebuild",
			currentHash: "",
			storedHash:  "",
			wantRebuild: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedsRebuild(tt.currentHash, tt.storedHash)
			if got != tt.wantRebuild {
				t.Errorf("NeedsRebuild(%q, %q) = %v, want %v", tt.currentHash, tt.storedHash, got, tt.wantRebuild)
			}
		})
	}
}

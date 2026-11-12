package hj

import (
	"strings"
	"testing"
)

// TestMS5JobRecordMasksSecretInCommand verifies that RecordMS5Job masks the
// prompt value in the stored command. It uses table-driven sub-tests to cover
// normalized whitespace, a one-byte value replaced by the empty token, and
// preservation of other tokens. Snapshots must contain only masked text.
func TestMS5JobRecordMasksSecretInCommand(t *testing.T) {
	// Reset global state before and after this test to avoid interference with
	// other tests in the package (like TestMS5JobRecordRetainsMaskedPromptCommand).
	mu.Lock()
	records = nil
	nextID = 1
	mu.Unlock()

	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		records = nil
		nextID = 1
	})

	rec, err := RecordMS5Job("run --prompt secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Command != "run --prompt *****" {
		t.Fatalf("expected Command %q, got %q", "run --prompt *****", rec.Command)
	}

	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{
			name: "normalized whitespace",
			cmd:  "run  --prompt\tsecret",
			want: "run --prompt *****",
		},
		{
			name: "one-byte value replaced by empty token",
			cmd:  "run --prompt s",
			want: "run --prompt ",
		},
		{
			name: "preservation of other tokens",
			cmd:  "run --foo bar --prompt secret",
			want: "run --foo bar --prompt *****",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := RecordMS5Job(tc.cmd)
			if err != nil {
				t.Fatalf("unexpected error for cmd %q: %v", tc.cmd, err)
			}
			if r.Command != tc.want {
				t.Errorf("expected Command %q, got %q", tc.want, r.Command)
			}
			// Snapshots must contain only masked text; no raw secrets.
			if strings.Contains(r.Command, "secret") {
				t.Errorf("snapshot contains unmasked secret text: %q", r.Command)
			}
		})
	}

	// Verify snapshots via MS5Records contain only masked text.
	all := MS5Records()
	for _, snap := range all {
		if strings.Contains(snap.Command, "secret") {
			t.Errorf("snapshot record contains unmasked text: %q", snap.Command)
		}
	}
}

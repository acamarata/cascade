package consult

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRolePromptStripsFrontMatter verifies that LoadRolePrompt strips YAML-style
// front matter delimited by exactly "---" on its own lines, while preserving
// non-leading delimiters and falling back for malformed inputs.
func TestRolePromptStripsFrontMatter(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		want       string
		isFallback bool
	}{
		{
			name:    "simple front matter stripped",
			content: "---\nname: x\n---\nBody",
			want:    "Body",
		},
		{
			name:    "CRLF front matter stripped",
			content: "---\r\nname: x\r\n---\r\nBody",
			want:    "Body",
		},
		{
			name:    "nonleading delimiter stays in body",
			content: "Body\n---\nname: x\n---",
			want:    "Body\n---\nname: x\n---",
		},
		{
			name:       "unclosed header falls back",
			content:    "---\nname: x\nBody",
			isFallback: true,
		},
		{
			name:       "invalid UTF-8 falls back",
			content:    "\xff\xfe",
			isFallback: true,
		},
		{
			name:       "stripped empty body falls back",
			content:    "---\nname: x\n---\n",
			isFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "Test.md")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			got := LoadRolePrompt(dir, "Test")

			if tt.isFallback {
				wantFallback := builtinFallback("Test")
				if got != wantFallback {
					t.Errorf("LoadRolePrompt() = %q, want fallback %q", got, wantFallback)
				}
			} else {
				wantBody := strings.TrimSpace(tt.want)
				if got != wantBody {
					t.Errorf("LoadRolePrompt() = %q, want %q", got, wantBody)
				}
			}
		})
	}
}

// TestRolePromptCapsAt5000 verifies that LoadRolePrompt truncates bodies
// exceeding 5000 Unicode code points to exactly the first 5000 code points,
// preserves bodies of exactly 5000 code points, and handles multibyte
// characters without splitting them.
func TestRolePromptCapsAt5000(t *testing.T) {
	dir := t.TempDir()

	// Test: exactly 5000 code points remains unchanged
	exact5000 := strings.Repeat("a", 5000)
	pathExact := filepath.Join(dir, "Exact.md")
	if err := os.WriteFile(pathExact, []byte(exact5000), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gotExact := LoadRolePrompt(dir, "Exact")
	if gotExact != exact5000 {
		t.Errorf("LoadRolePrompt(5000) returned %d chars, want 5000", len(gotExact))
	}

	// Test: 6000 code points returns first 5000
	long6000 := strings.Repeat("b", 6000)
	pathLong := filepath.Join(dir, "Long.md")
	if err := os.WriteFile(pathLong, []byte(long6000), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gotLong := LoadRolePrompt(dir, "Long")
	if len(gotLong) != 5000 {
		t.Errorf("LoadRolePrompt(6000) returned %d chars, want 5000", len(gotLong))
	}
	if !strings.HasPrefix(gotLong, strings.Repeat("b", 5000)) {
		t.Errorf("LoadRolePrompt(6000) did not return first 5000 chars")
	}

	// Test: multibyte characters are not split
	multibyte := strings.Repeat("\u4e16", 6000) // 6000 code points, each is 3 bytes
	pathMulti := filepath.Join(dir, "Multi.md")
	if err := os.WriteFile(pathMulti, []byte(multibyte), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gotMulti := LoadRolePrompt(dir, "Multi")
	wantMulti := strings.Repeat("\u4e16", 5000) // first 5000 code points
	if gotMulti != wantMulti {
		t.Errorf("LoadRolePrompt(multibyte 6000) = %d code points, want 5000",
			utf8.RuneCountInString(gotMulti))
	}
}

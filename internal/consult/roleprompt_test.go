package consult

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRolePromptLoadsFile(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "plain",
			content:  "Be harsh.",
			expected: "Be harsh.",
		},
		{
			name:     "whitespace trim",
			content:  "  \nBe harsh.\n  ",
			expected: "Be harsh.",
		},
		{
			name:     "BOM",
			content:  "\xef\xbb\xbfBe harsh.",
			expected: "Be harsh.",
		},
		{
			name:     "CRLF",
			content:  "Be harsh.\r\n",
			expected: "Be harsh.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmpDir, "Critic.md"), []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			got := LoadRolePrompt(tmpDir, "Critic")
			if got != tt.expected {
				t.Errorf("LoadRolePrompt(%q, %q) = %q, want %q", tmpDir, "Critic", got, tt.expected)
			}
		})
	}

	// Case-sensitive paths
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "critic.md"), []byte("Be harsh."), 0644); err != nil {
		t.Fatal(err)
	}
	got := LoadRolePrompt(tmpDir, "Critic")
	if got != "Identify flaws and risks." {
		t.Errorf("LoadRolePrompt case sensitivity: got %q, want fallback", got)
	}
}

func TestRolePromptMissingFallsBack(t *testing.T) {
	dir := t.TempDir()

	// Built-in fallbacks for missing file
	cases := []struct {
		role     string
		expected string
	}{
		{"Critic", "Identify flaws and risks."},
		{"Judge", "Evaluate the evidence and give a verdict."},
		{"Unknown", "Provide a concise, accurate response."},
		{"", "Provide a concise, accurate response."},
	}
	for _, tc := range cases {
		got := LoadRolePrompt(dir, tc.role)
		if got != tc.expected {
			t.Errorf("Missing file role %q: got %q, want %q", tc.role, got, tc.expected)
		}
	}

	// Empty dir falls back
	got := LoadRolePrompt("", "Critic")
	if got != "Identify flaws and risks." {
		t.Errorf("Empty dir: got %q, want fallback", got)
	}

	// Invalid roles: . and ..
	got = LoadRolePrompt(dir, ".")
	if got != "Provide a concise, accurate response." {
		t.Errorf("Role '.': got %q, want fallback", got)
	}
	got = LoadRolePrompt(dir, "..")
	if got != "Provide a concise, accurate response." {
		t.Errorf("Role '..': got %q, want fallback", got)
	}

	// Path separators
	got = LoadRolePrompt(dir, "sub/Critic")
	if got != "Provide a concise, accurate response." {
		t.Errorf("Role with slash: got %q, want fallback", got)
	}
	got = LoadRolePrompt(dir, "sub\\Critic")
	if got != "Provide a concise, accurate response." {
		t.Errorf("Role with backslash: got %q, want fallback", got)
	}

	// Non-regular file (directory instead of file)
	dirRole := filepath.Join(dir, "DirRole")
	if err := os.Mkdir(dirRole+".md", 0755); err != nil {
		t.Fatal(err)
	}
	got = LoadRolePrompt(dir, "DirRole")
	if got != "Provide a concise, accurate response." {
		t.Errorf("Non-regular file: got %q, want fallback", got)
	}

	// Empty body
	if err := os.WriteFile(filepath.Join(dir, "Empty.md"), []byte("   \n  "), 0644); err != nil {
		t.Fatal(err)
	}
	got = LoadRolePrompt(dir, "Empty")
	if got != "Provide a concise, accurate response." {
		t.Errorf("Empty body: got %q, want fallback", got)
	}

	// Symlink escape
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "Escape.md"), []byte("Escaped"), 0644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "Escape.md")
	if err := os.Symlink(filepath.Join(outsideDir, "Escape.md"), symlink); err != nil {
		t.Fatal(err)
	}
	got = LoadRolePrompt(dir, "Escape")
	if got != "Provide a concise, accurate response." {
		t.Errorf("Symlink escape: got %q, want fallback", got)
	}
}

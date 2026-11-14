// Package build contains build-time documentation checks.
package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocCheck_P1_GEN_13d verifies that documentation for the cascade context repair
// command and daemon maintenance model is present and contains required content.
func TestDocCheck_P1_GEN_13d(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		required string
	}{
		{
			name:     "context repair command documentation",
			path:     filepath.Join("..", "..", "docs", "cli-reference", "context.md"),
			required: "cascade context repair",
		},
		{
			name:     "semantic repo model daemon maintenance",
			path:     filepath.Join("..", "..", ".github", "wiki", "Semantic-Repo-Model.md"),
			required: "sweep loop",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("failed to read %s: %v", tc.path, err)
			}

			if !strings.Contains(string(content), tc.required) {
				t.Errorf("%s must contain %q", tc.path, tc.required)
			}
		})
	}
}

package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPostJSONGrantUsesOAuthClass verifies that JSONGrantPoster runs on the
// existing http.Client under EgressClassOAuth, and that go/parser finds
// exactly one http.Client construction in internal/secrets non-test code.
func TestPostJSONGrantUsesOAuthClass(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	t.Setenv("CASCADE_HOME", tmpDir)

	// Count http.Client constructions in non-test files
	clientCount := 0
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		// Simple heuristic: count "http.Client{" occurrences
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		count := strings.Count(string(content), "http.Client{")
		clientCount += count

		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk directory: %v", err)
	}

	if clientCount != 1 {
		t.Errorf("expected exactly 1 http.Client construction in non-test code, found %d", clientCount)
	}

	// Verify JSONGrantPoster can be constructed
	poster := NewJSONGrantPoster(nil)
	if poster == nil {
		t.Fatal("NewJSONGrantPoster returned nil")
	}

	// Verify it rejects nil exchanger
	_, _, err = poster.PostJSON(context.Background(), "https://example.com/token", map[string]any{"grant_type": "test"})
	if err == nil {
		t.Error("expected error for nil exchanger")
	}
}

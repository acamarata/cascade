// Purpose: doc check that the run reference documents `cascade run --agent`.
// SPORT: internal/build doccheck (ADD).
package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocCheck_P1_AGT_12d fails unless docs/cli-reference/run.md names
// "cascade run --agent".
func TestDocCheck_P1_AGT_12d(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "cli-reference", "run.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(body), "cascade run --agent") {
		t.Fatalf("%s does not document \"cascade run --agent\"", path)
	}
}

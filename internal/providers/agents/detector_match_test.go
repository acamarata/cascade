// Purpose: tests that the pre-spawn scan's matcher is the process detector:
// credential-shaped samples the four removed regexes missed are found, a
// plain file is not, and the matcher plugs into provider.PreSpawnScan.
// Constraints: every sample is assembled at runtime from split literals so no
// contiguous credential-shaped string sits in this source file.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-15.

package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestPreSpawnScanUsesProcessDetector(t *testing.T) {
	isolateHome(t)
	match, err := ProcessDetectorMatch()
	if err != nil {
		t.Fatalf("ProcessDetectorMatch: %v", err)
	}
	body := strings.Repeat("aZ3kQ9", 8)
	samples := map[string]string{
		"anthropic key":  "ANTHROPIC_API_KEY=sk-" + "ant-api03-" + body,
		"openai project": "OPENAI_API_KEY=sk-" + "proj-" + body,
		"openai key":     "OPENAI_API_KEY=sk-" + body,
		"github pat":     "token: github_" + "pat_" + body + "_" + body,
	}
	for name, content := range samples {
		t.Run(name, func(t *testing.T) {
			if class, hit := match([]byte(content)); !hit || class == "" {
				t.Fatalf("matcher missed %q (class %q, hit %v)", name, class, hit)
			}
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "leak.env"), []byte(content+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := provider.PreSpawnScan(root, provider.WorktreeScope{}, match); err != provider.ErrPreSpawnSecretFound {
				t.Fatalf("PreSpawnScan = %v, want ErrPreSpawnSecretFound by identity", err)
			}
		})
	}
	if class, hit := match([]byte("package main\n\nfunc main() {}\n")); hit {
		t.Fatalf("matcher flagged plain source (class %q)", class)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.PreSpawnScan(root, provider.WorktreeScope{}, match); err != nil {
		t.Fatalf("PreSpawnScan(clean tree) = %v, want nil", err)
	}
}

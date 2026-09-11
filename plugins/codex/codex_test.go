package codex

import (
	"os"
	"strings"
	"testing"
)

// TestCascadeCodexAGENTSMdFixture is the Art.2 real-counterpart replay: it
// asserts the golden this plugin installs (testdata/agents-md-fixture/
// input.AGENTS.md) appears, verbatim and unmodified, inside a real Codex
// CLI parse capture (testdata/agents-md-fixture/prompt-input.capture.txt).
// Both files and their provenance are documented in testdata/README.md.
// No network, no subprocess: this is a pure byte-containment check over
// two committed fixtures (Art.7 no-network unit lane).
func TestCascadeCodexAGENTSMdFixture(t *testing.T) {
	golden, err := os.ReadFile("testdata/agents-md-fixture/input.AGENTS.md")
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	capture, err := os.ReadFile("testdata/agents-md-fixture/prompt-input.capture.txt")
	if err != nil {
		t.Fatalf("read capture fixture: %v", err)
	}

	if !strings.Contains(string(capture), strings.TrimRight(string(golden), "\n")) {
		t.Fatalf("real codex debug prompt-input capture does not contain the golden's exact content;\ngolden:\n%s\ncapture:\n%s", golden, capture)
	}
	if !strings.Contains(string(capture), "<INSTRUCTIONS>") || !strings.Contains(string(capture), "</INSTRUCTIONS>") {
		t.Fatal("capture fixture missing the real Codex CLI's <INSTRUCTIONS> wrapper")
	}
}

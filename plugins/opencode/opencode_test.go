package opencode

import (
	"os"
	"strings"
	"testing"
)

// TestCascadeOpencodeInstructionFixture is the Art.2 real-counterpart
// replay: it asserts the real, installed OpenCode CLI binary's own
// embedded strings (testdata/instruction-fixture/instructions.capture.txt,
// extracted via `strings` per testdata/README.md's provenance stamp)
// document AGENTS.md as the file name OpenCode reads, in both the forms
// captured: the system-prompt instruction text and the config-schema
// example. This proves the golden this plugin installs
// (testdata/instruction-fixture/input.AGENTS.md) targets a filename the
// real CLI is documented, by its own shipped text, to read - never a
// self-authored dialect. No network, no subprocess (Art.7).
func TestCascadeOpencodeInstructionFixture(t *testing.T) {
	golden, err := os.ReadFile("testdata/instruction-fixture/input.AGENTS.md")
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	capture, err := os.ReadFile("testdata/instruction-fixture/instructions.capture.txt")
	if err != nil {
		t.Fatalf("read capture fixture: %v", err)
	}

	if !strings.Contains(string(golden), "cascade:generate-instructions") {
		t.Fatalf("golden fixture missing the cascade managed-block marker")
	}

	wantSubstrings := []string{
		"Markdown files named `AGENTS.md`",
		`"instructions": ["AGENTS.md"`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(string(capture), want) {
			t.Errorf("real opencode binary capture missing expected substring %q", want)
		}
	}
}

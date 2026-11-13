package hygiene

import (
	"testing"
)

// fixtureBase pins the scratch root so the tests never depend on the host temp dir.
const fixtureBase = "/scratch-fixture"

func init() { scratchBase = fixtureBase }

func TestScratchPathBuildsExpectedDir(t *testing.T) {
	path, err := ScratchPath("gate", "2026-11-05")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/scratch-fixture/gate-20261105/" {
		t.Errorf("expected /scratch-fixture/gate-20261105/, got %s", path)
	}
}

func TestScratchPathRejectsBadTopic(t *testing.T) {
	_, err := ScratchPath("../x", "2026-11-05")
	if err == nil {
		t.Error("expected error for bad topic '../x', got nil")
	}
	_, err = ScratchPath("", "2026-11-05")
	if err == nil {
		t.Error("expected error for empty topic, got nil")
	}
}

func TestValidateScratchAcceptsUnderTopicDir(t *testing.T) {
	err := ValidateScratch("/scratch-fixture/gate-20261105/a.txt")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateScratchRejectsOutsidePaths(t *testing.T) {
	paths := []string{
		"/tmp/a",
		"/scratch-fixture/a.txt",
		"/scratch-fixture/gate-20261105/../x",
	}
	for _, p := range paths {
		err := ValidateScratch(p)
		if err == nil {
			t.Errorf("expected error for %q, got nil", p)
		}
	}
}

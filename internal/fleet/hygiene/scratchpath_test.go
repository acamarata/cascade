package hygiene

import (
	"testing"
)

func TestScratchPathBuildsExpectedDir(t *testing.T) {
	path, err := ScratchPath("gate", "2026-11-05")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/Volumes/D5/tmp/gate-20261105/" {
		t.Errorf("expected /Volumes/D5/tmp/gate-20261105/, got %s", path)
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
	err := ValidateScratch("/Volumes/D5/tmp/gate-20261105/a.txt")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateScratchRejectsOutsidePaths(t *testing.T) {
	paths := []string{
		"/tmp/a",
		"/Volumes/D5/tmp/a.txt",
		"/Volumes/D5/tmp/gate-20261105/../x",
	}
	for _, p := range paths {
		err := ValidateScratch(p)
		if err == nil {
			t.Errorf("expected error for %q, got nil", p)
		}
	}
}

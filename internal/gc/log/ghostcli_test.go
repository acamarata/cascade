package log

import (
	"bytes"
	"testing"
)

func TestGhostCLIPrintsSupportMessage(t *testing.T) {
	var buf bytes.Buffer
	exitCode := GhostCLI([]string{}, &buf)
	got := buf.String()
	if got != GhostSupportMessage {
		t.Errorf("expected %q, got %q", GhostSupportMessage, got)
	}
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestGhostCLIExitZero(t *testing.T) {
	var buf bytes.Buffer
	exitCode := GhostCLI([]string{}, &buf)
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestGhostCLIUnknownSubcommandUsage(t *testing.T) {
	var buf bytes.Buffer
	exitCode := GhostCLI([]string{"bogus"}, &buf)
	if exitCode != 2 {
		t.Errorf("expected exit code 2, got %d", exitCode)
	}
	got := buf.String()
	if got != GhostUsage {
		t.Errorf("expected %q, got %q", GhostUsage, got)
	}
}

package log

import (
	"bytes"
	"testing"
)

func TestGatewayCLIPrintsSupportMessage(t *testing.T) {
	var buf bytes.Buffer
	exitCode := GatewayCLI([]string{}, &buf)
	got := buf.String()
	if got != GatewaySupportMessage {
		t.Errorf("expected %q, got %q", GatewaySupportMessage, got)
	}
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestGatewayCLIExitZero(t *testing.T) {
	var buf bytes.Buffer
	exitCode := GatewayCLI([]string{}, &buf)
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestGatewayCLIUnknownSubcommandUsage(t *testing.T) {
	var buf bytes.Buffer
	exitCode := GatewayCLI([]string{"bogus"}, &buf)
	if exitCode != 2 {
		t.Errorf("expected exit code 2, got %d", exitCode)
	}
	got := buf.String()
	if got != GatewayUsage {
		t.Errorf("expected %q, got %q", GatewayUsage, got)
	}
}

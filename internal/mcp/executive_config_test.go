package mcp

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestExecutiveMCPConfigIsCompactWrite(t *testing.T) {
	got, err := ExecutiveMCPConfigJSON("/opt/cascade/bin/cascade")
	want := `{"mcpServers":{"cascade":{"command":"/opt/cascade/bin/cascade","args":["mcp","serve","--stdio","--profile","compact-write"]}}}`
	if err != nil || string(got) != want {
		t.Fatalf("config = %s, error = %v; want %s", got, err, want)
	}
	for _, bin := range []string{"cascade", "./cascade", "../bin/cascade", ""} {
		got, err := ExecutiveMCPConfigJSON(bin)
		if got != nil || err != cascade.ErrInvalidInput || err.Error() != "invalid-input" {
			t.Fatalf("bin %q: config = %s, error = %v; want invalid input sentinel by identity and message", bin, got, err)
		}
	}
}

func TestExecutiveMCPConfigEscapesBinary(t *testing.T) {
	got, err := ExecutiveMCPConfigJSON("/opt/a\"b\\c/cascade")
	want := `{"mcpServers":{"cascade":{"command":"/opt/a\"b\\c/cascade","args":["mcp","serve","--stdio","--profile","compact-write"]}}}`
	if err != nil || string(got) != want {
		t.Fatalf("config = %s, error = %v; want %s", got, err, want)
	}
}

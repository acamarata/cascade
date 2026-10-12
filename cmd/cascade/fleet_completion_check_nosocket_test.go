// Purpose: prove that an absent completion socket refuses with the dial reason.
// Inputs: an absent socket under a temporary directory and empty hook input.
// Outputs: an exit-2 refusal naming the unreachable socket, never a deadline.
// Constraints: no listener, build tags, or test-side deadline.
// SPORT: cmd/cascade/fleet-completion-check.
package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCompletionCheckNoSocketFailsClosed(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "absent.sock")
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader(`{}`))
	deps := fleetSessionsDeps{
		DialContext: client.UnixDialer,
		Getenv:      func(string) string { return "" },
	}
	err := runCompletionCheck(cmd, deps, "Stop", socket)
	if err == nil {
		t.Fatal("missing socket allowed completion")
	}
	if code := cascade.ExitCode(err); code != 2 {
		t.Errorf("exit code = %d, want 2: %v", code, err)
	}
	for _, want := range []string{"daemon not running or unreachable at", socket, "unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want %q", err.Error(), want)
		}
	}
	for _, unwanted := range []string{"timed out", "timeout", "deadline"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("refusal = %q, must name the dial failure, not %q", err.Error(), unwanted)
		}
	}
}

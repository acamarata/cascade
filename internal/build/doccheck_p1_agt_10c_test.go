// Purpose: doc check that the delegation guide documents the agent approval
// bridge: its capability, action and the redeemed-token answer path.
// SPORT: internal/build doccheck (ADD).

package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocCheck_P1_AGT_10c fails unless docs/delegation.md names the bridge's
// capability "agents.tool_approval", its action "agent.approval" and the
// ResolveApproval and ConsumeToken answer path.
func TestDocCheck_P1_AGT_10c(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "delegation.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, want := range []string{
		"agents.tool_approval",
		"`agent.approval`",
		"ResolveApproval(id, token)",
		"ConsumeToken",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("%s does not document %q", path, want)
		}
	}
}

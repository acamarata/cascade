package agents

// Purpose: the claude row's gateway variables are the claude driver's own
//   constants, and the managed flag value matches the driver's.
// SPORT: internal.providers.agents/ADD (P1-GWY).

import (
	"testing"

	"github.com/acamarata/cascade/providers/agents/claude"
)

func TestClaudeRowGatewayUsesDriverConstants(t *testing.T) {
	want := CredentialSpec{Class: CredentialGateway, KeyVar: claude.KeyVarGatewayToken,
		BaseURLVar: claude.GatewayBaseURLVar, ManagedVar: claude.GatewayManagedVar}
	if claudeRow.Gateway != want {
		t.Fatalf("claudeRow.Gateway = %+v, want %+v", claudeRow.Gateway, want)
	}
	if claude.GatewayManagedValue != gatewayManagedValue {
		t.Fatalf("managed value %q differs from the shim's %q", claude.GatewayManagedValue, gatewayManagedValue)
	}
}

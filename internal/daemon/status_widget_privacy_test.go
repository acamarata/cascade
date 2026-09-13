package daemon

// Purpose (this file): the R-21.162/R-21.200 privacy proofs (task 9):
// TestStatusWidgetNoPII drives a real status.widget dispatch over a
// fixture whose underlying provider/node sources deliberately carry all
// five PII pattern families and asserts none reach the serialised
// response. TestStatusWidgetLabelRedaction proves show_project_names
// threads correctly from StatusWidgetDeps into capacity.Redact (Compose
// itself never populates Projects today — widget.go's own DISCLOSED GAP
// note — so this exercises redactSnapshot directly against a hand-built
// WidgetSnapshot, the same way TestWidgetRedactDirect in
// internal/fleet/capacity does for Redact itself).
//
// TestStatusWidgetPeerUIDRefused (the third proof this ticket's checks
// list names) lives in the sibling
// status_widget_peeruid_integration_test.go, NOT here — see that file's
// header for why: internal/build's real, mechanical
// TestNoNetworkUnitTest_RealTreeGreen gate (Art.7.2) forbids any
// untagged _test.go file from importing "net"/"net/http" at all, and
// proving a peer-UID rejection needs a real net.Conn.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
)

// TestStatusWidgetNoPII seeds deps' real sources with fixture records
// whose refs deliberately carry all five R-21.162 PII pattern families
// (email, absolute path, hostname), dispatches a real status.widget
// request, and asserts the serialised response matches none of them —
// Compose/Redact strip them before this daemon ever writes a byte to the
// wire.
func TestStatusWidgetNoPII(t *testing.T) {
	reg, deps := setupStatusWidget(t, nil)
	deps.providerSrc = &fakeWidgetProviderSource{
		providers: []registry.ProviderRecord{{Name: "user@example.com"}},
	}
	deps.nodeSrc = &fakeWidgetNodeSource{devices: []nodes.DeviceRecord{
		{NodeID: "/Users/alice/.cascade/node1", Presence: nodes.PresenceReachable},
		{NodeID: "host.example.local", Presence: nodes.PresenceReachable},
	}}

	req := &rpc.Request{JSONRPC: "2.0", Method: MethodStatusWidget, ID: json.RawMessage(`1`)}
	result, errObj := reg.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	body := string(raw)

	for _, forbidden := range []string{
		"user@example.com",
		"/Users/alice/.cascade/node1",
		"host.example.local",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("serialised status.widget response leaked PII %q:\n%s", forbidden, body)
		}
	}
}

// fakeWidgetProviderSource is a controllable capacity.ProviderSource,
// mirroring fakeWidgetNodeSource's own rationale (status_widget_test.go):
// swapping deps.providerSrc for this fake lets a test drive a fixture
// through the real Compositor without a live providers.db (this ticket's
// own DISCLOSED GAP: no production providerSrc exists yet either).
type fakeWidgetProviderSource struct {
	providers []registry.ProviderRecord
	lanes     []registry.LaneRecord
}

func (f *fakeWidgetProviderSource) ListProviders(context.Context) ([]registry.ProviderRecord, error) {
	return f.providers, nil
}

func (f *fakeWidgetProviderSource) ListLanes(context.Context) ([]registry.LaneRecord, error) {
	return f.lanes, nil
}

// TestStatusWidgetLabelRedaction proves the show_project_names flag
// threads from StatusWidgetDeps into capacity.Redact — see this file's
// header for why it drives redactSnapshot directly rather than through a
// live (today nonexistent) Projects source.
func TestStatusWidgetLabelRedaction(t *testing.T) {
	snap := capacity.WidgetSnapshot{Projects: []capacity.ProjectRow{{Ref: "p1", Label: "Real Project Name"}}}

	hidden := redactSnapshot(&StatusWidgetDeps{showProjectNames: func() bool { return false }}, snap)
	if hidden.Projects[0].Label != "Project 1" {
		t.Errorf("hidden label = %q, want \"Project 1\"", hidden.Projects[0].Label)
	}

	shown := redactSnapshot(&StatusWidgetDeps{showProjectNames: func() bool { return true }}, snap)
	if shown.Projects[0].Label != "Real Project Name" {
		t.Errorf("shown label = %q, want the real label", shown.Projects[0].Label)
	}

	// A nil showProjectNames accessor (never wired) must still default
	// false, never panic.
	def := redactSnapshot(&StatusWidgetDeps{}, snap)
	if def.Projects[0].Label != "Project 1" {
		t.Errorf("default label = %q, want \"Project 1\" (nil accessor defaults false)", def.Projects[0].Label)
	}
}

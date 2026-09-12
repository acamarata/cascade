// Purpose: `cascade fleet top` / hidden `cascade top` alias CLI-shape
//
//	tests (P1-E18-W4-S40-T1) — mirrors fleet_attention_test.go's and
//	fleet_test.go's own established patterns exactly (identical-
//	construction alias proof, hidden-from-help proof, GOOS-gated Windows
//	refusal, actionable-error proof, sessionListerAdapter's row mapping).
//
// SPORT: cmd/cascade/fleet-top-cli (ADD, P1-E18-W4-S40-T1).
package main

import (
	"context"
	"encoding/json"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeRPCCaller implements sessions.RPCCaller by JSON round-tripping a
// fixed {"sessions": result} payload into whatever *listResult-shaped
// pointer Do is called with — the same generic technique any RPCCaller
// fake needs since listResult itself is unexported to this package.
type fakeRPCCaller struct {
	result []sessions.SessionRecord
	err    error
}

func (f fakeRPCCaller) Do(_ context.Context, _ string, _, out any) error {
	if f.err != nil {
		return f.err
	}
	payload, err := json.Marshal(map[string]any{"sessions": f.result})
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, out)
}

func mustParseTime(t *testing.T, unixSeconds int64) time.Time {
	t.Helper()
	return time.Unix(unixSeconds, 0)
}

// TestFleetTopHiddenAlias proves the top-level `top` alias is hidden
// from --help while still resolving, per R-14.92. Required by this
// ticket's own `checks` list (-run TestFleetTopHiddenAlias).
func TestFleetTopHiddenAlias(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"top"})
	if err != nil {
		t.Fatalf("top alias not found: %v", err)
	}
	if !found.Hidden {
		t.Fatal("cascade top alias must be Hidden (07 §fleet consolidation note, R-14.92)")
	}
	if strings.Contains(root.UsageString(), "\n  top ") {
		t.Fatal("cascade top must not appear in --help usage")
	}
}

// TestFleetTopMountedUnderFleet proves `cascade fleet top` resolves.
func TestFleetTopMountedUnderFleet(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	if _, _, err := root.Find([]string{"fleet", "top"}); err != nil {
		t.Fatalf("fleet top not found: %v", err)
	}
}

// TestFleetTopAlias_IdenticalConstruction mirrors
// TestFleetAttentionAlias_IdenticalConstruction's exact pattern: the
// hidden alias and the canonical command share one constructor.
func TestFleetTopAlias_IdenticalConstruction(t *testing.T) {
	deps := fleetSessionsDeps{}
	canonical := newFleetTopCmd(deps)
	alias := newFleetTopCmd(deps)
	alias.Use = "top"
	alias.Hidden = true

	if canonical.Use != "top" || alias.Use != "top" {
		t.Fatalf("canonical.Use=%q alias.Use=%q, want both %q", canonical.Use, alias.Use, "top")
	}
	onceFlag, err := alias.Flags().GetBool("once")
	if err != nil {
		t.Fatalf("alias missing --once flag: %v", err)
	}
	if onceFlag {
		t.Fatal("--once must default to false")
	}
}

// TestFleetTopWindowsTier2Refusal proves `cascade fleet top` refuses on
// Windows before ever dialing. GOOS-gated: self-skips off-Windows,
// mirroring TestFleetSessionsWatch_WindowsTier2Refusal's own precedent.
func TestFleetTopWindowsTier2Refusal(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("this refusal is GOOS-gated (fleet_top.go); only Windows CI actually exercises it")
	}
	cmd := newFleetTopCmd(fleetSessionsDeps{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "Windows tier-2") {
		t.Fatalf("fleet top on windows = %v, want a Windows tier-2 refusal", err)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("error kind = %v, want unsupported", err)
	}
}

// TestFleetTopErrors_AreActionable proves both refusals carry a concrete
// next step, never a bare "failed" message.
func TestFleetTopErrors_AreActionable(t *testing.T) {
	if !strings.Contains(errFleetTopNoDaemon.Error(), "cascade daemon run") {
		t.Errorf("errFleetTopNoDaemon does not suggest starting the daemon: %v", errFleetTopNoDaemon)
	}
	if !strings.Contains(errFleetTopWindowsTier2.Error(), "Windows tier-2") {
		t.Errorf("errFleetTopWindowsTier2 not descriptive: %v", errFleetTopWindowsTier2)
	}
}

// TestSessionListerAdapter_MapsRows proves the daemon-record-to-view-row
// mapping used by both --once and the interactive path.
func TestSessionListerAdapter_MapsRows(t *testing.T) {
	adapter := sessionListerAdapter{
		inner: sessions.NewClient(fakeRPCCaller{
			result: []sessions.SessionRecord{
				{SessionID: "s1", Harness: "claude", Account: "a1", State: "running", UpdatedAt: 100},
			},
		}),
		clk: runtime.NewFixedClock(mustParseTime(t, 200)),
	}
	rows, err := adapter.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "s1" || rows[0].Harness != "claude" || rows[0].State != "running" {
		t.Fatalf("rows = %+v", rows)
	}
}

// TestTopSnapshotResult_JSONAndString proves both output.Writer.Result
// paths (JSON via MarshalJSON, human via String) produce real content,
// never an empty placeholder.
func TestTopSnapshotResult_JSONAndString(t *testing.T) {
	r := topSnapshotResult{}
	data, err := r.MarshalJSON()
	if err != nil || len(data) == 0 {
		t.Fatalf("MarshalJSON: data=%q err=%v", data, err)
	}
	if !strings.Contains(r.String(), "sessions=0") {
		t.Fatalf("String() = %q, missing summary line", r.String())
	}
}

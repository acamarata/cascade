package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/plugin"

	"github.com/acamarata/cascade/internal/backup"

	_ "github.com/acamarata/cascade/plugins/pbd"
)

// Purpose (this file): replay the frames a REAL MCP client actually sent,
//   and assert this server answers them the way it answered on the day the
//   client accepted it.
//
// These goldens are Art.2 evidence, not a shape the contract described.
//   They were captured with `cascade mcp serve --stdio --capture` from the
//   installed first-party client driving the built binary (named, with its
//   version, in testdata/README.md); the session that produced them ended
//   with the client listing cascade's four tools and invoking one. The
//   failing BASELINE from before the fix is kept beside them, so the
//   regression this ticket closed stays legible.
//
// Constraints: no network — the frames are replayed against the in-process
//   server, exactly as they came off the wire.
// SPORT: internal/mcp goldens (ADD) — P1-E04-W4-S86-T1.

// goldenDir holds the captured exchange.
const goldenDir = "testdata/goldens/claude-code/2.1.273"

// readFrames reads one captured jsonl file as raw lines.
func readFrames(t *testing.T, name string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, []byte(line))
		}
	}
	return out
}

// TestTheRealClientsSessionReplays drives every captured request through
// the server and compares against the captured responses, in order, with
// the replay harness every capture shares (replay_harness_test.go).
//
// serverInfo.version is the one field NOT compared literally: it is the
// build stamp, so a golden that froze it would fail on every release
// build. CompareFrames asserts it equals buildinfo.Version instead, which
// is stricter than skipping it: a wrong value still fails.
func TestTheRealClientsSessionReplays(t *testing.T) {
	in, out, _, err := mcp.ReadCapture(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(in) == 0 || len(out) == 0 {
		t.Fatal("the capture is empty; the goldens were never recorded")
	}
	got, err := mcp.Replay(context.Background(), mcp.NewServer(capturedRegistry()), in)
	if err != nil {
		t.Fatal(err)
	}
	if err := mcp.CompareFrames(out, got); err != nil {
		t.Errorf("the answer changed since the real client accepted it: %v", err)
	}
}

// capturedRegistry rebuilds the exact tool set the capture session had:
// the pbd builtin plugin (blank-imported above) plus the two first-party
// backup CORE registrations the composition root adds. The handlers are
// stubs because the golden exercises tools/list and one pbd call; the
// DESCRIPTORS are the production ones, which is what the capture recorded.
func capturedRegistry() *mcp.ToolRegistry {
	return mcp.NewToolRegistry(shippedBuiltins, mcp.AllowAllFilter{},
		backup.MCPRegistration(func(context.Context) ([]backup.SnapshotSummary, error) { return nil, nil }),
		backup.VerifyMCPRegistration(func(context.Context, string) (backup.VerificationReport, error) {
			return backup.VerificationReport{}, nil
		}))
}

// shippedBuiltins is plugin.Builtins minus the example plugin.
//
// server_test.go blank-imports plugins/examples/example-builtin, so its
// greet-tool is registered in THIS test binary — but the shipped binary
// never imports it, and the capture proves it: the real client saw four
// tools, not five. Filtering it here keeps the golden a record of the
// product rather than of the test binary.
func shippedBuiltins() []plugin.BuiltinRegistration {
	all := plugin.Builtins()
	out := make([]plugin.BuiltinRegistration, 0, len(all))
	for _, reg := range all {
		if reg.Manifest.ID == "example-builtin" {
			continue
		}
		out = append(out, reg)
	}
	return out
}

// TestTheBaselineFailureIsStillRecorded keeps the regression legible. The
// baseline is what the previous wire answered the real client's FIRST
// frame with: -32600, which is why no client could ever connect. If this
// file is ever lost, the reason the protocol changed goes with it.
func TestTheBaselineFailureIsStillRecorded(t *testing.T) {
	baselineIn := readFrames(t, "baseline-in.jsonl")
	baselineOut := readFrames(t, "baseline-out.jsonl")
	if len(baselineIn) != 1 || len(baselineOut) != 1 {
		t.Fatal("the baseline capture is missing; the regression record is gone")
	}
	if !strings.Contains(string(baselineOut[0]), "-32600") {
		t.Errorf("the baseline no longer records the rejection: %s", baselineOut[0])
	}
	// And the very same frame must now succeed.
	frame, perr := mcp.ParseFrame(baselineIn[0])
	if perr != nil {
		t.Fatalf("the baseline frame no longer parses: %v", perr)
	}
	if resp := mcp.NewServer(emptyRegistry()).Dispatch(context.Background(), frame); resp.Error != nil {
		t.Fatalf("the frame that was rejected is STILL rejected: %v", resp.Error)
	}
}

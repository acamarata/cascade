package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/buildinfo"
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
// the server and compares against the captured responses, in order.
//
// serverInfo.version is the one field NOT compared literally: it is the
// build stamp, so a golden that froze it would fail on every release
// build. It is asserted to equal buildinfo.Version instead, which is
// stricter than skipping it — a wrong value still fails.
func TestTheRealClientsSessionReplays(t *testing.T) {
	requests := readFrames(t, "in.jsonl")
	responses := readFrames(t, "out.jsonl")
	if len(requests) == 0 || len(responses) == 0 {
		t.Fatal("the capture is empty; the goldens were never recorded")
	}

	server := mcp.NewServer(capturedRegistry())
	next := 0
	for _, line := range requests {
		frame, perr := mcp.ParseFrame(line)
		if perr != nil {
			t.Fatalf("a captured client frame no longer parses: %v\n %s", perr, line)
		}
		got := server.Dispatch(context.Background(), frame)
		if got == nil {
			// A notification. The capture must contain no response for it,
			// which is exactly what "in order" checks: the next recorded
			// response belongs to the next request that had an id.
			continue
		}
		if next >= len(responses) {
			t.Fatalf("the server answered %q but the capture has no response left for it", frame.Method)
		}
		assertSameFrame(t, frame.Method, responses[next], got)
		next++
	}
	if next != len(responses) {
		t.Errorf("replayed %d responses, the capture holds %d", next, len(responses))
	}
}

// assertSameFrame compares one response against its captured counterpart.
func assertSameFrame(t *testing.T, method string, want []byte, got *mcp.Response) {
	t.Helper()
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantAny, gotAny := decodeAny(t, want), decodeAny(t, gotBytes)
	normalizeServerVersion(t, wantAny)
	normalizeServerVersion(t, gotAny)

	wantJSON, _ := json.Marshal(wantAny)
	gotJSON, _ := json.Marshal(gotAny)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("%s: the answer changed since the real client accepted it\n captured: %s\n now:      %s",
			method, wantJSON, gotJSON)
	}
}

// decodeAny parses a frame into a generic value for comparison.
func decodeAny(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("frame is not JSON: %v\n %s", err, raw)
	}
	return out
}

// normalizeServerVersion replaces serverInfo.version with a marker, after
// asserting it is the build version. Nothing else is normalized.
func normalizeServerVersion(t *testing.T, frame map[string]any) {
	t.Helper()
	result, _ := frame["result"].(map[string]any)
	if result == nil {
		return
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info == nil {
		return
	}
	if v, ok := info["version"].(string); ok && v != buildinfo.Version {
		t.Errorf("serverInfo.version = %q, want the build version %q", v, buildinfo.Version)
	}
	info["version"] = "<build>"
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

package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// goldensRoot is the capture tree every replay walks.
const goldensRoot = "testdata/goldens"

// updateGoldens regenerates each replayable out.jsonl with Replay over its
// unchanged in.jsonl. It is how a server-output change is re-captured.
var updateGoldens = flag.Bool("update-goldens", false, "regenerate out.jsonl from in.jsonl")

// buildCaptured is the replay's server builder: the production Server and
// Dispatch over the registry the capture session had. No capture names a
// profile yet, so one that does cannot be honoured and fails.
func buildCaptured(meta mcp.CaptureMeta) (*mcp.Server, error) {
	if meta.Profile != "" {
		return nil, fmt.Errorf("no registry for profile %q", meta.Profile)
	}
	return mcp.NewServer(capturedRegistry()), nil
}

// TestMCPGoldensReplayAllCaptures replays every capture directory and fails
// if any frame diverges or a capture lacks capture.json. The context bounds
// a stuck handler.
func TestMCPGoldensReplayAllCaptures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if *updateGoldens {
		regenerate(ctx, t)
	}
	if !slices.Contains(mcp.SupportedProtocolVersions, mcp.MCPProtocolVersion) {
		t.Errorf("SupportedProtocolVersions = %v lacks %q", mcp.SupportedProtocolVersions, mcp.MCPProtocolVersion)
	}
	if err := mcp.ReplayAll(ctx, goldensRoot, buildCaptured); err != nil {
		t.Fatal(err)
	}
}

// writeFile writes content to path, creating its directory.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// regenerate rewrites each replayable capture's out.jsonl from Replay over
// its unchanged in.jsonl. Daemon captures are left alone.
func regenerate(ctx context.Context, t *testing.T) {
	t.Helper()
	inputs, err := filepath.Glob(filepath.Join(goldensRoot, "*", "*", "in.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range inputs {
		dir := filepath.Dir(input)
		in, _, meta, err := mcp.ReadCapture(dir)
		if err != nil {
			t.Fatal(err)
		}
		if meta.Needs != "" {
			continue
		}
		server, err := buildCaptured(meta)
		if err != nil {
			t.Fatal(err)
		}
		got, err := mcp.Replay(ctx, server, in)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "out.jsonl"), string(bytes.Join(got, []byte("\n")))+"\n")
	}
}

// tinyRoot lays out one capture under a temp root: a single unknown method,
// which needs no tools to answer.
func tinyRoot(t *testing.T, readmeRow, captureJSON, out string) (root string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "goldens")
	dir := filepath.Join(root, "client", "1.0")
	writeFile(t, filepath.Join(base, "README.md"), readmeRow)
	writeFile(t, filepath.Join(dir, "in.jsonl"), `{"jsonrpc":"2.0","id":1,"method":"nope"}`+"\n")
	writeFile(t, filepath.Join(dir, "out.jsonl"), out)
	if captureJSON != "" {
		writeFile(t, filepath.Join(dir, "capture.json"), captureJSON)
	}
	return root
}

const (
	tinyRow  = "| `client/1.0` | a capture |\n"
	tinyMeta = `{"client":"c","client_version":"1.0","protocol_version":"2025-11-25",` +
		`"captured_at":"2026-01-01","command":"c"}`
	tinyAnswer = `{"id":1,"jsonrpc":"2.0","error":{"message":"method not found: nope","code":-32601}}` + "\n"
)

// buildEmpty serves a capture with no tools.
func buildEmpty(mcp.CaptureMeta) (*mcp.Server, error) { return mcp.NewServer(emptyRegistry()), nil }

func TestReplayAllAcceptsAMatchingCapture(t *testing.T) {
	root := tinyRoot(t, tinyRow, tinyMeta, tinyAnswer)
	if err := mcp.ReplayAll(context.Background(), root, buildEmpty); err != nil {
		t.Fatal(err)
	}
}

func TestReplayAllRefuses(t *testing.T) {
	cases := map[string]struct {
		row, meta, out, want string
	}{
		"a diverging frame":    {tinyRow, tinyMeta, strings.Replace(tinyAnswer, "-32601", "-32600", 1), "diverged"},
		"a missing frame":      {tinyRow, tinyMeta, "", "1 frames"},
		"no capture.json":      {tinyRow, "", tinyAnswer, "capture.json"},
		"no README row":        {"| `client/1.0-inbox` | |\n", tinyMeta, tinyAnswer, "README row"},
		"unattributed capture": {tinyRow, `{"client":"c"}`, tinyAnswer, "client_version"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := mcp.ReplayAll(context.Background(), tinyRoot(t, tc.row, tc.meta, tc.out), buildEmpty)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestReplayAllRefusesAnEmptyTree(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "goldens")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, "README.md"), tinyRow)
	if err := mcp.ReplayAll(context.Background(), root, buildEmpty); err == nil {
		t.Fatal("an empty capture tree proved nothing and must not pass")
	}
}

func TestReadCaptureMissingMetaIsInvalidInput(t *testing.T) {
	root := tinyRoot(t, tinyRow, "", tinyAnswer)
	_, _, _, err := mcp.ReadCapture(filepath.Join(root, "client", "1.0"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "capture.json") {
		t.Fatalf("err = %v, want KindInvalidInput naming capture.json", err)
	}
}

func TestReplayAnswersRequestsNotNotifications(t *testing.T) {
	in := [][]byte{
		[]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`),
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`),
		[]byte(`not json`),
	}
	got, err := mcp.Replay(context.Background(), mcp.NewServer(emptyRegistry()), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want one per request and one for the bad line: %s", len(got), got)
	}
	if !bytes.Contains(got[1], []byte("-32700")) {
		t.Errorf("a bad line must answer as the transport does: %s", got[1])
	}
}

func TestCompareFramesServerVersion(t *testing.T) {
	frame := func(version string) [][]byte {
		return [][]byte{[]byte(`{"result":{"serverInfo":{"version":"` + version + `"}}}`)}
	}
	if err := mcp.CompareFrames(frame("recorded-elsewhere"), frame(buildinfo.Version)); err != nil {
		t.Errorf("a stamp recorded by another build must not fail: %v", err)
	}
	if err := mcp.CompareFrames(frame(buildinfo.Version), frame("wrong")); err == nil {
		t.Error("a serverInfo.version that is not the build version must fail")
	}
	// Only that one path is masked: the same key elsewhere still compares.
	other := func(v string) [][]byte { return [][]byte{[]byte(`{"result":{"version":"` + v + `"}}`)} }
	if err := mcp.CompareFrames(other("a"), other("b")); err == nil {
		t.Error("a version outside serverInfo must compare literally")
	}
}

// TestMCPReplaySkipsDaemonCaptures marks a capture needs:daemon whose frames
// would fail if replayed, and asserts ReplayAll skips it, reports no failure
// and never builds a server for it. The same capture without the mark must
// fail, or the skip proved nothing.
func TestMCPReplaySkipsDaemonCaptures(t *testing.T) {
	daemonMeta := strings.TrimSuffix(tinyMeta, "}") + `,"needs":"daemon"}`
	diverging := strings.Replace(tinyAnswer, "-32601", "-32600", 1)
	built := 0
	build := func(meta mcp.CaptureMeta) (*mcp.Server, error) {
		built++
		return buildEmpty(meta)
	}
	if err := mcp.ReplayAll(context.Background(), tinyRoot(t, tinyRow, daemonMeta, diverging), build); err != nil {
		t.Fatalf("a needs:daemon capture was replayed: %v", err)
	}
	if built != 0 {
		t.Fatalf("ReplayAll built %d servers for a needs:daemon capture, want 0", built)
	}
	if err := mcp.ReplayAll(context.Background(), tinyRoot(t, tinyRow, tinyMeta, diverging), build); err == nil {
		t.Fatal("the same capture without needs:daemon must fail")
	}
}

// delegateCapture is the claude-code session that called the delegate tool.
const delegateCapture = "claude-code/delegate"

// TestMCPDelegateSpec covers the delegate capture both ways. ReplayAll over
// the committed tree accepts it as a needs:daemon capture (capture.json and
// README row present) with the production builder, and its owner replay
// drives in.jsonl through the production Server and the coretools delegate
// registrations against a stand-in daemon, matching the recorded answer.
func TestMCPDelegateSpec(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := mcp.ReplayAll(ctx, goldensRoot, buildCaptured); err != nil {
		t.Fatal(err)
	}
	in, out, meta, err := mcp.ReadCapture(filepath.Join(goldensRoot, filepath.FromSlash(delegateCapture)))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Needs != "daemon" || meta.Client != "claude-code" {
		t.Fatalf("delegate capture.json = %+v, want client claude-code needing a daemon", meta)
	}
	got, err := mcp.Replay(ctx, buildDelegate(), in)
	if err != nil {
		t.Fatal(err)
	}
	if err := mcp.CompareFrames(out, got); err != nil {
		t.Errorf("the delegate answer changed since the real client accepted it: %v", err)
	}
}

// buildDelegate is the production Server over the coretools delegate
// registrations, dispatching to a stand-in daemon that dispatches one job.
func buildDelegate() *mcp.Server {
	daemon := rpc.NewRegistry()
	dispatched := func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"job_id": "j_test", "status": "dispatched"}, nil
	}
	daemon.Register("conductor.delegate", dispatched)
	daemon.Register("conductor.delegate_wait", dispatched)
	return mcp.NewServer(mcp.NewToolRegistry(noBuiltins, mcp.AllowAllFilter{}, coretools.Registrations(daemon)...))
}

// noBuiltins is a manifest source with no plugins: only the delegate tools.
func noBuiltins() []plugin.BuiltinRegistration { return nil }

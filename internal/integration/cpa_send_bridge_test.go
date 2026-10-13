//go:build !windows && integration

// Purpose: fix18's REQUIRED real-socket proof (T0 decision L2) of the route
//
//	the harness actually uses: an MCP client speaking newline-delimited
//	JSON-RPC to the SAME stdio server `cascade mcp serve --stdio` runs
//	(internal/mcp's NewServer over plugin.Builtins, transport.NewStdioTransport),
//	calling cascade_cpa_send. The tool reaches the builtin cascade-pa plugin,
//	whose Conversations is the one internal/plugins' init() registered; that
//	decorator appends through chat.append_turn and then dials bridge.send, both
//	over the real daemon socket CASCADE_HOME resolves to
//	(cpa_send_bridge_env_test.go), where the real enabled bridge's Send makes
//	the Bot API call. Only the Bot API transport is a fake.
//
//	The MCP side and the daemon side share one test process, so the wiring
//	proof below is what shows the mirror crosses the SOCKET: with bridge.send
//	left unregistered on the served registry — the bridge still armed in this
//	process — nothing reaches Telegram and the tool reports the failure.
//
// SPORT: internal/integration cpa_send_bridge (ADD) — fix18 round 3 (L2/L3).
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/transport"
	_ "github.com/acamarata/cascade/internal/plugins" // init(): cascade-pa's Conversations
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// callCpaSendOverStdio runs one MCP session (initialize, initialized,
// tools/call cascade_cpa_send) over the stdio transport and returns the
// tool's decoded output.
func callCpaSendOverStdio(t *testing.T, args map[string]string) tools.SendOutput {
	t.Helper()
	call, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": tools.ToolSend, "arguments": args}})
	if err != nil {
		t.Fatalf("marshal tools/call: %v", err)
	}
	frames := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25",` +
		`"capabilities":{},"clientInfo":{"name":"cascade-integration-test","version":"0"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + string(call) + "\n"
	out := &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv := mcp.NewServer(mcp.NewToolRegistry(plugin.Builtins, mcp.AllowAllFilter{}))
	if err := transport.NewStdioTransport(srv, strings.NewReader(frames), out).Serve(ctx); err != nil {
		t.Fatalf("stdio Serve: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var resp struct {
			ID     int `json:"id"`
			Result struct {
				Content []struct{ Text string } `json:"content"`
				IsError bool                    `json:"isError"`
			} `json:"result"`
			Error *struct{ Message string } `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil || resp.ID != 2 {
			continue
		}
		if resp.Error != nil || resp.Result.IsError || len(resp.Result.Content) != 1 {
			t.Fatalf("tools/call cascade_cpa_send failed: %s", line)
		}
		var got tools.SendOutput
		if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &got); err != nil {
			t.Fatalf("decode cascade_cpa_send output %q: %v", resp.Result.Content[0].Text, err)
		}
		return got
	}
	t.Fatalf("no tools/call response in the stdio output:\n%s", out.String())
	return tools.SendOutput{}
}

func TestCpaSendReachesTheBridgeOverTheRealSocket(t *testing.T) {
	d := startCPADaemon(t, true)

	got := callCpaSendOverStdio(t, map[string]string{
		"content": "hello owner", "thread_id": cpaBridgeThread, "sensitivity": "internal"})
	if got.ThreadID != cpaBridgeThread || got.TurnID == "" {
		t.Fatalf("cascade_cpa_send = %+v, want a turn recorded on %s", got, cpaBridgeThread)
	}
	if got.Bridge == nil || got.Bridge.Status != tools.BridgeMirrored {
		t.Fatalf("bridge outcome = %+v, want mirrored", got.Bridge)
	}
	sent := d.api.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], `"chat_id":`+cpaBridgeChat) ||
		!strings.Contains(sent[0], `"text":"hello owner"`) {
		t.Fatalf("Bot API sendMessage bodies = %v, want exactly one carrying the turn", sent)
	}
}

// TestCpaSendDefaultSensitivityIsRefusedVisibly is L3 end to end: an unset
// sensitivity resolves to restricted, the daemon refuses the mirror
// (bridge.refused on the bus), nothing reaches the Bot API, and the MCP
// caller sees a typed refused outcome rather than a silent success.
func TestCpaSendDefaultSensitivityIsRefusedVisibly(t *testing.T) {
	d := startCPADaemon(t, true)

	got := callCpaSendOverStdio(t, map[string]string{"content": "stay home", "thread_id": cpaBridgeThread})
	if got.Sensitivity != tools.SensitivityRestricted {
		t.Fatalf("echoed sensitivity = %q, want restricted", got.Sensitivity)
	}
	if got.Bridge == nil || got.Bridge.Status != tools.BridgeRefused || got.Bridge.Reason == "" {
		t.Fatalf("bridge outcome = %+v, want a typed refusal with its reason", got.Bridge)
	}
	if sent := d.api.sent(); len(sent) != 0 {
		t.Fatalf("a refused turn reached the Bot API: %v", sent)
	}
	evs, err := d.bus.Replay(context.Background(), "bridge", 0)
	if err != nil || len(evs) != 1 || string(evs[0].Kind) != "bridge.refused" {
		t.Fatalf("bridge events = %+v, %v; want exactly one bridge.refused", evs, err)
	}
	if strings.Contains(string(evs[0].Payload), "stay home") {
		t.Fatal("bridge.refused carries the turn's content")
	}
}

// TestCpaSendBridge_WiringProof proves the mirror travels over the socket:
// with bridge.send unregistered on the served registry (the bridge still
// armed in this process), the turn is recorded, nothing reaches the Bot
// API, and the caller sees a failed outcome.
func TestCpaSendBridge_WiringProof(t *testing.T) {
	d := startCPADaemon(t, false)

	got := callCpaSendOverStdio(t, map[string]string{
		"content": "hello owner", "thread_id": cpaBridgeThread, "sensitivity": "internal"})
	if got.TurnID == "" {
		t.Fatalf("cascade_cpa_send = %+v, want the turn still recorded", got)
	}
	if got.Bridge == nil || got.Bridge.Status != tools.BridgeFailed {
		t.Fatalf("bridge outcome = %+v, want failed (bridge.send not served)", got.Bridge)
	}
	if sent := d.api.sent(); len(sent) != 0 {
		t.Fatalf("the Bot API was reached without bridge.send on the socket: %v", sent)
	}
}

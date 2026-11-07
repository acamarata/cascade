//go:build integration && !windows

// Purpose: TestClaudeGatewayReplay, the real-counterpart run of class gateway:
// the vendor CLI through the shim and the real claude driver against a loopback
// fake. The fake checks the bearer value, scripts one Bash tool_use and records
// whether the tool saw the value (the disclosed residual, as evidence).
// Inputs: CASCADE_TEST_CLAUDE_BINARY, the absolute path of the pinned CLI.
// Constraints: integration tag, run in the golang:1.26-bookworm container: the
// darwin CLI reads the login keychain whatever HOME is. Unset, the test skips.
// Only 127.0.0.1 sockets; the shim's dial is routed to the fake.
// SPORT: internal.providers.agents tests (ADD) - P1-GWY-05.

package agents

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/pkg/provider"
)

// replayFake is the loopback gateway. It answers the first message request
// with the Bash tool_use and every later one with a closing text.
type replayFake struct {
	expected string
	mu       sync.Mutex
	calls    int
	bearer   bool
	bodies   []string
}

func (s *replayFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bearer = s.bearer || r.Header.Get("Authorization") == "Bearer "+s.expected
	s.bodies = append(s.bodies, string(body))
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	block, delta, stop := `{"type":"text","text":""}`, `{"type":"text_delta","text":"done"}`, "end_turn"
	if first {
		block = `{"type":"tool_use","id":"toolu_one","name":"Bash","input":{}}`
		delta = `{"type":"input_json_delta","partial_json":"{\"command\":\"printenv ` + gwAuthName + ` | wc -c\"}"}`
		stop = "tool_use"
	}
	for _, ev := range []string{
		`{"type":"message_start","message":{"id":"msg_one","type":"message","role":"assistant","model":"model-a","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":` + block + `}`,
		`{"type":"content_block_delta","index":0,"delta":` + delta + `}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"` + stop + `"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	} {
		name, _, _ := strings.Cut(strings.TrimPrefix(ev, `{"type":"`), `"`)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, ev)
	}
}

// inherited reports whether the second request's tool_result counted the value.
func (s *replayFake) inherited(value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := strconv.Itoa(len(value) + 1)
	for _, b := range s.bodies[1:] {
		if strings.Contains(b, "tool_result") && strings.Contains(b, want) {
			return true
		}
	}
	return false
}

// replayRepo makes the fixture's worktree a real one-commit git repository.
func replayRepo(t *testing.T, root string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."},
		{"-c", strings.Join([]string{"user.name", "fixture"}, "="), "-c", strings.Join([]string{"user.email", "fixture@gateway.invalid"}, "="), "commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args[0], err, out)
		}
	}
}

func TestClaudeGatewayReplay(t *testing.T) {
	bin := os.Getenv("CASCADE_TEST_CLAUDE_BINARY")
	if bin == "" {
		t.Skip("CASCADE_TEST_CLAUDE_BINARY not set")
	}
	canary := gwCanary(t)
	fake := &replayFake{expected: canary}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	f := newGatewayFix(t, canary)
	replayRepo(t, f.root)
	reg := &registry{rows: map[HarnessID]entry{}}
	reg.add(claudeRow, newClaudeDriver)
	res, err := newResolver(reg, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	accts := gwAccounts()
	accts[0].BaseURL, accts[0].ID = srv.URL, srv.URL
	f.deps.Resolver = res
	f.deps.Gateway = GatewayDeps{Accounts: accts, Source: gateway.Source{}}
	f.deps.Binaries[HarnessClaude] = bin
	f.deps.Postures = map[HarnessID]provider.CompliancePosture{
		HarnessClaude: provider.NewCompliancePosture([]string{"gateway"}, true, true, []string{"agent"}, "steady", false)}
	f.build()
	host := strings.TrimPrefix(srv.URL, "http://")
	f.shim.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, host)
	}
	f.req.Job.ToolPolicy, f.req.Job.Model = provider.ToolPolicyAll, "model-a"
	f.req.Job.Envelope.Tools = []string{envelopeToolPrefix + provider.ToolPolicyAll}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	h, err := f.shim.Spawn(ctx, f.req)
	if err != nil {
		t.Fatalf("Spawn of class gateway through the real driver: %v", err)
	}
	t.Cleanup(func() { _ = h.Release(context.Background()) })
	if _, err := h.Driver.Collect(ctx, h.JobID); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !fake.bearer {
		t.Fatal("the fake never saw the bearer value")
	}
	t.Logf("session tool inherited the gateway value: %v", fake.inherited(canary))
	profile := gwProfileText(t, filepath.Join(f.base, string(HarnessClaude), string(h.JobID)))
	if n := strings.Count(profile, canary); n != 0 {
		t.Fatalf("the per-spawn config dir holds the value %d times", n)
	}
}

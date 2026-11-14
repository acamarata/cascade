//go:build !windows

// Purpose: `cascade run --agent` against a stub conductor registry served on a
// real unix socket through the production client wiring.
// SPORT: cmd/cascade/run (CHANGE).
package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// agentStub records the params of each conductor verb it serves.
type agentStub struct {
	mu       sync.Mutex
	delegate map[string]any
	wait     map[string]any
}

// agentRig serves a stub daemon and returns deps that dial it.
func agentRig(t *testing.T, env string) (runDeps, *agentStub) {
	t.Helper()
	stub := &agentStub{}
	reg := rpc.NewRegistry()
	reg.Register(runAgentDelegateMethod, func(_ context.Context, raw json.RawMessage) (any, error) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return agentDelegateReply{JobID: "job-1", Status: "dispatched"}, json.Unmarshal(raw, &stub.delegate)
	})
	reg.Register(runAgentWaitMethod, func(_ context.Context, raw json.RawMessage) (any, error) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return agentResultView{JobID: "job-1", State: "completed", Output: "done", DataClass: "restricted"}, json.Unmarshal(raw, &stub.wait)
	})
	deps := runDeps{Paths: fakeDaemonPaths{root: t.TempDir()}, Environ: func() []string { return nil }}
	deps.Getenv = func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return env
		}
		return ""
	}
	deps.DialContext = redirectDial(client.UnixDialer, serveRegistry(t, reg))
	return deps, stub
}

// execAgent runs the real run command with args and empty stdin.
func execAgent(t *testing.T, deps runDeps, args ...string) (string, error) {
	t.Helper()
	cmd := newRunCmd(deps)
	cmd.SetIn(strings.NewReader(""))
	return execLeaf(t, cmd, args)
}

func TestRunAgentRefusesWithoutDaemon(t *testing.T) {
	deps := runDeps{Paths: fakeDaemonPaths{root: t.TempDir()}, Getenv: func(string) string { return "" },
		Environ: func() []string { return nil }, DialContext: client.UnixDialer}
	out, err := execAgent(t, deps, "--agent", "list files", "--timeout", "1s")
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "the daemon is not running") {
		t.Fatalf("no daemon: err = %v, out = %q; want KindUnavailable \"the daemon is not running\"", err, out)
	}
}

func TestRunAgentFlagsComplete(t *testing.T) {
	flags := newRunCmd(runDeps{}).Flags()
	params := map[string]string{"intent": "agent", "task_class": "task-class", "min_quality": "min-quality",
		"deadline": "deadline", "mutation": "mutation", "job_id": "job", "wait_timeout_ms": "timeout"}
	for param, flag := range params {
		if flags.Lookup(flag) == nil {
			t.Errorf("delegate param %q has no --%s flag", param, flag)
		}
	}
	var sent map[string]any
	raw, _ := json.Marshal(agentDelegateParams{})
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatal(err)
	}
	for k := range sent {
		if _, ok := params[k]; !ok {
			t.Errorf("request field %q is not a known delegate param", k)
		}
	}
}

func TestRunAgentNoInputCompletes(t *testing.T) {
	deps, stub := agentRig(t, "1")
	out, err := execAgent(t, deps, "--agent", "list files", "--task-class", "review", "--min-quality", "high",
		"--deadline", "batch", "--job", "job-9", "--timeout", "2s")
	if err != nil {
		t.Fatalf("run --agent under CASCADE_NO_INPUT=1: %v (%s)", err, out)
	}
	var env struct{ Data agentResultView }
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Data.JobID != "job-1" || env.Data.State != "completed" {
		t.Fatalf("envelope = %q, %v; want job-1 completed", out, err)
	}
	want := map[string]any{"intent": "list files", "task_class": "review", "min_quality": "high",
		"deadline": "batch", "job_id": "job-9", "mutation": false}
	for k, v := range want {
		if stub.delegate[k] != v {
			t.Errorf("delegate param %s = %v, want %v", k, stub.delegate[k], v)
		}
	}
}

func TestRunAgentWaitsWithDeadline(t *testing.T) {
	deps, stub := agentRig(t, "")
	out, err := execAgent(t, deps, "--agent", "summarize the repo", "--deadline", "interactive", "--timeout", "1500ms")
	if err != nil {
		t.Fatalf("run --agent: %v (%s)", err, out)
	}
	if !strings.Contains(out, "done") || !strings.Contains(out, "job-1") {
		t.Errorf("output = %q, want the agent output and the job id", out)
	}
	if stub.wait["job_id"] != "job-1" || stub.wait["timeout_ms"] != float64(1500) {
		t.Errorf("delegate_wait params = %v, want job_id job-1 timeout_ms 1500", stub.wait)
	}
	if stub.delegate["deadline"] != "interactive" {
		t.Errorf("delegate deadline = %v, want interactive", stub.delegate["deadline"])
	}
}

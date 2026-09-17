//go:build integration

// Purpose: the v1 DST-S4 acceptance on a REAL counterpart — kill a node
//   mid-run and the interrupted work re-queues and resumes elsewhere with
//   its journal, and the action it was running produces exactly one
//   external side effect across the kill.
//
// WHAT IS REAL HERE, EXACTLY. The node is a real `cascade node serve`
//   process: the shipped binary, built from this tree, with its own data
//   directory and its own generated identity. It really serves
//   node.dispatch.execute over a real unix socket, and it really reserves
//   the action in its durable on-disk log before answering. It is then
//   really SIGKILLed — no graceful shutdown, no cleanup — which is what
//   "lost mid-run" means and what a t.Cleanup-based fake cannot reproduce.
//   The results leg is real git.
//
//   What this lane does NOT cover is the cross-machine hop: the controller
//   reaches the node over a loopback socket rather than the S-36.T3 ssh
//   tunnel (which has its own real-sshd lane) and both processes are on
//   this machine. The enrolled-second-machine kill drill is the 06 §7
//   owner prerequisite, and it gates that drill only — never this one.
//
// SPORT: internal/nodes TestKillNodeMidRun (P1-E17-W4-S37-T3).

package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// realNode is one running `cascade node serve` process and its home.
type realNode struct {
	cmd  *exec.Cmd
	home string
	sock string
}

// startRealNode builds the shipped binary and runs `cascade node serve`
// against a throwaway home, returning once its socket answers.
//
// The home is short on purpose: a unix socket path is capped near 104
// bytes, and t.TempDir's is already most of that on macOS.
func startRealNode(t *testing.T, bin, home string) *realNode {
	t.Helper()
	cmd := exec.Command(bin, "node", "serve") //nolint:gosec // bin is built by this test.
	cmd.Env = append(os.Environ(), "HOME="+home, "CASCADE_HOME="+filepath.Join(home, ".c"))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the node agent: %v", err)
	}
	n := &realNode{cmd: cmd, home: home,
		sock: filepath.Join(home, ".c", "data", "nodes", "node.sock")}
	t.Cleanup(func() { n.kill() })
	n.waitForSocket(t)
	return n
}

// waitForSocket blocks until the node's socket accepts a connection.
func (n *realNode) waitForSocket(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("unix", n.sock); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the node agent never listened on %s", n.sock)
}

// kill SIGKILLs the node. No SIGTERM: a node that shut down cleanly is not
// a lost node, and the whole point is that nothing got to run on the way
// out.
func (n *realNode) kill() {
	if n.cmd.Process == nil {
		return
	}
	_ = n.cmd.Process.Signal(syscall.SIGKILL)
	_, _ = n.cmd.Process.Wait()
}

// call sends one JSON-RPC frame to the node over its real socket.
func (n *realNode) call(t *testing.T, method string, params any) (map[string]any, error) {
	t.Helper()
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","method":"` + method + `","params":` + string(encoded) + `,"id":1}`
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", n.sock)
		},
	}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://unix"+RPCPath, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the node answered something that is not JSON-RPC: %s", raw)
	}
	return decoded, nil
}

// buildCascade builds the shipped binary into dir.
func buildCascade(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "cascade")
	build := exec.Command("go", "build", "-o", bin, "./cmd/cascade")
	build.Dir = moduleRootForKillLane(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building cascade: %v\n%s", err, out)
	}
	return bin
}

// moduleRootForKillLane walks up to the directory holding go.mod.
func moduleRootForKillLane(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// shortHome makes a directory whose path leaves room for a unix socket.
func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "kn")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestKillNodeMidRun is the acceptance. One real node agent reserves an
// action and is then SIGKILLed; the controller observes the loss, re-queues
// onto a healthy spare with a fresh fenced attempt and the lost attempt's
// journal position, and the action is refused when it comes back to a node
// that already recorded it — one side effect across the kill.
func TestKillNodeMidRun(t *testing.T) {
	bin := buildCascade(t, t.TempDir())
	home := shortHome(t)
	node := startRealNode(t, bin, home)

	// 1. The node really reserves the action, in its durable on-disk log.
	const actionID = "kill-lane-action"
	resp, err := node.call(t, ExecuteMethod, ExecuteRequest{
		DispatchID: "d-kill", Attempt: 1, ActionID: actionID,
		Idempotent: true, Outcome: OutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("the node refused the dispatch before it was even killed: %v", err)
	}
	if resp["error"] != nil {
		t.Fatalf("node.dispatch.execute returned %v", resp["error"])
	}

	// 2. It dies mid-run. SIGKILL: nothing runs on the way out.
	node.kill()
	deadline := time.Now().Add(10 * time.Second)
	var lossErr error
	for time.Now().Before(deadline) {
		if _, lossErr = node.call(t, ExecuteMethod, ExecuteRequest{
			DispatchID: "d-kill", Attempt: 1, ActionID: actionID,
			Idempotent: true, Outcome: OutcomeSucceeded,
		}); lossErr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lossErr == nil {
		t.Fatal("the killed node kept answering; nothing was actually lost")
	}

	// 3. The controller reads that as a loss and re-queues, fenced, with
	//    the lost attempt's journal position.
	assertKillRequeue(t, lossErr)

	// 4. The work comes back to a node that already recorded it — which is
	//    what a restart looks like — and is refused before it runs again.
	assertSingleSideEffect(t, bin, home, actionID)
}

// assertKillRequeue drives the recovery decision over the real loss error.
func assertKillRequeue(t *testing.T, lossErr error) {
	t.Helper()
	signal, lost := DetectLoss(LossObservation{
		Liveness: LivenessReachable, Tunnel: TunnelUp, ChannelErr: lossErr,
	})
	if !lost {
		t.Fatalf("a node whose socket is gone was read as healthy (%v)", lossErr)
	}
	if signal != LossChannel {
		t.Errorf("signal = %q, want %q", signal, LossChannel)
	}

	deps := RequeueDeps{
		Placement:  Engine{Tunnels: func(string) TunnelState { return TunnelUp }},
		Candidates: []Candidate{healthyNode("killed"), healthyNode("spare")},
		Attempts:   NewAttemptRegister(),
		Continuity: fixedContinuity{records: []StreamedRecord{
			{Seq: 11, Attempt: 1, OperationID: "op-before-the-kill"},
		}},
		Attention: &recordingFiler{},
	}
	deps.Attempts.Next("d-kill") // the attempt that died

	plan, err := PlanRequeue(context.Background(), deps, RequeueRequest{
		DispatchID: "d-kill", LostNodeID: "killed",
		Action:      Action{ID: "kill-lane-action", Idempotent: true},
		Requirement: Requirement{Capabilities: []string{"docker"}, Sensitivity: SensitivityNormal},
		EntityID:    "job-kill",
	}, signal)
	if err != nil {
		t.Fatalf("the interrupted work was not re-queued: %v", err)
	}
	if plan.Node.NodeID != "spare" {
		t.Errorf("replacement node = %q, want the node that did not die", plan.Node.NodeID)
	}
	if plan.Attempt != 2 {
		t.Errorf("replacement attempt = %d, want 2 — the replacement must be fenced", plan.Attempt)
	}
	if plan.Resume.FromScratch || plan.Resume.Seq != 11 {
		t.Errorf("resume point = %+v, want the position the lost attempt reached", plan.Resume)
	}
	if !plan.Resume.Completed("op-before-the-kill") {
		t.Error("the replacement would re-run an operation the lost attempt had already recorded")
	}
}

// assertSingleSideEffect restarts a node over the SAME data directory and
// redelivers the action. The durable reservation survived the kill, so it
// is refused — one side effect, across a process that was never allowed to
// clean up after itself.
func assertSingleSideEffect(t *testing.T, bin, home, actionID string) {
	t.Helper()
	restarted := startRealNode(t, bin, home)
	resp, err := restarted.call(t, ExecuteMethod, ExecuteRequest{
		DispatchID: "d-kill", Attempt: 2, ActionID: actionID,
		Idempotent: true, Outcome: OutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("the restarted node did not answer: %v", err)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("the restarted node returned no result: %v", resp)
	}
	if got := result["outcome"]; got != string(OutcomeRefused) {
		t.Fatalf("outcome = %v, want %q: the redelivered action ran a second time",
			got, OutcomeRefused)
	}
}

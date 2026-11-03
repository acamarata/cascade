package codex

// Purpose: the shared test kit: a worktree, a fresh HOME and profile auth
// dir under t.TempDir(), a driver whose binary is this test binary in
// replay mode, and helpers to build session files and read what the
// driver wrote to the child.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

type kit struct {
	t                     *testing.T
	root, home, codexHome string
	opts                  replayOpts
	cfg                   Config
	drv                   *Driver
}

// newKit builds the replay harness. The driver refuses on Windows by
// design (tier-2, provider.ErrPlatform; see spawn_windows_test.go), so
// every replay-driven test skips there.
func newKit(t *testing.T) *kit {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the codex driver refuses on windows by design; spawn_windows_test.go covers the refusal")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	base := t.TempDir()
	k := &kit{t: t, root: filepath.Join(base, "wt"), home: filepath.Join(base, "home")}
	k.codexHome = filepath.Join(k.home, "codex")
	for _, d := range []string{k.root, k.codexHome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	k.opts = replayOpts{Session: testdataPath(t, "session.jsonl"), Log: true}
	k.cfg = Config{Binary: bin, Posture: entitled(true), Defaults: testDefaults(), Clock: testClock{}}
	return k
}

func testdataPath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func entitled(ok bool) provider.CompliancePosture {
	return provider.NewCompliancePosture([]string{"subscription"}, true, ok, []string{"app-server"}, "", false)
}

func testDefaults() provider.DriverDefaults {
	d := provider.NewDriverDefaults()
	d.ApprovalTimeout, d.CancelGrace = 10*time.Second, 200*time.Millisecond
	return d
}

// driver writes the replay options and builds the driver once.
func (k *kit) driver() *Driver {
	k.t.Helper()
	if k.drv != nil {
		return k.drv
	}
	b, _ := json.Marshal(k.opts)
	if err := os.WriteFile(filepath.Join(k.codexHome, "replay.json"), b, 0o600); err != nil {
		k.t.Fatal(err)
	}
	d, err := New(k.cfg)
	if err != nil {
		k.t.Fatalf("New: %v", err)
	}
	k.drv = d
	k.t.Cleanup(func() { cancelAll(d) })
	return d
}

// cancelAll stops every job a driver still runs, so no child outlives a test.
func cancelAll(d *Driver) {
	d.mu.Lock()
	var ids []provider.AgentJobID
	for id := range d.jobs {
		ids = append(ids, id)
	}
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, id := range ids {
		_ = d.Cancel(ctx, id)
	}
}

func (k *kit) env() provider.SpawnEnv {
	return provider.SpawnEnv{Home: k.home, Driver: []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + k.home, VendorAuthVar + "=" + k.codexHome,
	}}
}

func envelopeFor(root string) provider.SpawnEnvelope {
	return provider.SpawnEnvelope{
		Digest: strings.Repeat("ab", 32), Principal: "test-principal", Roots: []string{root},
		Sensitivity: provider.SensitivityInternal, ExpiresAt: time.Now().Add(time.Hour),
	}
}

func identity(b []byte) ([]byte, error) { return b, nil }

func (k *kit) spec() provider.AgentJobSpec {
	return provider.AgentJobSpec{
		Prompt: "Reply with the single word: ready", DataClass: provider.DataClassInternal,
		Env: k.env(), WorktreeRoot: k.root, Filter: identity, Envelope: envelopeFor(k.root),
	}
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// run spawns spec and collects it.
func (k *kit) run(spec provider.AgentJobSpec) (provider.SpawnResult, provider.CollectResult, error) {
	k.t.Helper()
	d := k.driver()
	res, err := d.Spawn(testCtx(k.t), spec)
	if err != nil {
		k.t.Fatalf("Spawn: %v", err)
	}
	cr, err := d.Collect(testCtx(k.t), res.JobID)
	return res, cr, err
}

// loadSession returns the committed session's lines.
func loadSession(t *testing.T) []sessionLine {
	t.Helper()
	b, err := os.ReadFile(testdataPath(t, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []sessionLine
	for _, l := range splitLines(b) {
		var sl sessionLine
		if err := json.Unmarshal(l, &sl); err != nil {
			t.Fatal(err)
		}
		out = append(out, sl)
	}
	return out
}

func s2c(tms int, frame string) sessionLine {
	return sessionLine{Dir: "s2c", TMS: tms, Frame: json.RawMessage(frame)}
}

// beforeTurnCompleted inserts extra lines just before turn/completed.
func beforeTurnCompleted(lines []sessionLine, extra ...sessionLine) []sessionLine {
	for i, l := range lines {
		if strings.Contains(string(l.Frame), `"turn/completed"`) {
			out := append(append([]sessionLine{}, lines[:i]...), extra...)
			return append(out, lines[i:]...)
		}
	}
	return append(lines, extra...)
}

// useSession writes lines as this kit's session.
func (k *kit) useSession(lines []sessionLine) {
	k.t.Helper()
	var buf bytes.Buffer
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			k.t.Fatal(err)
		}
		buf.Write(append(b, '\n'))
	}
	p := filepath.Join(k.home, "session.jsonl")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		k.t.Fatal(err)
	}
	k.opts.Session = p
}

// written returns every frame the driver wrote to the child.
func (k *kit) written() []map[string]json.RawMessage {
	b, _ := os.ReadFile(filepath.Join(k.codexHome, "stdin.log"))
	var out []map[string]json.RawMessage
	for _, l := range splitLines(b) {
		var m map[string]json.RawMessage
		if json.Unmarshal(l, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// answersTo returns the frames written in reply to server request id.
func (k *kit) answersTo(id string) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	for _, m := range k.written() {
		if _, isMethod := m["method"]; !isMethod && string(m["id"]) == id {
			out = append(out, m)
		}
	}
	return out
}

func (k *kit) wroteMethod(method string) bool {
	for _, m := range k.written() {
		if string(m["method"]) == `"`+method+`"` {
			return true
		}
	}
	return false
}

func (k *kit) childStarted() bool {
	_, err := os.Stat(filepath.Join(k.codexHome, "started"))
	return err == nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// isSentinel reports err wraps target by identity (never errors.Is, which
// compares Kind only) and carries the same Kind.
func isSentinel(err, target error) bool {
	wk, _ := cascade.KindOf(target)
	if gk, _ := cascade.KindOf(err); gk != wk {
		return false
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == target {
			return true
		}
	}
	return false
}

func mustSentinel(t *testing.T, err, target error) {
	t.Helper()
	if !isSentinel(err, target) {
		t.Fatalf("error %v does not wrap %v by identity", err, target)
	}
}

// approvalLine is a command approval server request with rpc id id.
func approvalLine(tms int, id, method, params string) sessionLine {
	return s2c(tms, `{"id":`+id+`,"method":"`+method+`","params":`+params+`}`)
}

const cmdApproval = "item/commandExecution/requestApproval"

const cmdParams = `{"threadId":"019a6f10-3c2e-7d41-9b8a-5e0c2f7d1a42","turnId":"0","itemId":"item-3","command":"go test ./...","cwd":"/w","reason":"run the tests"}`

// nextApproval waits for one surfaced approval.
func nextApproval(t *testing.T, d *Driver) provider.ApprovalRequest {
	t.Helper()
	select {
	case req := <-d.ApprovalRequests():
		return req
	case <-time.After(15 * time.Second):
		t.Fatal("no approval request surfaced")
	}
	return provider.ApprovalRequest{}
}

// testClock is the wall clock for the envelope expiry check.
type testClock struct{}

func (testClock) Now() time.Time { return time.Now() }

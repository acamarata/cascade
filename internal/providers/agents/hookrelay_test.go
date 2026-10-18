//go:build !windows

package agents

// Purpose: the hook relay against a fake driver on a real unix socket and the
//   captured claude hook payloads (testdata/claude-hooks): frames arrive
//   token-framed and sequenced, a loose, foreign or symlinked callback file
//   sends nothing, and a tree without a callback file is a no-op.
// Constraints: see hookbridge_test.go (the rig). The concurrency test runs
//   each relay as its own process (this test binary re-executed), because a
//   process-local counter would pass in-process.
// SPORT: internal.providers.agents/ADD (P1-AGT-14).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// failReader fails the test if the relay reads stdin.
type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Helper()
	f.t.Error("the relay read stdin when it should not have")
	return 0, os.ErrClosed
}

func relayCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func relayOnce(t *testing.T, cwd, event string, payload []byte) error {
	t.Helper()
	return Relay(relayCtx(t), cwd, event, bytes.NewReader(payload), os.Getuid())
}

func TestHookRelayFramesReachDriverChannel(t *testing.T) {
	cases := []struct {
		file, event, kind, tool string
		wantPaths               []string
		hasHash                 bool
		wantDuration            int64
	}{
		{"pretooluse-read.json", HookPreToolUse, "status", "Read", []string{"a.txt"}, false, 0},
		{"posttooluse-read.json", HookPostToolUse, "status", "Read", []string{"a.txt"}, true, 2},
		{"posttooluse-write.json", HookPostToolUse, "status", "Write", []string{"b.txt"}, true, 2},
		{"stop.json", HookStop, "done", "Stop", []string{}, false, 0},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			r := newRig(t)
			r.serve(1)
			raw := r.hook(c.file)
			if err := relayOnce(t, r.root, c.event, raw); err != nil {
				t.Fatalf("Relay: %v", err)
			}
			r.wait()
			var in struct {
				ToolUseID    string          `json:"tool_use_id"`
				ToolResponse json.RawMessage `json:"tool_response"`
			}
			_ = json.Unmarshal(raw, &in)
			sum := sha256.Sum256(in.ToolResponse)
			wantHash, wantKey := "", ""
			if c.hasHash {
				wantHash = hex.EncodeToString(sum[:])
			}
			if in.ToolUseID != "" {
				wantKey = c.event + ":" + in.ToolUseID
			}
			frames := r.got()
			if len(frames) != 1 {
				t.Fatalf("driver received %d frames, want 1", len(frames))
			}
			p := frames[0]
			if p.Seq != 1 || p.Kind != c.kind || p.ToolName != c.tool || p.DedupKey != wantKey || p.ResultHash != wantHash ||
				p.DurationMS != c.wantDuration || p.ExitCode != 0 || strings.Join(p.Paths, ",") != strings.Join(c.wantPaths, ",") {
				t.Fatalf("frame %+v does not match the captured %s payload (want key %q hash %q)", p, c.file, wantKey, wantHash)
			}
		})
	}
}

func TestHookRelayPathOutsideTreeIsDropped(t *testing.T) {
	r := newRig(t)
	r.serve(1)
	raw := bytes.ReplaceAll(r.hook("pretooluse-read.json"), []byte(r.root+"/a.txt"), []byte("/etc/hosts"))
	if err := relayOnce(t, r.root, HookPreToolUse, raw); err != nil {
		t.Fatal(err)
	}
	r.wait()
	if p := r.got()[0]; len(p.Paths) != 0 || p.Paths == nil {
		t.Fatalf("paths %#v: a file outside the tree must not be forwarded (and Paths stays a non-nil list)", p.Paths)
	}
}

// TestHookRelayHelperProcess is the child of the concurrency test; it is a
// no-op in a normal run.
func TestHookRelayHelperProcess(_ *testing.T) {
	root := os.Getenv("HOOKRELAY_HELPER_ROOT")
	if root == "" {
		return
	}
	err := Relay(context.Background(), root, os.Getenv("HOOKRELAY_HELPER_EVENT"), os.Stdin, os.Getuid())
	if err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestHookRelaySeqStrictlyIncreasesUnderConcurrency(t *testing.T) {
	const n = 16
	r := newRig(t)
	r.serve(n)
	base := r.hook("posttooluse-read.json")
	var wg sync.WaitGroup
	errs := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := bytes.ReplaceAll(base, []byte("toolu_01PJnfeg18hY9djy2MbFSd7E"), []byte(fmt.Sprintf("toolu_%02d", i)))
			cmd := exec.Command(os.Args[0], "-test.run=^TestHookRelayHelperProcess$")
			cmd.Env = append(os.Environ(), "HOOKRELAY_HELPER_ROOT="+r.root, "HOOKRELAY_HELPER_EVENT="+HookPostToolUse)
			cmd.Stdin = bytes.NewReader(payload)
			if out, err := cmd.CombinedOutput(); err != nil {
				errs[i] = fmt.Sprintf("%v: %s", err, out)
			}
		}()
	}
	wg.Wait()
	r.wait()
	for i, e := range errs {
		if e != "" {
			t.Fatalf("relay process %d: %s", i, e)
		}
	}
	frames := r.got()
	if len(frames) != n {
		t.Fatalf("driver received %d frames, want %d", len(frames), n)
	}
	for i, p := range frames {
		if p.Seq != uint64(i+1) {
			t.Fatalf("frame %d arrived with Seq %d, want %d: Seq must be distinct and increasing in arrival order across processes", i, p.Seq, i+1)
		}
	}
}

func TestHookRelayRefusesLooseTokenFile(t *testing.T) {
	cases := map[string]func(t *testing.T, r *rig, path string) int{
		"mode 0644": func(_ *testing.T, r *rig, path string) int { r.writeCallback(path, 0o644); return os.Getuid() },
		"mode 0660": func(_ *testing.T, r *rig, path string) int { r.writeCallback(path, 0o660); return os.Getuid() },
		"other uid": func(t *testing.T, _ *rig, _ string) int {
			foreignOwner(t, provider.CallbackFileName)
			return os.Getuid()
		},
		"symlink to another tree's file": func(t *testing.T, r *rig, path string) int {
			other := filepath.Join(t.TempDir(), "other-callback.json")
			r.writeCallback(other, 0o600)
			_ = os.Remove(path)
			if err := os.Symlink(other, path); err != nil {
				t.Fatal(err)
			}
			return os.Getuid()
		},
		"directory": func(t *testing.T, _ *rig, path string) int {
			_ = os.Remove(path)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return os.Getuid()
		},
		"socket path is a regular file": func(t *testing.T, r *rig, _ string) int {
			_ = os.Remove(r.sock)
			if err := os.WriteFile(r.sock, nil, 0o600); err != nil {
				t.Skipf("cannot replace the socket path: %v", err)
			}
			return os.Getuid()
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			path := filepath.Join(r.root, ".claude", provider.CallbackFileName)
			uid := setup(t, r, path)
			err := Relay(relayCtx(t), r.root, HookPostToolUse, bytes.NewReader(r.hook("posttooluse-read.json")), uid)
			if kindOf(err) != cascade.KindPermissionDenied {
				t.Fatalf("got %v, want a permission-denied refusal", err)
			}
			if strings.Contains(err.Error(), hex.EncodeToString(fakeToken[:])) {
				t.Fatalf("the refusal leaks the token: %v", err)
			}
			if name != "socket path is a regular file" && r.connected() {
				t.Fatal("a frame was sent although the callback file is not the driver's")
			}
		})
	}
}

func TestHookRelayAbsentCallbackFileIsNoop(t *testing.T) {
	r := newRig(t)
	if err := os.Remove(filepath.Join(r.root, ".claude", provider.CallbackFileName)); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{HookPostToolUse, "SessionEnd", ""} {
		if err := Relay(relayCtx(t), r.root, event, failReader{t}, os.Getuid()); err != nil {
			t.Fatalf("event %q: %v, want a no-op", event, err)
		}
	}
	if err := Relay(relayCtx(t), t.TempDir(), HookStop, failReader{t}, os.Getuid()); err != nil {
		t.Fatalf("a tree with no .claude at all: %v", err)
	}
	if r.connected() {
		t.Fatal("a no-op relay connected to a socket")
	}
}

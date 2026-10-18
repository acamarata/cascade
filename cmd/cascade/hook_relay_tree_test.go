//go:build !windows

// Purpose: `cascade hook relay` with the real relay against leased trees:
//
//	the callback lookup is bound to CLAUDE_PROJECT_DIR, never to the process
//	working directory or the payload's cwd.
//
// Constraints: the listener uses raw syscalls (unit tests may not import
//
//	"net"); every directory is a temp directory and HOME is a temp dir. The
//	tests change the process working directory (t.Chdir), so none runs in
//	parallel.
//
// SPORT: cmd/cascade root mounts (P1-AGT-14, contract:agent-hook-relay).
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// treeTokenHex is an obviously fake session token.
var treeTokenHex = strings.Repeat("ab", 16)

// treeChannel is one leased tree with a callback file naming a listening
// socket in a private directory.
type treeChannel struct {
	t          *testing.T
	root, sock string
	fd         int
}

func newTreeChannel(t *testing.T) *treeChannel {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("", "hr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	c := &treeChannel{t: t, root: root, sock: filepath.Join(sockDir, "s")}
	if c.fd, err = syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(c.fd) })
	if err := syscall.Bind(c.fd, &syscall.SockaddrUnix{Name: c.sock}); err != nil {
		t.Skipf("cannot bind a unix socket at %q: %v", c.sock, err)
	}
	if err := syscall.Listen(c.fd, 8); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(provider.CallbackFile{Socket: c.sock, Token: treeTokenHex})
	if err := os.WriteFile(filepath.Join(root, ".claude", provider.CallbackFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

// serveOne answers one connection with "ok" and returns its line on the channel.
func (c *treeChannel) serveOne() <-chan []byte {
	got := make(chan []byte, 1)
	go func() {
		nfd, _, err := syscall.Accept(c.fd)
		if err != nil {
			close(got)
			return
		}
		defer func() { _ = syscall.Close(nfd) }()
		var line []byte
		buf := make([]byte, 4096)
		for !bytes.Contains(line, []byte("\n")) {
			n, rerr := syscall.Read(nfd, buf)
			if n <= 0 || rerr != nil {
				break
			}
			line = append(line, buf[:n]...)
		}
		_, _ = syscall.Write(nfd, []byte("ok\n"))
		got <- line
	}()
	return got
}

// connected reports whether a client is queued on the socket.
func (c *treeChannel) connected() bool {
	if err := syscall.SetNonblock(c.fd, true); err != nil {
		c.t.Fatal(err)
	}
	nfd, _, err := syscall.Accept(c.fd)
	if err == nil {
		_ = syscall.Close(nfd)
	}
	return err == nil
}

// realRelayEnv runs agents.Relay with CLAUDE_PROJECT_DIR set to projectDir
// ("" = unset).
func realRelayEnv(projectDir string) hookRelayEnv {
	return hookRelayEnv{
		Getenv: func(k string) string {
			if k == projectDirVar {
				return projectDir
			}
			return ""
		},
		Getuid: os.Getuid, Relay: agents.Relay,
		Clock: runtime.NewFixedClock(time.Now()), Timeout: 5 * time.Second,
	}
}

// isolateRelayHome points HOME and USERPROFILE at a fresh temp dir.
func isolateRelayHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// stopPayload is a Stop hook payload whose cwd names dir.
func stopPayload(dir string) string {
	b, _ := json.Marshal(map[string]any{"session_id": "s", "cwd": dir, "hook_event_name": "Stop", "stop_hook_active": false})
	return string(b)
}

func TestHookRelayCmdBindsToProjectDirNotCwd(t *testing.T) {
	isolateRelayHome(t)
	a := newTreeChannel(t)
	treeB, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a.root, filepath.Join(treeB, "lnk")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(a.root)
	if err := runRelayCmd(t, realRelayEnv(treeB), stopPayload(a.root), "Stop"); err != nil {
		t.Fatalf("project dir tree B, working directory and payload cwd in tree A: %v, want a no-op", err)
	}
	if a.connected() {
		t.Fatal("a hook of tree B's spawn reached tree A's channel through the working directory or the payload cwd")
	}
	err = runRelayCmd(t, realRelayEnv(""), stopPayload(a.root), "Stop")
	if sent := a.connected(); err != nil || sent {
		t.Fatalf("CLAUDE_PROJECT_DIR unset: err=%v sent=%t, want exit 0 and nothing sent", err, sent)
	}
}

func TestHookRelayCmdReachesItsOwnTree(t *testing.T) {
	isolateRelayHome(t)
	a := newTreeChannel(t)
	got := a.serveOne()
	t.Chdir(t.TempDir())
	if err := runRelayCmd(t, realRelayEnv(a.root), stopPayload(t.TempDir()), "Stop"); err != nil {
		t.Fatalf("project dir tree A: %v", err)
	}
	select {
	case line := <-got:
		if !bytes.HasPrefix(line, []byte(treeTokenHex+" ")) || !bytes.Contains(line, []byte(`"kind":"done"`)) {
			t.Fatalf("tree A's channel got %q, want one token-framed done frame", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("tree A's channel got nothing although CLAUDE_PROJECT_DIR named tree A")
	}
}

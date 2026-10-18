//go:build !windows

package agents

// Purpose: the test rig for the hook relay (a fake driver on a real unix
//   socket, a leased-tree fixture, the captured hook payloads) and the frame
//   check tests for DecodeRelayFrame.
// Constraints: the listener and the accepted connections use raw syscalls: a
//   local unix socket is not network egress, and unit tests may not import
//   "net" (the no-network lane gate). Nothing here touches the operator HOME;
//   every directory is a temp directory removed on cleanup. The session token
//   is an obviously fake constant.
// SPORT: internal.providers.agents/ADD (P1-AGT-14).

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var fakeToken = provider.SessionToken{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

// rig is one leased tree with a callback file and a fake driver listening on
// the socket the file names.
type rig struct {
	t      *testing.T
	root   string
	sock   string
	fd     int
	mu     sync.Mutex
	frames []provider.CallbackPayload
	ack    string
	done   chan struct{}
}

// newRig builds the tree: root/.claude/cascade-callback.json (0600), a
// short socket directory and a listening socket.
func newRig(t *testing.T) *rig {
	t.Helper()
	isolateHome(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("", "hr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	r := &rig{t: t, root: root, sock: filepath.Join(sockDir, "s"), ack: "ok", done: make(chan struct{})}
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	r.listen()
	r.writeCallback(filepath.Join(root, ".claude", provider.CallbackFileName), 0o600)
	return r
}

func (r *rig) listen() {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: r.sock}); err != nil {
		r.t.Skipf("cannot bind a unix socket at %q: %v", r.sock, err)
	}
	if err := syscall.Listen(fd, 32); err != nil {
		r.t.Fatal(err)
	}
	r.fd = fd
	r.t.Cleanup(func() { _ = syscall.Close(fd) })
}

// writeCallback writes the callback file body to path with mode.
func (r *rig) writeCallback(path string, mode os.FileMode) {
	body, _ := json.Marshal(provider.CallbackFile{Socket: r.sock, Token: hex.EncodeToString(fakeToken[:])})
	if err := os.WriteFile(path, body, mode); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		r.t.Fatal(err)
	}
}

// serve answers the next n connections, one frame each, in a goroutine; done
// closes after the last.
func (r *rig) serve(n int) {
	go func() {
		defer close(r.done)
		for i := 0; i < n; i++ {
			nfd, _, err := syscall.Accept(r.fd)
			if err != nil {
				return
			}
			r.answer(nfd)
		}
	}()
}

// answer reads one line from nfd, checks it with DecodeRelayFrame and acks.
func (r *rig) answer(nfd int) {
	defer func() { _ = syscall.Close(nfd) }()
	var line []byte
	buf := make([]byte, 4096)
	for !bytes.Contains(line, []byte("\n")) {
		n, err := syscall.Read(nfd, buf)
		if n <= 0 || err != nil {
			return
		}
		line = append(line, buf[:n]...)
	}
	ack := r.ack
	p, err := DecodeRelayFrame(fakeToken, line)
	if err != nil {
		ack = "refused auth"
	} else {
		r.mu.Lock()
		r.frames = append(r.frames, p)
		r.mu.Unlock()
	}
	_, _ = syscall.Write(nfd, []byte(ack+"\n"))
}

// wait blocks until serve has answered every expected connection.
func (r *rig) wait() {
	r.t.Helper()
	select {
	case <-r.done:
	case <-time.After(20 * time.Second):
		r.t.Fatal("the fake driver did not receive every expected connection")
	}
}

// got returns the frames the driver accepted.
func (r *rig) got() []provider.CallbackPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]provider.CallbackPayload(nil), r.frames...)
}

// connected reports whether any client is waiting on the socket (nothing was
// served, so a sent frame would still be queued).
func (r *rig) connected() bool {
	if err := syscall.SetNonblock(r.fd, true); err != nil {
		r.t.Fatal(err)
	}
	nfd, _, err := syscall.Accept(r.fd)
	if err == nil {
		_ = syscall.Close(nfd)
	}
	return err == nil
}

// hook returns a captured fixture with the capture tree rewritten to this
// rig's root.
func (r *rig) hook(name string) []byte {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude-hooks", name))
	if err != nil {
		r.t.Fatal(err)
	}
	return bytes.ReplaceAll(b, []byte("/work/tree"), []byte(r.root))
}

func goodPayload() provider.CallbackPayload {
	return provider.CallbackPayload{Seq: 1, Kind: "status", ToolName: "Read", Paths: []string{"a.txt"}}
}

func TestHookBridgeFrameWithoutTokenRefused(t *testing.T) {
	good, err := provider.EncodeCallbackFrame(fakeToken, goodPayload())
	if err != nil {
		t.Fatal(err)
	}
	body := good[33:]
	other := fakeToken
	other[0] ^= 0xff
	wrong, _ := provider.EncodeCallbackFrame(other, goodPayload())
	cases := map[string][]byte{
		"payload only":   body,
		"empty line":     nil,
		"wrong token":    wrong,
		"short token":    append([]byte("0102 "), body...),
		"non-hex token":  append([]byte(strings.Repeat("zz", 16)+" "), body...),
		"zero token":     append([]byte(strings.Repeat("00", 16)+" "), body...),
		"token no space": append([]byte(hex.EncodeToString(fakeToken[:])), body...),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := DecodeRelayFrame(fakeToken, line)
			if kindOf(err) != cascade.KindPermissionDenied || !reflectZero(p) {
				t.Fatalf("got payload %+v, err %v: a frame without the right token must be refused before parsing", p, err)
			}
		})
	}
	if p, err := DecodeRelayFrame(fakeToken, good); err != nil || p.ToolName != "Read" || p.Seq != 1 {
		t.Fatalf("a good frame: %+v, %v", p, err)
	}
}

func reflectZero(p provider.CallbackPayload) bool {
	return p.Seq == 0 && p.Kind == "" && p.ToolName == "" && p.Paths == nil
}

func TestDecodeRelayFrameRefusesBadPayloads(t *testing.T) {
	mut := func(f func(*provider.CallbackPayload)) provider.CallbackPayload {
		p := goodPayload()
		f(&p)
		return p
	}
	cases := map[string]provider.CallbackPayload{
		"zero seq":        mut(func(p *provider.CallbackPayload) { p.Seq = 0 }),
		"unknown kind":    mut(func(p *provider.CallbackPayload) { p.Kind = "boom" }),
		"empty tool":      mut(func(p *provider.CallbackPayload) { p.ToolName = "" }),
		"bad tool":        mut(func(p *provider.CallbackPayload) { p.ToolName = "a b" }),
		"negative time":   mut(func(p *provider.CallbackPayload) { p.DurationMS = -1 }),
		"bad hash":        mut(func(p *provider.CallbackPayload) { p.ResultHash = "XYZ" }),
		"absolute path":   mut(func(p *provider.CallbackPayload) { p.Paths = []string{"/etc/passwd"} }),
		"dotdot path":     mut(func(p *provider.CallbackPayload) { p.Paths = []string{"../x"} }),
		"long dedup key":  mut(func(p *provider.CallbackPayload) { p.DedupKey = strings.Repeat("k", 129) }),
		"control in path": mut(func(p *provider.CallbackPayload) { p.Paths = []string{"a\nb"} }),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			line, err := provider.EncodeCallbackFrame(fakeToken, p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeRelayFrame(fakeToken, line); kindOf(err) != cascade.KindInvalidInput {
				t.Fatalf("got %v, want an invalid-input refusal", err)
			}
		})
	}
	extra := append([]byte(hex.EncodeToString(fakeToken[:])+` {"seq":1,"kind":"status","tool_name":"Read","paths":[],"surprise":1}`), '\n')
	if _, err := DecodeRelayFrame(fakeToken, extra); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unknown field must be refused, got %v", err)
	}
	trailing := append([]byte(hex.EncodeToString(fakeToken[:])+` {"seq":1,"kind":"status","tool_name":"Read","paths":[]} {}`), '\n')
	if _, err := DecodeRelayFrame(fakeToken, trailing); err == nil {
		t.Fatal("trailing JSON after the payload must be refused")
	}
}

// TestFramePathRejectsInvalidUTF8 proves a path with a byte the JSON round
// trip would rewrite is dropped on its own, so the frame keeps its other
// fields instead of failing the relay's self-check.
func TestFramePathRejectsInvalidUTF8(t *testing.T) {
	cases := map[string]bool{
		"pkg/main.go":             true,
		"dir/é/ü.txt":             true,
		"dir/\xff/file.txt":       false,
		strings.Repeat("\xff", 8): false,
	}
	for path, want := range cases {
		if got := validFramePath(path); got != want {
			t.Errorf("validFramePath(%q) = %t, want %t", path, got, want)
		}
	}
}

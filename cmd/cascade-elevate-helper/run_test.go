//go:build !windows

// Purpose: tests for the descriptor handling of run: the gate that names the
//
//	listener cascade owns, and the hand-run fallback that serves status only.
//
// SPORT: cmd/cascade-elevate-helper run tests/ADDED (P1-SHIP-15).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func socketPair(t *testing.T) (client, server *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	return os.NewFile(uintptr(fds[0]), "client"), os.NewFile(uintptr(fds[1]), "server")
}

// unixListener opens a unix listener on a short path (macOS limits socket
// paths) with raw syscalls: unit tests may not import net. accept blocks for
// one connection and returns nil once the listener is closed.
func unixListener(t *testing.T) (accept func() *os.File, path string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ce")
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "s")
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 4); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd); _ = os.RemoveAll(dir) })
	return func() *os.File {
		nfd, _, err := syscall.Accept(fd)
		if err != nil {
			return nil
		}
		return os.NewFile(uintptr(nfd), "conn")
	}, path
}

// unixDial connects to path with raw syscalls.
func unixDial(t *testing.T, path string) *os.File {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Connect(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), "client")
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func allowBind(int) (func() error, error) { return allowCaller, nil }

func TestRunServesThroughTheListenerTheGateNames(t *testing.T) {
	accept, path := unixListener(t)
	gateClient, gate := socketPair(t)
	defer func() { _ = gateClient.Close() }()
	reply := make(chan string, 1)
	go func() {
		c := accept()
		if c == nil {
			reply <- "accept failed"
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = c.Write([]byte(`{"v":1,"op":"pubkey"}` + "\n"))
		line, _ := bufio.NewReader(c).ReadString('\n')
		reply <- line
	}()
	if _, err := gateClient.WriteString(path + "\n"); err != nil {
		t.Fatal(err)
	}
	if rc := runGate(gate, strings.NewReader(""), io.Discard, server{be: newFake()}, allowBind); rc != 0 {
		t.Fatalf("exit %d", rc)
	}
	if line := <-reply; !strings.Contains(line, `"ok":true`) {
		t.Fatalf("reply %q", line)
	}
}

func TestRunGateRefusesBadGatesAndBinds(t *testing.T) {
	for name, line := range map[string]string{
		"relative path": "relative/socket",
		"long path":     "/" + strings.Repeat("a", maxSockPath),
		"nothing there": "/nonexistent-dir-for-the-helper-test/s",
		"empty":         "",
	} {
		t.Run(name, func(t *testing.T) {
			gateClient, gate := socketPair(t)
			defer func() { _ = gateClient.Close() }()
			_, _ = gateClient.WriteString(line + "\n")
			fb := newFake()
			if rc := runGate(gate, strings.NewReader(""), io.Discard, server{be: fb}, allowBind); rc != 1 || len(fb.calls) != 0 {
				t.Fatalf("exit %d calls %v", rc, fb.calls)
			}
		})
	}
	accept, path := unixListener(t)
	go func() {
		if c := accept(); c != nil {
			_ = c.Close()
		}
	}()
	gateClient, gate := socketPair(t)
	defer func() { _ = gateClient.Close() }()
	_, _ = gateClient.WriteString(path + "\n")
	failBind := func(int) (func() error, error) { return nil, errors.New("no peer identity") }
	if rc := runGate(gate, strings.NewReader(""), io.Discard, server{be: newFake()}, failBind); rc != 1 {
		t.Fatalf("a failed caller binding must not serve: exit %d", rc)
	}
}

func TestRunFallbackServesStatusOnly(t *testing.T) {
	closed := os.NewFile(uintptr(1<<20), "not-open")
	for op, wantOK := range map[string]bool{"status": true, "pubkey": false, "sign": false, "delete": false, "generate": false} {
		var out bytes.Buffer
		line := `{"v":1,"op":"` + op + `","payload_b64":"AAAA"}`
		if op != "sign" {
			line = `{"v":1,"op":"` + op + `"}`
		}
		fb := newFake()
		rc := runGate(closed, strings.NewReader(line+"\n"), &out, server{be: fb, caller: allowCaller}, allowBind)
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil || rc != 0 {
			t.Fatalf("%s: rc=%d out=%q err=%v", op, rc, out.String(), err)
		}
		if wantOK && m["ok"] != true {
			t.Fatalf("%s: status must answer: %v", op, m)
		}
		if !wantOK && (m["ok"] != false || m["kind"] != "permission-denied") {
			t.Fatalf("%s: want an explicit permission-denied refusal, got %v", op, m)
		}
		if !wantOK && len(fb.calls) != 0 {
			t.Fatalf("%s reached the keystore without a verified peer: %v", op, fb.calls)
		}
	}
}

func TestRunExitsNonZeroWithoutARequest(t *testing.T) {
	if rc := runGate(nil, strings.NewReader(""), io.Discard, server{be: newFake(), caller: allowCaller}, allowBind); rc != 1 {
		t.Fatalf("exit %d, want 1", rc)
	}
}

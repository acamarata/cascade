//go:build !windows && integration

package agents

// Purpose: the credential is the process that called connect(), not the one that
// writes (the SCM_RIGHTS case of the review list).
// Constraints: see peer_guard_integration_test.go.

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// passListener starts reapchild's "pass" mode against srv: the child connects to
// the daemon socket, sends the connected descriptor to the returned listener and
// stays alive until the returned done file exists. The child is live under marker.
func passListener(t *testing.T, bin string, srv *ownerServer, marker string) (net.Listener, string, *exec.Cmd) {
	t.Helper()
	dir, err := os.MkdirTemp("", "agpass")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	passSock, done := filepath.Join(dir, "p.sock"), filepath.Join(dir, "done")
	ln, err := net.Listen("unix", passSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cmd := exec.Command(bin, "pass", srv.sock, passSock, done)
	cmd.Env = childEnv(t, marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(done, nil, 0o600); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addLive(LiveSpawn{Marker: marker, PGID: cmd.Process.Pid, PID: cmd.Process.Pid})
	return ln, done, cmd
}

// receiveConn accepts the child's connection to the pass listener and returns
// the daemon connection it sent (SCM_RIGHTS).
func receiveConn(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()
	pc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	oob := make([]byte, syscall.CmsgSpace(4))
	_, oobn, _, _, err := pc.(*net.UnixConn).ReadMsgUnix(make([]byte, 1), oob)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		t.Fatalf("control messages: %v %v", msgs, err)
	}
	fds, err := syscall.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		t.Fatalf("rights: %v %v", fds, err)
	}
	f := os.NewFile(uintptr(fds[0]), "passed")
	defer func() { _ = f.Close() }()
	conn, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// ownerRequest is one owner-CLI call written by hand.
func ownerRequest() string {
	body := `{"jsonrpc":"2.0","id":1,"method":"` + ownerVerb + `"}`
	return "POST /rpc HTTP/1.1\r\nHost: unix\r\nContent-Type: application/json\r\nCascade-Client: owner-cli\r\nConnection: close\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

// TestConnectionJudgedByItsConnector proves the credential is the process that
// called connect(): an agent child connects and passes the descriptor to the
// test process (SCM_RIGHTS), which is outside every live tree and writes the
// owner-CLI request itself. The daemon still refuses it, because the peer is the
// connector, not the writer.
func TestConnectionJudgedByItsConnector(t *testing.T) {
	bin := reapChildBin(t)
	srv := startOwnerServer(t)
	useLive(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	ln, _, _ := passListener(t, bin, srv, marker)
	waitAccepted(t, srv) // the daemon read the peer pid before this process imported the descriptor
	conn := receiveConn(t, ln)
	defer func() { _ = conn.Close() }()
	wantRefused(t, "owner-CLI request written by a non-agent on an agent's connection", sendOwnerRequest(t, conn))
	if srv.count(ownerVerb) != 0 {
		t.Error("the request written on an agent's connection reached the handler")
	}
}

// TestConnectionFromAnExitedConnectorRefused: the agent that connected has
// exited and been reaped before the request arrives, so the pid the daemon
// recorded names no process (or, once recycled, another one). The request is
// refused, never judged by whatever holds that pid now.
func TestConnectionFromAnExitedConnectorRefused(t *testing.T) {
	bin := reapChildBin(t)
	srv := startOwnerServer(t)
	useLive(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	ln, done, cmd := passListener(t, bin, srv, marker)
	waitAccepted(t, srv) // the daemon recorded the connector before this process imported the descriptor
	conn := receiveConn(t, ln)
	defer func() { _ = conn.Close() }()
	if err := os.WriteFile(done, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("connector did not exit cleanly: %v", err)
	}
	wantRefused(t, "owner-CLI request on a connection whose connector has exited", sendOwnerRequest(t, conn))
	if srv.count(ownerVerb) != 0 {
		t.Error("the request from an exited connector reached the handler")
	}
}

// waitAccepted fails the test unless the daemon has accepted a connection.
func waitAccepted(t *testing.T, srv *ownerServer) {
	t.Helper()
	select {
	case <-srv.accepted:
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon never accepted the connection")
	}
}

// sendOwnerRequest writes one owner-CLI call on conn and reads the answer.
func sendOwnerRequest(t *testing.T, conn net.Conn) reply {
	t.Helper()
	if _, err := conn.Write([]byte(ownerRequest())); err != nil {
		t.Fatal(err)
	}
	return readReply(t, conn)
}

// readReply reads one HTTP answer from conn.
func readReply(t *testing.T, conn net.Conn) reply {
	t.Helper()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{status: resp.StatusCode, body: string(body)}
}

// TestConnectionWrittenBeforeAcceptIsJudgedPerOS pins what the kernel reports
// when a descriptor an agent opened is written by a non-agent before the daemon
// accepts. linux (SO_PEERCRED) always names the process that called connect(),
// so the request is refused. macOS (LOCAL_PEERPID) names the last process to do
// I/O on the connection, here the non-agent writer, so the request is judged as
// that process. That needs a non-agent to act on an agent's connection; it gives
// the agent nothing it could not get by asking that process, and is disclosed.
func TestConnectionWrittenBeforeAcceptIsJudgedPerOS(t *testing.T) {
	bin := reapChildBin(t)
	gate := make(chan struct{})
	srv := startGatedOwnerServer(t, gate)
	useLive(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	ln, _, _ := passListener(t, bin, srv, marker)
	conn := receiveConn(t, ln)
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(ownerRequest())); err != nil {
		t.Fatal(err)
	}
	close(gate)
	waitAccepted(t, srv)
	got := readReply(t, conn)
	if runtime.GOOS != "darwin" {
		wantRefused(t, "linux: the connector is judged", got)
		return
	}
	if got.status != 200 || srv.count(ownerVerb) != 1 {
		t.Errorf("darwin: status %d served %d body %q, want the writer (this process) judged and admitted", got.status, srv.count(ownerVerb), got.body)
	}
	if pids := seenPIDs(); len(pids) != 1 || pids[0] != os.Getpid() {
		t.Errorf("darwin: the guard judged %v, want only the writer %d", pids, os.Getpid())
	}
}

// TestLeakedDescriptorIsJudgedPerOS: a non-agent process connected and the
// descriptor reached an agent child (an owner leaking it), which writes the
// owner-CLI call. macOS asks the kernel again for every request and so judges the
// agent that wrote it: refused. linux names the connector, the non-agent, and
// admits it: the disclosed leaked-descriptor residual.
func TestLeakedDescriptorIsJudgedPerOS(t *testing.T) {
	bin := reapChildBin(t)
	srv := startOwnerServer(t)
	useLive(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", srv.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	waitAccepted(t, srv)
	f, err := c.(*net.UnixConn).File()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got := writeOnInherited(t, bin, f, marker)
	if runtime.GOOS == "darwin" {
		wantRefused(t, "darwin: the agent that wrote the request is judged", got)
		return
	}
	if got.status != 200 || srv.count(ownerVerb) != 1 {
		t.Errorf("linux: status %d served %d body %q, want the leaked-descriptor residual (the connector, a non-agent, admitted)", got.status, srv.count(ownerVerb), got.body)
	}
}

// writeOnInherited runs an agent child (live under marker) that writes one
// owner-CLI call on f, a connected socket it inherits as fd 3, and returns what
// it recorded.
func writeOnInherited(t *testing.T, bin string, f *os.File, marker string) reply {
	t.Helper()
	dir := t.TempDir()
	out, gofile := filepath.Join(dir, "out"), filepath.Join(dir, "go")
	cmd := exec.Command(bin, "write-fd3", out, ownerVerb, gofile)
	cmd.Env = childEnv(t, marker)
	cmd.SysProcAttr = inOwnGroup
	cmd.ExtraFiles = []*os.File{f}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addLive(leaderLive(marker)(cmd.Process.Pid))
	if err := os.WriteFile(gofile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("write-fd3: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ := strings.Cut(string(b), "\n")
	code, _ := strconv.Atoi(status)
	return reply{status: code, body: body}
}

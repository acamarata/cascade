// Purpose: a test-only, deliberately UNSIGNED stand-in for cascade, for the
//
//	real-helper refusal leg: it talks to a Developer-ID-signed
//	cascade-elevate-helper the way cascade does (gate socketpair on fd 3, a
//	listener the helper connects to) and prints each reply, so the leg can
//	show that pubkey, sign, generate and delete are all answered
//	permission-denied while status still answers, and that an exec after the
//	listener was created does not borrow the new binary's signature.
//
// Inputs: `callerprobe direct <helper>` sends status, pubkey, sign, generate
//
//	and delete in turn (one helper process each); `callerprobe race <helper>
//	<signed-copy-of-this-binary>` creates the listener, starts the helper,
//	writes the request on the helper's connection and THEN execs the signed
//	copy, which reads the reply (`relay`).
//
// Outputs: one line per request on stdout: `<op> <reply line>`.
//
// Constraints: never touches the Keychain itself; it does NOT verify the
//
//	helper (it is a probe, not cascade); the sign request carries a fixed fake
//	payload. Not built into any release artefact.
//
// SPORT: internal/elevation testdata callerprobe/ADDED (P1-SHIP-15).
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

var requests = []struct{ op, line string }{
	{"status", `{"v":1,"op":"status"}`},
	{"pubkey", `{"v":1,"op":"pubkey"}`},
	{"sign", `{"v":1,"op":"sign","payload_b64":"ZmFrZSBwYXlsb2Fk"}`},
	{"generate", `{"v":1,"op":"generate"}`},
	{"delete", `{"v":1,"op":"delete"}`},
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: callerprobe direct <helper> | race <helper> <signed-probe> | relay <connection-fd>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "direct":
		for _, r := range requests {
			fmt.Println(r.op, exchange(os.Args[2], r.line))
		}
	case "race":
		race(os.Args[2], os.Args[3])
	case "relay":
		fd, _ := strconv.Atoi(os.Args[2])
		reply, err := bufio.NewReader(os.NewFile(uintptr(fd), "conn")).ReadString('\n')
		if err != nil {
			fmt.Println("relay no-reply:", err)
			os.Exit(1)
		}
		fmt.Print("relay ", reply)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// start listens in a fresh private directory and starts the helper with the
// gate; the helper is told where the listener is.
func start(helper string) (ln net.Listener, cleanup func()) {
	dir, err := os.MkdirTemp("", "callerprobe-")
	if err != nil {
		fatal(err)
	}
	ln, err = net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		fatal(err)
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		fatal(err)
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	gate, child := os.NewFile(uintptr(fds[0]), "gate"), os.NewFile(uintptr(fds[1]), "child")
	cmd := exec.Command(helper)
	cmd.Env = []string{}
	cmd.ExtraFiles = []*os.File{child}
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	_ = child.Close()
	_, _ = gate.WriteString(ln.Addr().String() + "\n")
	return ln, func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = gate.Close()
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	}
}

// talk accepts the helper's connection, sends one request and reads the reply.
func talk(ln net.Listener, line string) string {
	if ul, ok := ln.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Now().Add(15 * time.Second))
	}
	conn, err := ln.Accept()
	if err != nil {
		return "no-connection: " + err.Error()
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	_, _ = conn.Write([]byte(line + "\n"))
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "no-reply: " + err.Error()
	}
	return reply[:len(reply)-1]
}

func exchange(helper, line string) string {
	ln, cleanup := start(helper)
	defer cleanup()
	return talk(ln, line)
}

// race listens as this (unsigned) process, starts the helper, accepts its
// connection and WRITES the request, and only then replaces the process image
// with the signed copy, which reads the reply on the inherited connection.
func race(helper, signed string) {
	ln, _ := start(helper)
	if ul, ok := ln.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Now().Add(15 * time.Second))
	}
	conn, err := ln.Accept()
	if err != nil {
		fatal(err)
	}
	if _, err := conn.Write([]byte(requests[2].line + "\n")); err != nil {
		fatal(err)
	}
	f, err := conn.(*net.UnixConn).File()
	if err != nil {
		fatal(err)
	}
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETFD, 0); e != 0 {
		fatal(e)
	}
	err = syscall.Exec(signed, []string{signed, "relay", strconv.Itoa(int(f.Fd()))}, []string{})
	fatal(err)
}

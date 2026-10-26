//go:build !windows

// Command reapchild is the test-only process the lease-sweep tests spawn. It
// inherits its environment untouched (so it carries whatever CASCADE_LEASE_ID
// the test set) and then behaves as its first argument says:
//
//	sleep <pidfile>    write own pid to pidfile, then sleep until killed
//	fork  <pidfile>    start a "sleep" copy of itself in a new session
//	                   (setsid), then exit, leaving that copy an orphan
//	scrub <pidfile>    like sleep, but re-exec with an empty environment first
//	print-env <name>   print the value of one environment variable and exit
//	dial <sock> <out> <mode> <method> <gofile>
//	                   wait until gofile exists, POST one JSON-RPC call for
//	                   method to the unix socket sock and write "<status>\n<body>"
//	                   to out. mode "owner" sends the owner-CLI marker header,
//	                   "token:<t>" the principal header, "both:<t>" both.
//	pass <sock> <passsock> <donefile>
//	                   connect to the daemon socket sock, send that connected
//	                   descriptor to the listener at passsock (SCM_RIGHTS), and
//	                   stay alive until donefile exists
//	write-fd3 <out> <method> <gofile>
//	                   wait until gofile exists, then write one owner-CLI call on
//	                   the connected unix socket inherited as file descriptor 3
//	                   (cmd.ExtraFiles[0]) and record "<status>\n<body>" in out
//	setsid-dial <same arguments>
//	                   run dial in a child in a new session whose environment
//	                   no longer holds CASCADE_LEASE_ID, and wait for it: the
//	                   child is a scrubbed, setsid grandchild of the caller's
//	                   process that is still in its ppid chain.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sleep":
		writePid(os.Args[2])
		time.Sleep(10 * time.Minute)
	case "fork":
		cmd := exec.Command(os.Args[0], "sleep", os.Args[2])
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_ = cmd.Process.Release()
	case "scrub":
		cmd := exec.Command(os.Args[0], "sleep", os.Args[2])
		cmd.Env = []string{}
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_ = cmd.Process.Release()
	case "print-env":
		fmt.Println(os.Getenv(os.Args[2]))
	case "dial":
		if len(os.Args) != 7 {
			os.Exit(2)
		}
		dial(os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6])
	case "write-fd3":
		if len(os.Args) != 5 {
			os.Exit(2)
		}
		writeFD3(os.Args[2], os.Args[3], os.Args[4])
	case "pass":
		if len(os.Args) != 5 {
			os.Exit(2)
		}
		passConn(os.Args[2], os.Args[3], os.Args[4])
	case "setsid-dial":
		if len(os.Args) != 7 {
			os.Exit(2)
		}
		setsidDial(os.Args[2:])
	default:
		os.Exit(2)
	}
}

// setsidDial runs the dial mode in a child that has no lease marker and is in a
// session of its own, and waits for it.
func setsidDial(args []string) {
	cmd := exec.Command(os.Args[0], append([]string{"dial"}, args...)...)
	cmd.Env = withoutLease(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Run(); err != nil {
		os.Exit(3)
	}
}

// writePid records this process's pid atomically, so a poller never reads a
// partial number.
func writePid(path string) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(4)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Exit(4)
	}
}

// withoutLease returns env minus any CASCADE_LEASE_ID entry.
func withoutLease(env []string) []string {
	var out []string
	for _, e := range env {
		if !strings.HasPrefix(e, "CASCADE_LEASE_ID=") {
			out = append(out, e)
		}
	}
	return out
}

// dial waits for gofile, sends one JSON-RPC call over the unix socket and
// records the HTTP status and body in out (atomically).
func dial(sock, out, mode, method, gofile string) {
	waitFor(gofile)
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"}`
	req, err := http.NewRequest(http.MethodPost, "http://unix/rpc", strings.NewReader(body))
	if err != nil {
		os.Exit(6)
	}
	req.Header.Set("Content-Type", "application/json")
	switch kind, token, _ := strings.Cut(mode, ":"); kind {
	case "owner":
		req.Header.Set("Cascade-Client", "owner-cli")
	case "token":
		req.Header.Set("Cascade-Principal", token)
	case "both":
		req.Header.Set("Cascade-Client", "owner-cli")
		req.Header.Set("Cascade-Principal", token)
	default:
		os.Exit(2)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := client.Do(req)
	if err != nil {
		os.Exit(7)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		os.Exit(7)
	}
	record(out, strconv.Itoa(resp.StatusCode)+"\n"+string(got))
}

// passConn connects to sock and hands the connected descriptor to the unix
// listener at passsock, then waits for donefile so this process (the
// connection's connector) stays alive while the receiver uses the connection.
func passConn(sock, passsock, donefile string) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		os.Exit(7)
	}
	f, err := c.(*net.UnixConn).File()
	if err != nil {
		os.Exit(7)
	}
	p, err := net.Dial("unix", passsock)
	if err != nil {
		os.Exit(7)
	}
	if _, _, err := p.(*net.UnixConn).WriteMsgUnix([]byte("x"), syscall.UnixRights(int(f.Fd())), nil); err != nil {
		os.Exit(7)
	}
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(donefile); err == nil {
			return
		}
	}
	os.Exit(5)
}

// writeFD3 waits for gofile, writes one owner-CLI call for method on the
// connection inherited as fd 3 and records the HTTP status and body in out.
func writeFD3(out, method, gofile string) {
	waitFor(gofile)
	conn, err := net.FileConn(os.NewFile(3, "inherited"))
	if err != nil {
		os.Exit(7)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"}`
	req := "POST /rpc HTTP/1.1\r\nHost: unix\r\nContent-Type: application/json\r\nCascade-Client: owner-cli\r\nConnection: close\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body
	if _, err := conn.Write([]byte(req)); err != nil {
		os.Exit(7)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		os.Exit(7)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		os.Exit(7)
	}
	record(out, strconv.Itoa(resp.StatusCode)+"\n"+string(got))
}

// waitFor blocks until path exists or 30 seconds pass.
func waitFor(path string) {
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			os.Exit(5)
		}
	}
}

// record writes text to path atomically.
func record(path, text string) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		os.Exit(4)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Exit(4)
	}
}

// Purpose: cascade-elevate-helper, the cgo darwin elevation helper. A release
//
//	cascade binary is built with CGO_ENABLED=0 and cannot reach the Keychain
//	or LocalAuthentication; this binary does, for exactly one JSON request
//	per invocation (see protocol.go).
//
// Inputs: fd 3 is one end of a unix socketpair created by cascade (stdin is
//
//	/dev/null). It is only a gate: the first line on it is the absolute path
//	of a unix socket cascade listens on. The helper connects there and serves
//	the request on that connection, so the channel is the connection cascade
//	accepted from this process, not the inherited descriptor. When fd 3 is
//	not open (a hand run) the helper reads stdin and writes stdout, but then
//	only the status op is served: every key op needs a verified peer.
//
// Outputs: one response line on the connection, exit 0 (also for a refused
//
//	request: the refusal is in the line); exit 1 only when no line could be
//	exchanged at all.
//
// Constraints: no flags, no environment variables and no PATH lookup; no
//
//	network; the only stdout write is the protocol line in the hand run mode,
//
//	made through internal/output.
//
// SPORT: cmd/cascade-elevate-helper main/ADDED (P1-SHIP-15).
package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	gateFD      = 3
	dialTimeout = 5 * time.Second
	maxSockPath = 100
)

func main() {
	os.Exit(run(os.Stdin))
}

// lineWriter prints each reply line through the sanctioned output seam.
type lineWriter struct{ w *output.Writer }

func (l lineWriter) Write(p []byte) (int, error) {
	l.w.Println(strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

// run serves one request on the real descriptors; the hand-run reply goes
// through internal/output, the only sanctioned path to the real stdout.
func run(stdin io.Reader) int {
	out := lineWriter{output.NewDefault(false, false, false, true)}
	return runGate(os.NewFile(gateFD, "helper-gate"), stdin, out, server{be: newBackend()}, bindCaller)
}

// runGate serves one request. When gate is an open descriptor, the helper
// connects to the listener it names and serves there, with the caller check
// bound to that connection; otherwise stdin/stdout serve status only (every
// key op is refused because there is no verified peer).
func runGate(gate *os.File, stdin io.Reader, stdout io.Writer, srv server, bind func(fd int) (func() error, error)) int {
	if gate != nil {
		if _, err := gate.Stat(); err == nil {
			defer func() { _ = gate.Close() }()
			return exitCode(serveGate(gate, srv, bind))
		}
	}
	srv.caller = func() error {
		return cascade.New(cascade.KindPermissionDenied, "caller is not signed by this helper's team")
	}
	return exitCode(srv.serve(stdin, stdout))
}

func serveGate(gate io.Reader, srv server, bind func(fd int) (func() error, error)) error {
	line, err := readLine(gate)
	if err != nil {
		return err
	}
	path := string(line)
	if !filepath.IsAbs(path) || len(path) > maxSockPath {
		return cascade.New(cascade.KindInvalidInput, "the gate did not name an absolute socket path")
	}
	c, err := net.DialTimeout("unix", path, dialTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	sc, ok := c.(syscall.Conn)
	if !ok {
		return cascade.New(cascade.KindInvalidInput, "the connection has no descriptor to identify the peer by")
	}
	fd, err := connFD(sc)
	if err != nil {
		return err
	}
	if srv.caller, err = bind(fd); err != nil {
		return err
	}
	return srv.serve(c, c)
}

// connFD returns the descriptor of c, which stays valid while c is open.
func connFD(c syscall.Conn) (int, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	fd := -1
	if err := rc.Control(func(f uintptr) { fd = int(f) }); err != nil {
		return -1, err
	}
	return fd, nil
}

func exitCode(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

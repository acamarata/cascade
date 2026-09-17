//go:build darwin || linux

// Purpose: tier 2's real pseudo-terminal, on the two platforms
//
//	06-FORGE-SPEC §2 scopes it to.
//
// WHY creack/pty. R-16.54 named it: MIT, pure Go syscalls, no cgo, which
//
//	is what keeps the core CGO-free (repo hard rule 2). The build tag is
//	darwin||linux rather than !windows so a platform nobody has tested
//	this on gets the honest refusal in tier2_other.go instead of a build
//	error or an untested attach.
//
// Constraints: Open returns the controller side and the terminal side of
//
//	one pseudo-terminal. The caller writes the question to the controller
//	side and reads the answer from it; Close releases both, and closing
//	the controller side is what ends a blocked read on the other.
//
// SPORT: fleet.supervision.Tier2Supervisor/ADDED (P1-E18-W4-S39-T3).

package supervision

import (
	"context"
	"os"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/acamarata/cascade/pkg/cascade"
)

// unixAttacher opens a real OS pseudo-terminal.
type unixAttacher struct{}

// NewPTYAttacher returns the platform's pseudo-terminal attacher.
func NewPTYAttacher() PTYAttacher { return unixAttacher{} }

// Attach opens a pseudo-terminal pair and returns the controller side as
// the session's streams.
//
// A failure here is KindUnavailable rather than KindUnsupported: this
// platform DOES have pseudo-terminals, so a failed open means the host ran
// out of them or the process was not permitted one — both conditions an
// operator can act on, and neither the same as "this platform cannot".
func (unixAttacher) Attach(context.Context) (*Session, error) {
	controller, terminal, err := pty.Open()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err,
			"supervision: tier 2 could not open a pseudo-terminal")
	}
	if err := disableEcho(terminal); err != nil {
		_ = terminal.Close()
		_ = controller.Close()
		return nil, err
	}
	return &Session{
		In:  controller,
		Out: controller,
		Close: func() error {
			// The terminal side is closed first: closing it is what ends a
			// read blocked on the controller side, so the order here is
			// what makes Close actually release a waiting prompt rather
			// than leaving it blocked on a half-open pair.
			terminalErr := terminal.Close()
			controllerErr := controller.Close()
			return firstErr(terminalErr, controllerErr)
		},
	}, nil
}

// firstErr returns the first non-nil error, so Close reports a real
// failure rather than only the last one.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// disableEcho turns the line discipline's echo off on the terminal side.
//
// WITHOUT THIS THE SUPERVISOR READS ITS OWN QUESTION AS THE ANSWER. A
// fresh pseudo-terminal echoes: everything written to the controller side
// arrives as input on the terminal side, and the line discipline sends it
// straight back out to the controller — where the supervisor is waiting
// for a human's answer. The first line it would read is its own prompt,
// and a prompt is not a "yes", so the action would be denied for a reason
// that has nothing to do with what anybody decided.
//
// It is also a RACE when left on: whether the echo or the real answer
// arrives first decides the outcome, which is how this surfaced — green
// under `go test` and red under `-race`, the same run either way.
//
// Echo is the terminal's job when a person is typing at a real one. Here
// there is no terminal emulator in the loop, so the discipline's echo is
// pure feedback into our own read.
func disableEcho(terminal *os.File) error {
	fd := int(terminal.Fd())
	attrs, err := unix.IoctlGetTermios(fd, echoGetRequest)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err,
			"supervision: tier 2 could not read the pseudo-terminal's mode")
	}
	attrs.Lflag &^= unix.ECHO | unix.ECHOE | unix.ECHOK | unix.ECHONL
	if err := unix.IoctlSetTermios(fd, echoSetRequest, attrs); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err,
			"supervision: tier 2 could not set the pseudo-terminal's mode")
	}
	return nil
}

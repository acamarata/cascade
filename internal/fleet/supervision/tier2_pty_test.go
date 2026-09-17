//go:build darwin || linux

package supervision

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Purpose (this file): the REAL pseudo-terminal. Everything in
//   tier2_test.go drives the decision half over in-memory streams, which
//   proves the logic and proves nothing about whether a terminal can
//   actually be opened, written to and read from on this host.
//
// Art.2, applied honestly: there is no recorded fixture here and there
//   cannot be one — a pseudo-terminal is a live OS object, so this test
//   exercises the real counterpart directly rather than replaying bytes
//   captured from it. Provenance is in testdata/README.md.
//
// HOW THE TWO SIDES ARE USED, because getting this backwards is the easy
//   mistake: pty.Open returns the CONTROLLER side and the TERMINAL side of
//   one device. The supervisor holds the controller side — it writes the
//   question there and reads the answer there. The test plays the PERSON,
//   which means writing to the TERMINAL side. A test that wrote the answer
//   to the controller side would be talking to itself, and the "denied"
//   case would pass for the wrong reason: no answer read is also not a yes.
//
// Build-tagged darwin||linux: 06-FORGE-SPEC §2 scopes tier 2 to the
//   platforms that have this, and tier2_windows_test.go asserts the
//   refusal on the one that does not.
// SPORT: fleet.supervision tier-2 real-PTY test (ADD) — P1-E18-W4-S39-T3.

// realTerminal opens a real pseudo-terminal and returns a Session over the
// controller side plus the terminal side the test types on.
func realTerminal(t *testing.T) (*Session, *os.File) {
	t.Helper()
	controller, terminal, err := pty.Open()
	if err != nil {
		t.Skipf("no pseudo-terminal available on this host: %v", err)
	}
	// The SAME echo-disable the production attacher applies. Without it
	// the line discipline sends the supervisor's own question straight
	// back to it and the first line it reads is the prompt, not the
	// answer — see disableEcho's comment in tier2_unix.go.
	if err := disableEcho(terminal); err != nil {
		t.Skipf("cannot set the pseudo-terminal's mode on this host: %v", err)
	}
	t.Cleanup(func() {
		_ = terminal.Close()
		_ = controller.Close()
	})
	return &Session{In: controller, Out: controller, Close: func() error { return nil }}, terminal
}

// TestTier2RealPTY drives one approval through an actual OS
// pseudo-terminal: the question goes out over the device and the answer
// comes back over it.
func TestTier2RealPTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, err := NewTier2Supervisor(NewPTYAttacher(), noEnv, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess, person := realTerminal(t)

	// The person answers. Written from a goroutine because the device's
	// buffer is finite and Approve is writing the question while this
	// waits to be read.
	typed := make(chan error, 1)
	go func() {
		_, werr := person.Write([]byte("y\n"))
		typed <- werr
	}()

	if err := s.Approve(ctx, sess, tier2Request(), held()); err != nil {
		t.Fatalf("a yes typed on a real terminal did not approve: %v", err)
	}
	if err := <-typed; err != nil {
		t.Fatalf("typing on the real terminal: %v", err)
	}
}

// TestTier2RealPTYDeniesOnNo is the other direction over the same real
// device. It matters that this is a real "no" and not merely an absent
// answer: the helper above hands the test the terminal side precisely so
// this case can be distinguished from a silent one.
func TestTier2RealPTYDeniesOnNo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, err := NewTier2Supervisor(NewPTYAttacher(), noEnv, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess, person := realTerminal(t)
	go func() { _, _ = person.Write([]byte("n\n")) }()

	err = s.Approve(ctx, sess, tier2Request(), held())
	if err == nil {
		t.Fatal("a no typed on a real terminal approved the action")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("err = %q, want a denial", err)
	}
}

// TestTheAttacherOpensARealDevice holds the production attacher itself to
// the same standard: NewPTYAttacher must return something that actually
// opens a device on this platform, not a value that only fails later.
func TestTheAttacherOpensARealDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess, err := NewPTYAttacher().Attach(ctx)
	if err != nil {
		t.Skipf("no pseudo-terminal available on this host: %v", err)
	}
	if sess == nil || sess.In == nil || sess.Out == nil || sess.Close == nil {
		t.Fatalf("Attach returned an incomplete session: %+v", sess)
	}
	if _, err := sess.Out.Write([]byte("probe\n")); err != nil {
		t.Errorf("writing to the attached device: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Errorf("closing the attached device: %v", err)
	}
}

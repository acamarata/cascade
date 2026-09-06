//go:build linux

// Purpose: the Linux clipboardOps: `xclip -selection clipboard -i` over a
//
//	stdin pipe, with the same two-step clear as darwin. When xclip is
//	absent or exits non-zero, Write fails closed with
//	ErrClipboardUnavailable (R-14.24) — no alternative delivery path.
//
// Inputs: a payload to place on the clipboard.
// Outputs: newClipboardOps (build-tag selected), satisfying clipboardOps.
// Constraints: fail closed on any xclip failure, including "not found".
//
//	Wayland (wl-copy) support is DEF-P2-wayland-clipboard, an explicit P2
//	deferral already recorded in phase/deferrals.yaml; this file adds no
//	row there. Art.2 external-contract: this runs the real xclip binary.
//
// SPORT: internal/secrets clipboard_linux.go/ADDED (P1-E08-W2-S16-T4).

package secrets

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// xclipBin is the real Linux X11 clipboard tool. Wayland's wl-copy is out
// of scope per DEF-P2-wayland-clipboard.
const xclipBin = "xclip"

// linuxClipboardOps runs xclip as a subprocess. runCmd is overridable in
// tests; clipboard_linux_test.go additionally runs a real-xclip case and
// an absent-xclip case per the ticket's acceptance criteria.
type linuxClipboardOps struct {
	bin    string
	runCmd func(ctx context.Context, bin string, stdin []byte) error
}

func newClipboardOps() (clipboardOps, error) {
	return &linuxClipboardOps{bin: xclipBin, runCmd: runXclip}, nil
}

func (l *linuxClipboardOps) platform() string { return "linux" }

func (l *linuxClipboardOps) setValue(ctx context.Context, payload []byte) error {
	if err := l.runCmd(ctx, l.bin, payload); err != nil {
		return ErrClipboardUnavailable
	}
	return nil
}

// clearValue performs the two-step clear, same pattern as darwin: both
// steps run regardless of the first's outcome, since the AUDIT CONTRACT
// records the attempt "regardless of subprocess error status".
func (l *linuxClipboardOps) clearValue(ctx context.Context) error {
	err1 := l.runCmd(ctx, l.bin, []byte(" "))
	err2 := l.runCmd(ctx, l.bin, []byte{})
	if err1 != nil || err2 != nil {
		return ErrClipboardUnavailable
	}
	return nil
}

// runXclip is the production runCmd. exec.LookPath first, distinctly,
// because "not found" and "found but exited non-zero" both fail closed
// the same way (R-14.24), and separating them here would only invite a
// caller to treat one as recoverable.
func runXclip(ctx context.Context, bin string, stdin []byte) error {
	if _, err := exec.LookPath(bin); err != nil {
		return errors.New("secrets: xclip is not installed; install xclip to use clipboard delivery")
	}
	cmd := exec.CommandContext(ctx, bin, "-selection", "clipboard", "-i")
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Run()
}

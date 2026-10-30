//go:build linux

// Purpose: the Linux clipboardOps: `xclip -selection clipboard -i` over a
//
//	stdin pipe, with the same two-step clear as darwin.
//
// Inputs: a payload to place on the clipboard.
// Outputs: newClipboardOps (build-tag selected), satisfying clipboardOps.
// Constraints: no xclip on PATH is ErrClipboardUnsupported (KindUnsupported)
//
//	at probe, before anything is recorded; an xclip that runs and fails is
//	ErrClipboardUnavailable (KindUnavailable). Wayland (wl-copy) stays
//	DEF-P2-wayland-clipboard.
//
// SPORT: internal/secrets clipboard_linux.go/CHANGED (P1-SEC-09).

package secrets

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/acamarata/cascade/pkg/cascade"
)

// xclipBin is the Linux X11 clipboard tool.
const xclipBin = "xclip"

// linuxClipboardOps runs xclip. lookPath and runCmd are overridable in
// tests so no test reaches the real clipboard.
type linuxClipboardOps struct {
	bin      string
	lookPath func(string) (string, error)
	runCmd   func(ctx context.Context, bin string, stdin []byte) error
}

func newClipboardOps() (clipboardOps, error) {
	return &linuxClipboardOps{bin: xclipBin, lookPath: exec.LookPath, runCmd: runXclip}, nil
}

func (l *linuxClipboardOps) platform() string { return "linux" }

func (l *linuxClipboardOps) probe() error {
	if _, err := l.lookPath(l.bin); err != nil {
		return cascade.Wrap(cascade.KindUnsupported, ErrClipboardUnsupported, err.Error())
	}
	return nil
}

func (l *linuxClipboardOps) setValue(ctx context.Context, payload []byte) error {
	if err := l.runCmd(ctx, l.bin, payload); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, ErrClipboardUnavailable, "xclip write failed: "+err.Error())
	}
	return nil
}

// clearValue runs both clear steps whatever the first returns.
func (l *linuxClipboardOps) clearValue(ctx context.Context) error {
	err1 := l.runCmd(ctx, l.bin, []byte(" "))
	err2 := l.runCmd(ctx, l.bin, []byte{})
	if err1 != nil || err2 != nil {
		return cascade.Wrap(cascade.KindUnavailable, ErrClipboardUnavailable, "xclip clear failed")
	}
	return nil
}

// runXclip runs xclip with the payload on stdin, never in argv.
func runXclip(ctx context.Context, bin string, stdin []byte) error {
	cmd := exec.CommandContext(ctx, bin, "-selection", "clipboard", "-i")
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Run()
}

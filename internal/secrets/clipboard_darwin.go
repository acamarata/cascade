//go:build darwin

// Purpose: the macOS clipboardOps: a write-only pbcopy stdin-pipe write,
//
//	and the two-step clear (an ASCII space, then a zero-length write) that
//	defeats clipboard-history tools caching the last non-empty entry.
//
// Inputs: a payload to place on the clipboard.
// Outputs: newClipboardOps (build-tag selected), satisfying clipboardOps.
// Constraints: write-only surface — no clipboard read-back. Payload bytes
//
//	reach pbcopy only through its stdin pipe, never as a command argument,
//	so they never appear in the process table. This file is the Art.2
//	external-contract counterpart: it runs the real /usr/bin/pbcopy.
//
// SPORT: internal/secrets clipboard_darwin.go/ADDED (P1-E08-W2-S16-T4).

package secrets

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/acamarata/cascade/pkg/cascade"
)

// pbcopyBin is the real macOS clipboard tool.
const pbcopyBin = "/usr/bin/pbcopy"

// darwinClipboardOps runs pbcopy as a subprocess for both the write and
// the two clear steps. runCmd is overridable in tests so the shared
// clipboard.go logic can be exercised without spawning a real process;
// clipboard_darwin_test.go additionally runs one case against the real
// binary per Art.2.
type darwinClipboardOps struct {
	bin    string
	runCmd func(ctx context.Context, bin string, stdin []byte) error
}

// newClipboardOps builds the production darwin ops. Never fails at
// construction: pbcopy's absence is a Write-time failure, not a
// build-time one, since a host missing /usr/bin/pbcopy is not expected on
// a tier-1 platform but must still fail closed rather than panic.
func newClipboardOps() (clipboardOps, error) {
	return &darwinClipboardOps{bin: pbcopyBin, runCmd: runPipedCommand}, nil
}

func (d *darwinClipboardOps) platform() string { return "darwin" }

func (d *darwinClipboardOps) setValue(ctx context.Context, payload []byte) error {
	if err := d.runCmd(ctx, d.bin, payload); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: pbcopy write failed")
	}
	return nil
}

// clearValue performs the two-step clear. Both steps run even if the
// first fails: the AUDIT CONTRACT requires the cleared_at update
// "regardless of subprocess error status", and a caller that stopped
// after the first failure would leave the second, more effective step
// (the zero-length write) unattempted for no reason.
func (d *darwinClipboardOps) clearValue(ctx context.Context) error {
	err1 := d.runCmd(ctx, d.bin, []byte(" "))
	err2 := d.runCmd(ctx, d.bin, []byte{})
	if err1 != nil {
		return cascade.Wrap(cascade.KindUnavailable, err1, "secrets: pbcopy clear (space) failed")
	}
	if err2 != nil {
		return cascade.Wrap(cascade.KindUnavailable, err2, "secrets: pbcopy clear (empty) failed")
	}
	return nil
}

// runPipedCommand is the production runCmd: exec.CommandContext with
// stdin piped from a byte slice, never a command-line argument.
func runPipedCommand(ctx context.Context, bin string, stdin []byte) error {
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Run()
}

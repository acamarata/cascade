//go:build darwin

// Purpose: the macOS clipboardOps: a write-only pbcopy stdin-pipe write,
//
//	and the two-step clear (an ASCII space, then a zero-length write) that
//	defeats clipboard-history tools caching the last non-empty entry.
//
// Inputs: a payload to place on the clipboard.
// Outputs: newClipboardOps (build-tag selected), satisfying clipboardOps.
// Constraints: write-only surface, no clipboard read-back. Payload bytes
//
//	reach pbcopy only through its stdin pipe, never as an argument. A host
//	without /usr/bin/pbcopy is ErrClipboardUnsupported at probe, before
//	anything is recorded; a pbcopy that runs and fails is KindUnavailable.
//
// SPORT: internal/secrets clipboard_darwin.go/CHANGED (P1-SEC-09).

package secrets

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/acamarata/cascade/pkg/cascade"
)

// pbcopyBin is the real macOS clipboard tool.
const pbcopyBin = "/usr/bin/pbcopy"

// darwinClipboardOps runs pbcopy. lookPath and runCmd are overridable in
// tests so no test reaches the real clipboard.
type darwinClipboardOps struct {
	bin      string
	lookPath func(string) (string, error)
	runCmd   func(ctx context.Context, bin string, stdin []byte) error
}

func newClipboardOps() (clipboardOps, error) {
	return &darwinClipboardOps{bin: pbcopyBin, lookPath: exec.LookPath, runCmd: runPipedCommand}, nil
}

func (d *darwinClipboardOps) platform() string { return "darwin" }

func (d *darwinClipboardOps) probe() error {
	if _, err := d.lookPath(d.bin); err != nil {
		return cascade.Wrap(cascade.KindUnsupported, ErrClipboardUnsupported, err.Error())
	}
	return nil
}

func (d *darwinClipboardOps) setValue(ctx context.Context, payload []byte) error {
	if err := d.runCmd(ctx, d.bin, payload); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, ErrClipboardUnavailable, "pbcopy write failed: "+err.Error())
	}
	return nil
}

// clearValue runs both clear steps whatever the first returns.
func (d *darwinClipboardOps) clearValue(ctx context.Context) error {
	err1 := d.runCmd(ctx, d.bin, []byte(" "))
	err2 := d.runCmd(ctx, d.bin, []byte{})
	if err1 != nil {
		return cascade.Wrap(cascade.KindUnavailable, ErrClipboardUnavailable, "pbcopy clear (space) failed: "+err1.Error())
	}
	if err2 != nil {
		return cascade.Wrap(cascade.KindUnavailable, ErrClipboardUnavailable, "pbcopy clear (empty) failed: "+err2.Error())
	}
	return nil
}

// runPipedCommand runs bin with stdin as its only input.
func runPipedCommand(ctx context.Context, bin string, stdin []byte) error {
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Run()
}

//go:build windows

// Purpose: the Windows refusal. Clipboard delivery is not offered on
//
//	tier-2 platforms; this file calls no OS clipboard API and links none.
//
// Inputs: none.
// Outputs: newClipboardOps (build-tag selected), satisfying clipboardOps.
// Constraints: probe refuses with ErrTier2Unsupported (KindUnsupported)
//
//	before anything is recorded; set and clear refuse the same way.
//
// SPORT: internal/secrets clipboard_windows.go/CHANGED (P1-SEC-09).

package secrets

import "context"

type windowsClipboardOps struct{}

func newClipboardOps() (clipboardOps, error) {
	return windowsClipboardOps{}, nil
}

func (windowsClipboardOps) platform() string { return "windows" }

func (windowsClipboardOps) probe() error { return ErrTier2Unsupported }

func (windowsClipboardOps) setValue(context.Context, []byte) error { return ErrTier2Unsupported }

func (windowsClipboardOps) clearValue(context.Context) error { return ErrTier2Unsupported }

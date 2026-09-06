//go:build windows

// Purpose: the Windows refusal. Clipboard delivery is not offered on
//
//	tier-2 platforms (06-FORGE-SPEC S2: binary + headless one-shot; no
//	daemon service); this file calls no OS clipboard API and links none.
//
// Inputs: none.
// Outputs: newClipboardOps (build-tag selected), satisfying clipboardOps,
//
//	whose every method refuses with ErrTier2Unsupported.
//
// Constraints: no OS clipboard operation is attempted on this platform.
// SPORT: internal/secrets clipboard_windows.go/ADDED (P1-E08-W2-S16-T4).
package secrets

import "context"

// windowsClipboardOps refuses every call. Construction succeeds (there is
// nothing platform-specific to probe) so ClipboardWriter.Write is the
// single, uniform place every platform's refusal surfaces; setValue is
// the first thing it does, before any Windows clipboard API could be
// reached, and none is ever called or linked here.
type windowsClipboardOps struct{}

func newClipboardOps() (clipboardOps, error) {
	return windowsClipboardOps{}, nil
}

func (windowsClipboardOps) platform() string { return "windows" }

func (windowsClipboardOps) setValue(ctx context.Context, payload []byte) error {
	return ErrTier2Unsupported
}

func (windowsClipboardOps) clearValue(ctx context.Context) error {
	return ErrTier2Unsupported
}

//go:build windows

package config

// Purpose: the Windows counterpart of config_reload_unix.go. SIGHUP has
//   no Windows equivalent, so `cascade config reload` returns an
//   explicit tier-2 refusal rather than silently doing nothing (Art.5
//   platform parity, matching internal/runtime/hotreload_signal_windows.go's
//   daemon-side counterpart).

import "github.com/acamarata/cascade/pkg/cascade"

// sendReloadSignalToPID always refuses on Windows: there is no SIGHUP to
// send. `cascade daemon restart` (D/S-06.T2) is the Windows-supported
// equivalent once that command exists.
func sendReloadSignalToPID(_ int) error {
	return cascade.New(cascade.KindUnsupported,
		"config reload: SIGHUP-based reload is not available on Windows (tier-2); restart the daemon instead")
}

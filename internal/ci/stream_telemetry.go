// Purpose (this file): switches the Go toolchain's telemetry off inside a
// clean-room run's HOME. The toolchain otherwise writes counter files under
// the user config directory after its command has exited, which races the
// removal of the run directory; with the mode file set to "off" it writes
// nothing there, so one RemoveAll is enough.
//
// Inputs: the run's HOME directory and the target OS name.
// Outputs: the run's user-config environment values and the mode file on disk.
// Constraints: the layout mirrors os.UserConfigDir for the forced HOME
// (darwin: HOME/Library/Application Support, windows: %AppData%, others:
// $XDG_CONFIG_HOME) plus "go/telemetry/mode", which is where the go command
// reads its telemetry mode; verified against the installed toolchain by
// TestCleanRoomEnvTurnsGoTelemetryOff.
// SPORT: internal.ci.cleanRoomEnv/ADDED (P1-CI-01).

package ci

import (
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runConfigEnv returns the user-config environment values a run's HOME
// implies: XDG_CONFIG_HOME (linux and other unixes) and APPDATA (windows).
// Forcing both keeps os.UserConfigDir inside the run directory whatever the
// ambient environment held.
func runConfigEnv(home string) map[string]string {
	return map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
	}
}

// telemetryModePath is the file the go command reads its telemetry mode
// from for a run whose HOME is home, on the given GOOS.
func telemetryModePath(goos, home string) string {
	cfg := runConfigEnv(home)
	base := cfg["XDG_CONFIG_HOME"]
	switch goos {
	case "darwin", "ios":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = cfg["APPDATA"]
	}
	return filepath.Join(base, "go", "telemetry", "mode")
}

// disableGoTelemetry writes the telemetry mode file "off" for a run whose
// HOME is home on goos. It must run before the first go command.
func disableGoTelemetry(goos, home string) error {
	path := telemetryModePath(goos, home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: local executor: creating the go telemetry directory")
	}
	if err := runtime.WriteFileAtomic(path, []byte("off"), 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: local executor: turning go telemetry off")
	}
	return nil
}

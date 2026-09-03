//go:build linux

package service

// Purpose: the linux Installer — generates a systemd user unit at
//   ~/.config/systemd/user/cascade.service and registers it via
//   `systemctl --user daemon-reload`+`enable`+`start`, per this ticket's
//   contract and the required-section/key set captured from a real
//   systemd-shipped user unit (testdata/README.md's provenance entry).
// Inputs: Config — HomeDir/Executable/LogPath/Runner, all injected (UID is
//   darwin-only and unused here).
// Outputs: DeltaReport (installed/reloaded/removed/not installed) or a
//   pkg/cascade taxonomy error.
// Constraints: never touches the real filesystem outside Config.HomeDir;
//   never execs outside Config.Runner (a fake in every test — Art.7.1).
//   Unlike darwin's bootout, none of systemctl's four verbs here have a
//   contract-documented "ignore if absent" case (the absent-unit no-op is
//   handled entirely by the pre-flight os.Stat before any systemctl call
//   is made) — so every Runner call's error propagates.
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// systemdUnitName is the fixed unit cascade manages.
const systemdUnitName = "cascade"

// systemdManagedMarker is a leading comment line embedded in every
// generated unit so writeManagedFile can converge over cascade's own
// prior install while refusing to clobber a foreign file.
const systemdManagedMarker = "# Managed by cascade daemon install"

// NewInstaller returns the linux Installer.
func NewInstaller() Installer { return linuxInstaller{} }

type linuxInstaller struct{}

// systemdUnitPath is ~/.config/systemd/user/cascade.service under the
// given home directory.
func systemdUnitPath(homeDir string) string {
	return filepath.Join(homeDir, ".config", "systemd", "user", systemdUnitName+".service")
}

// Install writes the unit (refusing to clobber a foreign file at that
// path) then reloads/enables/starts it. Every systemctl step here is
// correctness-bearing — none is ignored — so any failure propagates and
// no success DeltaReport is ever returned for it (Art.1).
func (linuxInstaller) Install(cfg Config) (DeltaReport, error) {
	if err := validateInstallConfig(cfg); err != nil {
		return DeltaReport{}, err
	}
	path := systemdUnitPath(cfg.HomeDir)
	existed, foreign, err := writeManagedFile(path, renderSystemdUnit(cfg), isManagedUnit)
	if err != nil {
		return DeltaReport{}, err
	}
	if foreign {
		return DeltaReport{}, foreignUnitError(path)
	}

	if err := cfg.Runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return DeltaReport{}, wrapRunnerError(err, "systemctl daemon-reload")
	}
	if err := cfg.Runner.Run("systemctl", "--user", "enable", systemdUnitName); err != nil {
		return DeltaReport{}, wrapRunnerError(err, "systemctl enable")
	}
	if err := cfg.Runner.Run("systemctl", "--user", "start", systemdUnitName); err != nil {
		return DeltaReport{}, wrapRunnerError(err, "systemctl start")
	}

	if existed {
		return DeltaReport{Action: ActionReloaded, Detail: "systemd unit reloaded at " + path}, nil
	}
	return DeltaReport{Action: ActionInstalled, Detail: "systemd unit installed at " + path}, nil
}

// Uninstall stops/disables/reloads then removes the unit. Absent unit is
// a clean no-op, checked before any systemctl call is made.
func (linuxInstaller) Uninstall(cfg Config) (DeltaReport, error) {
	if err := requireHomeDir(cfg); err != nil {
		return DeltaReport{}, err
	}
	path := systemdUnitPath(cfg.HomeDir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return DeltaReport{Action: ActionNotInstalled, Detail: "no systemd unit at " + path}, nil
		}
		return DeltaReport{}, wrapFSError(err, "stat systemd unit")
	}

	if err := cfg.Runner.Run("systemctl", "--user", "stop", systemdUnitName); err != nil {
		return DeltaReport{}, wrapRunnerError(err, "systemctl stop")
	}
	if err := cfg.Runner.Run("systemctl", "--user", "disable", systemdUnitName); err != nil {
		return DeltaReport{}, wrapRunnerError(err, "systemctl disable")
	}
	if err := cfg.Runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return DeltaReport{}, wrapRunnerError(err, "systemctl daemon-reload")
	}
	if err := removeManagedFile(path); err != nil {
		return DeltaReport{}, err
	}
	return DeltaReport{Action: ActionRemoved, Detail: "systemd unit removed from " + path}, nil
}

// renderSystemdUnit builds the unit content: the required [Unit]/
// [Service]/[Install] sections and keys (verified against testdata/
// golden_systemd.service, itself derived from a real systemd-shipped user
// unit — see testdata/README.md). ExecStart's executable path is quoted
// per systemd's unit-file quoting rules whenever it contains whitespace or
// a quote/backslash, so a path with a space cannot be mis-split into
// multiple argv entries or corrupt the file.
func renderSystemdUnit(cfg Config) []byte {
	var buf bytes.Buffer
	buf.WriteString(systemdManagedMarker + "\n")
	buf.WriteString("[Unit]\n")
	buf.WriteString("Description=Cascade daemon\n")
	buf.WriteString("After=default.target\n\n")
	buf.WriteString("[Service]\n")
	buf.WriteString("ExecStart=" + systemdQuoteArg(cfg.Executable) + " daemon run\n")
	buf.WriteString("Type=simple\n")
	buf.WriteString("Restart=on-failure\n")
	buf.WriteString("RestartSec=5\n\n")
	buf.WriteString("[Install]\n")
	buf.WriteString("WantedBy=default.target\n")
	return buf.Bytes()
}

// systemdQuoteArg quotes s per systemd.syntax(5) whenever it contains
// whitespace or a character that would otherwise corrupt the unit file's
// word-splitting; a plain path with none of those is left unquoted,
// matching real systemd unit files.
func systemdQuoteArg(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\$") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func isManagedUnit(content []byte) bool { return containsMarker(content, systemdManagedMarker) }

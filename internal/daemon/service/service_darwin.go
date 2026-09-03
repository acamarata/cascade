//go:build darwin

package service

// Purpose: the darwin Installer — generates a launchd user-agent plist at
//   ~/Library/LaunchAgents/com.acamarata.cascade.plist and registers it via
//   `launchctl bootout`(ignored)+`bootstrap`, per this ticket's contract
//   and the required-key set captured from a real macOS launchd user agent
//   (testdata/README.md's provenance entry).
// Inputs: Config — HomeDir/Executable/LogPath/UID/Runner, all injected.
// Outputs: DeltaReport (installed/reloaded/removed/not installed) or a
//   pkg/cascade taxonomy error.
// Constraints: never touches the real filesystem outside Config.HomeDir;
//   never execs outside Config.Runner (a fake in every test in this
//   package — Art.7.1).
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
)

// launchdLabel is the fixed Label every generated plist carries; cascade
// manages exactly one user agent per machine.
const launchdLabel = "com.acamarata.cascade"

// launchdManagedMarker is embedded as an extra plist key so writeManagedFile
// can tell "cascade's own prior install" (safe to converge over) apart from
// "a foreign file that happens to live at this path" (refuse).
const launchdManagedMarker = "<key>X-Cascade-Managed</key>"

// NewInstaller returns the darwin Installer.
func NewInstaller() Installer { return darwinInstaller{} }

type darwinInstaller struct{}

// launchAgentPath is ~/Library/LaunchAgents/com.acamarata.cascade.plist
// under the given home directory.
func launchAgentPath(homeDir string) string {
	return filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
}

// domainTarget is launchctl's gui/<uid> domain.
func domainTarget(uid int) string { return fmt.Sprintf("gui/%d", uid) }

// serviceTarget is launchctl's gui/<uid>/<label> service target.
func serviceTarget(uid int) string { return fmt.Sprintf("%s/%s", domainTarget(uid), launchdLabel) }

// Install writes the plist (refusing to clobber a foreign file at that
// path) then bootstraps it into the user's gui domain. A prior bootout is
// attempted and its result ignored — the unit may not currently be
// loaded, which is not a failure (contract: "bootout (ignore if not
// loaded)"). bootstrap failing IS a failure: that is the step that
// actually registers the service, so its error always propagates and no
// success DeltaReport is ever returned for it (Art.1).
func (darwinInstaller) Install(cfg Config) (DeltaReport, error) {
	if err := validateInstallConfig(cfg); err != nil {
		return DeltaReport{}, err
	}
	path := launchAgentPath(cfg.HomeDir)
	existed, foreign, err := writeManagedFile(path, renderLaunchdPlist(cfg), isManagedPlist)
	if err != nil {
		return DeltaReport{}, err
	}
	if foreign {
		return DeltaReport{}, foreignUnitError(path)
	}

	_ = cfg.Runner.Run("launchctl", "bootout", serviceTarget(cfg.UID))
	if err := cfg.Runner.Run("launchctl", "bootstrap", domainTarget(cfg.UID), path); err != nil {
		return DeltaReport{}, wrapRunnerError(err, "launchctl bootstrap")
	}

	if existed {
		return DeltaReport{Action: ActionReloaded, Detail: "launch agent reloaded at " + path}, nil
	}
	return DeltaReport{Action: ActionInstalled, Detail: "launch agent installed at " + path}, nil
}

// Uninstall boots the agent out (best-effort, ignored — it may already be
// unloaded) then removes the plist. Absent plist is a clean no-op.
func (darwinInstaller) Uninstall(cfg Config) (DeltaReport, error) {
	if err := requireHomeDir(cfg); err != nil {
		return DeltaReport{}, err
	}
	path := launchAgentPath(cfg.HomeDir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return DeltaReport{Action: ActionNotInstalled, Detail: "no launch agent at " + path}, nil
		}
		return DeltaReport{}, wrapFSError(err, "stat launch agent")
	}

	_ = cfg.Runner.Run("launchctl", "bootout", serviceTarget(cfg.UID))
	if err := removeManagedFile(path); err != nil {
		return DeltaReport{}, err
	}
	return DeltaReport{Action: ActionRemoved, Detail: "launch agent removed from " + path}, nil
}

// renderLaunchdPlist builds the plist content, keys in the same
// alphabetical order `plutil -convert xml1` emits (verified against
// testdata/golden_launchd.plist, itself a real plutil conversion). Every
// dynamic value goes through escapeXMLText so a path or executable
// containing a quote, space, or XML-special character cannot corrupt the
// document.
func renderLaunchdPlist(cfg Config) []byte {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	buf.WriteString("<plist version=\"1.0\">\n<dict>\n")
	buf.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	buf.WriteString("\t<key>Label</key>\n\t<string>" + escapeXMLText(launchdLabel) + "</string>\n")
	buf.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range []string{cfg.Executable, "daemon", "run"} {
		buf.WriteString("\t\t<string>" + escapeXMLText(arg) + "</string>\n")
	}
	buf.WriteString("\t</array>\n")
	buf.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	buf.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + escapeXMLText(cfg.LogPath) + "</string>\n")
	buf.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + escapeXMLText(cfg.LogPath) + "</string>\n")
	buf.WriteString(launchdManagedMarker + "\n\t<true/>\n")
	buf.WriteString("</dict>\n</plist>\n")
	return buf.Bytes()
}

// escapeXMLText escapes s for safe use as XML character data.
func escapeXMLText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func isManagedPlist(content []byte) bool { return containsMarker(content, launchdManagedMarker) }

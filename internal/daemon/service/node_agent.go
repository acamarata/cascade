package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// NodeAgentUnitName is the systemd user unit for the node agent.
const NodeAgentUnitName = "cascade-node"

const nodeAgentManagedMarker = "# Managed by cascade node service install"

// NodeAgentUnitPath resolves the node unit under the supplied home directory.
func NodeAgentUnitPath(homeDir string) string {
	return filepath.Join(homeDir, ".config", "systemd", "user", NodeAgentUnitName+".service")
}

// NodeAgentInstaller manages a Linux user unit, requiring linger at install.
// GOOS and LingerDir are injected so tests never consult real linger state.
type NodeAgentInstaller struct {
	GOOS      string
	LingerDir string
}

// NewNodeAgentInstaller selects the host platform and systemd linger directory.
func NewNodeAgentInstaller() NodeAgentInstaller {
	return NodeAgentInstaller{GOOS: runtime.GOOS, LingerDir: DefaultLingerDir}
}

// Install checks linger before writing a unit or invoking systemctl.
func (inst NodeAgentInstaller) Install(cfg Config) (DeltaReport, error) {
	if inst.GOOS != "linux" {
		return DeltaReport{}, cascade.New(cascade.KindUnsupported, "node service: install is linux-only in P1")
	}
	if err := validateInstallConfig(cfg); err != nil {
		return DeltaReport{}, err
	}
	enabled, err := LingerEnabled(inst.LingerDir, cfg.User)
	if err != nil {
		return DeltaReport{}, err
	}
	if !enabled {
		return DeltaReport{}, ErrNoLinger(cfg.User)
	}
	path := NodeAgentUnitPath(cfg.HomeDir)
	existed, foreign, err := writeManagedFile(path, renderNodeAgentUnit(cfg), isManagedNodeAgentUnit)
	if err != nil {
		return DeltaReport{}, err
	}
	if foreign {
		return DeltaReport{}, foreignUnitError(path)
	}
	if err := runNodeAgentSystemctl(cfg.Runner, "daemon-reload", "enable", "start"); err != nil {
		return DeltaReport{}, err
	}
	if existed {
		return DeltaReport{Action: ActionReloaded, Detail: "systemd unit reloaded at " + path}, nil
	}
	return DeltaReport{Action: ActionInstalled, Detail: "systemd unit installed at " + path}, nil
}

// Uninstall stops, disables and reloads the user unit before removing it.
func (inst NodeAgentInstaller) Uninstall(cfg Config) (DeltaReport, error) {
	if inst.GOOS != "linux" {
		return DeltaReport{}, cascade.New(cascade.KindUnsupported, "node service: uninstall is linux-only in P1")
	}
	if err := requireHomeDir(cfg); err != nil {
		return DeltaReport{}, err
	}
	path := NodeAgentUnitPath(cfg.HomeDir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return DeltaReport{Action: ActionNotInstalled, Detail: "no systemd unit at " + path}, nil
		}
		return DeltaReport{}, wrapFSError(err, "stat systemd unit")
	}
	if err := runNodeAgentSystemctl(cfg.Runner, "stop", "disable", "daemon-reload"); err != nil {
		return DeltaReport{}, err
	}
	if err := removeManagedFile(path); err != nil {
		return DeltaReport{}, err
	}
	return DeltaReport{Action: ActionRemoved, Detail: "systemd unit removed from " + path}, nil
}

func runNodeAgentSystemctl(runner Runner, verbs ...string) error {
	for _, verb := range verbs {
		args := []string{"--user", verb}
		if verb != "daemon-reload" {
			args = append(args, NodeAgentUnitName)
		}
		if err := runner.Run("systemctl", args...); err != nil {
			return wrapRunnerError(err, "systemctl "+verb)
		}
	}
	return nil
}

func renderNodeAgentUnit(cfg Config) []byte {
	args := []string{systemdQuoteArg(cfg.Executable), "node", "serve"}
	for _, arg := range cfg.NodeServeArgs {
		args = append(args, systemdQuoteArg(arg))
	}
	return []byte(nodeAgentManagedMarker + "\n" +
		"[Unit]\nDescription=Cascade node agent\n" +
		"After=network-online.target\nWants=network-online.target\n\n" +
		"[Service]\nExecStart=" + strings.Join(args, " ") + "\n" +
		"Type=simple\nRestart=always\nRestartSec=5\n\n" +
		"[Install]\nWantedBy=default.target\n")
}

func isManagedNodeAgentUnit(content []byte) bool {
	return containsMarker(content, nodeAgentManagedMarker)
}

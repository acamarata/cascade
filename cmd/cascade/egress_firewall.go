// Purpose: the ONE outbound firewall this process runs. It owns the custody
//
//	selection, the non-elevated broker and the egress vault, and it is the
//	only non-test code that builds an *egress.Engine: every internal package
//	takes its engine from egress.ProcessEngine, which this file's init()
//	backs. It also installs the source of the process secret detector, so
//	the operator's [secrets] settings govern every scan site.
//
// Inputs: the cascade paths (lazily resolved), config.toml's [secrets]
//
//	section, and the custody the host offers.
//
// Outputs: *processFirewall (memoized per process) and the engines built
//
//	over it; the [secrets] detection configuration.
//
// Constraints: fail closed. No resolvable data directory and no custody are
//
//	refusals with their Kind and cause unchanged; a failure is never cached,
//	so a later call (a vault that became available) can succeed. An invalid
//	[secrets] section is a KindInvalidInput refusal naming the key, never
//	the shipped defaults. Nothing here reads a secret value into an error.
//
// SPORT: EGRESS_PROCESS_FIREWALL: ADD (cmd/cascade process firewall).
package main

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// processFirewall is the process's vault binding for outbound content.
type processFirewall struct {
	broker *secrets.Broker
	vault  *secrets.EgressVault
}

// selectProcessCustody is the ONE custody selector for every host vault
// cmd/cascade opens: the process firewall, `cascade vault`, `provider add`,
// doctor's vault checks and the daemon's chat scrub all select through it
// with hostCustodyConfig, so they read and write the same store (under the
// QA switch too). It is also the test seam: cmd/cascade's TestMain wraps
// it so no test reaches the host keychain. internal/build refuses any
// other non-test secrets.SelectCustody reference in cmd/cascade.
var selectProcessCustody = secrets.SelectCustody

// hostCustodyConfig is the custody every host vault selection asks for: the
// cascade vault under dir, forced onto the encrypted file vault in the same
// dir when a devkeys build runs with the QA switch set.
func hostCustodyConfig(dir string) secrets.Config {
	return hostCustodyConfigFor(dir, elevation.DevkeysBuild(), os.Getenv)
}

// hostCustodyConfigFor is hostCustodyConfig with the build bit and the
// environment injected.
func hostCustodyConfigFor(dir string, devkeys bool, getenv func(string) string) secrets.Config {
	return secrets.Config{Service: vaultService, Dir: dir, ForceFileVault: secrets.QAFileVaultForced(devkeys, getenv)}
}

// bridgeCustodyConfig is the cascade-pa bridge's token vault config: the
// plugin's own label and dir, with the file vault forced exactly when
// hostCustodyConfig forces it, so the bridge reads the store the firewall
// and `cascade vault` use under the QA switch too.
func bridgeCustodyConfig(dir string) secrets.Config {
	return bridgeCustodyConfigFor(dir, elevation.DevkeysBuild(), os.Getenv)
}

// bridgeCustodyConfigFor is bridgeCustodyConfig with the build bit and the
// environment injected.
func bridgeCustodyConfigFor(dir string, devkeys bool, getenv func(string) string) secrets.Config {
	cfg := plugins.BridgeVaultConfig(dir)
	cfg.ForceFileVault = hostCustodyConfigFor(dir, devkeys, getenv).ForceFileVault
	return cfg
}

// processFirewallState memoizes the bound firewall and its data directory.
// A mutex rather than sync.Once: a failure must not be cached. provisional
// marks a binding the path-less engine factory made over the default
// paths: no caller chose that directory, so the first caller that names
// one takes it over.
var processFirewallState struct {
	mu          sync.Mutex
	fw          *processFirewall
	dataDir     string
	provisional bool
}

// firewallBind is how a caller asks for the process firewall.
type firewallBind int

const (
	bindProvisional firewallBind = iota // the path-less factory: default paths
	bindNamed                           // a caller naming its data directory
	bindOwner                           // the daemon's composition root
)

// errFirewallBoundElsewhere refuses a caller whose data directory is not
// the one the process firewall is bound to. No path in the text.
func errFirewallBoundElsewhere() error {
	return cascade.New(cascade.KindConflict, "egress firewall: already bound to another data directory")
}

// errNoFirewallDataDir refuses a caller whose data directory is unknown.
func errNoFirewallDataDir() error {
	return cascade.New(cascade.KindUnavailable,
		"egress firewall: the cascade data directory could not be resolved")
}

// processFirewallFor returns the process firewall, building it over paths on
// the first successful call and returning that same value afterwards. A
// caller whose data directory differs from the bound one is refused, so a
// grant store under one directory is never paired with another's broker.
func processFirewallFor(paths runtime.PathProvider) (*processFirewall, error) {
	return bindProcessFirewall(paths, bindNamed)
}

// bindDaemonFirewall is processFirewallFor for the daemon's composition
// root, which owns the binding: it rebinds the process firewall to the
// daemon's data directory, so every daemon path that follows (and the
// engine factory) uses the daemon's vault. Callers that took an earlier
// firewall keep it, paired with their own directory.
func bindDaemonFirewall(paths runtime.PathProvider) (*processFirewall, error) {
	return bindProcessFirewall(paths, bindOwner)
}

// bindProcessFirewall validates paths and binds under the lock. Checked
// before the memo: a caller with no paths is refused every time, not handed
// whatever an earlier caller built.
func bindProcessFirewall(paths runtime.PathProvider, mode firewallBind) (*processFirewall, error) {
	if paths == nil || paths.DataDir() == "" {
		return nil, errNoFirewallDataDir()
	}
	processFirewallState.mu.Lock()
	defer processFirewallState.mu.Unlock()
	return bindProcessFirewallLocked(paths.DataDir(), mode)
}

// bindProcessFirewallLocked returns the firewall bound to dir, building it
// when none is bound, when a named dir replaces a provisional binding, or
// when the daemon rebinds. The caller holds processFirewallState.mu.
func bindProcessFirewallLocked(dir string, mode firewallBind) (*processFirewall, error) {
	if fw := processFirewallState.fw; fw != nil {
		switch {
		case processFirewallState.dataDir == dir:
			processFirewallState.provisional = processFirewallState.provisional && mode == bindProvisional
			return fw, nil
		case mode == bindProvisional || (mode == bindNamed && !processFirewallState.provisional):
			return nil, errFirewallBoundElsewhere()
		}
	}
	custody, err := selectProcessCustody(hostCustodyConfig(dir))
	if err != nil {
		return nil, err
	}
	// No elevation gate: this broker backs the EgressVault, whose Get is the
	// documented non-elevated path. Broker.Get itself still refuses without
	// a gate, so nothing here gains an elevated read.
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		return nil, err
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		return nil, err
	}
	processFirewallState.fw = &processFirewall{broker: broker, vault: vault}
	processFirewallState.dataDir, processFirewallState.provisional = dir, mode == bindProvisional
	return processFirewallState.fw, nil
}

// boundProcessFirewall is the path-less lookup the engine factory uses: the
// bound firewall when there is one (the daemon binds its own paths first),
// else one built over the default paths.
func boundProcessFirewall() (*processFirewall, error) {
	processFirewallState.mu.Lock()
	defer processFirewallState.mu.Unlock()
	if processFirewallState.fw != nil {
		return processFirewallState.fw, nil
	}
	dir := lazyPaths{}.DataDir()
	if dir == "" {
		return nil, errNoFirewallDataDir()
	}
	return bindProcessFirewallLocked(dir, bindProvisional)
}

// Engine binds r to this firewall's vault and the process detector.
func (f *processFirewall) Engine(r *egress.Registry) (*egress.Engine, error) {
	if f == nil || f.vault == nil {
		return nil, cascade.New(cascade.KindUnavailable, "egress firewall: not built")
	}
	detector, err := secrets.ProcessDetector()
	if err != nil {
		return nil, err
	}
	return egress.NewEngine(r, f.vault, detector)
}

// Broker returns the process's non-elevated vault broker, for the daemon
// paths that read under a human-issued grant and must use the same custody
// as the firewall.
func (f *processFirewall) Broker() *secrets.Broker {
	if f == nil {
		return nil
	}
	return f.broker
}

// firewallUnavailable is the egress gate a caller gets when the process
// firewall cannot be built (provider health's reachability prober): every
// capability request is refused with the cause, so the operator sees why
// the probe was refused, and nothing leaves.
type firewallUnavailable struct{ cause error }

// Capability refuses every class with the firewall's cause.
func (u firewallUnavailable) Capability(egress.EgressClass) (egress.Capability, error) {
	return egress.Capability{}, u.cause
}

// Registry is the default registry; it is never reached past Capability.
func (firewallUnavailable) Registry() *egress.Registry { return egress.DefaultRegistry() }

// firewallCause keeps err's Kind and its own message and drops the wrapped
// chain, which can carry a vault path. A non-cascade error becomes a bare
// KindUnavailable refusal.
func firewallCause(err error) error {
	var ce *cascade.Error
	if errors.As(err, &ce) {
		return cascade.Newf(ce.Kind, "egress firewall unavailable: %s", ce.Msg)
	}
	return cascade.New(cascade.KindUnavailable, "egress firewall unavailable")
}

func init() {
	egress.SetProcessEngineFactory(processEngine)
	if err := secrets.SetProcessDetectionConfigSource(loadProcessDetectionConfig); err != nil {
		// Only reachable if a package init resolved the detector first,
		// which would leave scan sites on rules the operator did not set.
		panic(err)
	}
}

// processEngine is the factory egress.ProcessEngine calls.
func processEngine(r *egress.Registry) (*egress.Engine, error) {
	fw, err := boundProcessFirewall()
	if err != nil {
		return nil, err
	}
	return fw.Engine(r)
}

// loadProcessDetectionConfig reads config.toml's [secrets] section. An
// absent file or section is the shipped defaults; anything present is
// parsed strictly.
func loadProcessDetectionConfig() (secrets.DetectionConfig, error) {
	paths := lazyPaths{}
	path := paths.ConfigPath()
	if path == "" {
		return secrets.DetectionConfig{}, cascade.Wrap(cascade.KindUnavailable, paths.ResolveErr(),
			"secrets: the config file holding [secrets] could not be located")
	}
	cfg, err := runtime.Load(context.Background(), runtime.LoadOptions{Path: path})
	if err != nil {
		return secrets.DetectionConfig{}, err
	}
	return detectionConfigFrom(cfg.Extra)
}

// detectionConfigFrom interprets the [secrets] table of a loaded config.
func detectionConfigFrom(extra map[string]interface{}) (secrets.DetectionConfig, error) {
	raw, ok := extra["secrets"]
	if !ok {
		// No section: every key takes its shipped default.
		return secrets.ParseDetectionConfig(map[string]interface{}{})
	}
	section, ok := raw.(map[string]interface{})
	if !ok {
		return secrets.DetectionConfig{}, cascade.New(cascade.KindInvalidInput,
			"secrets: [secrets] must be a table")
	}
	return secrets.ParseDetectionConfig(section)
}

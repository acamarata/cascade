//go:build !windows

// Purpose: the daemon side of [secrets] hot reload. The applier installed on
// the HotReloader reloads the one process detector in place, so a tightened
// entropy_floor or confidence_threshold governs the very next egress scan,
// and bootSecrets makes a degraded boot's detector run the baseline's
// effective thresholds instead of the on-disk ones.
// Inputs: the proposed [secrets] table, the daemon manifest, the boot config
// and its baseline check.
// Outputs: an applier function value; thresholds applied to the detector and
// to the boot config; the egress-firewall manifest entry kept truthful.
// Constraints: fail closed. A table ParseDetectionConfig refuses is an error
// and nothing is applied. An error of kind unavailable also marks the
// egress-firewall subsystem failed (status.get: degraded) until a later
// apply succeeds. The loosening gate is not here: Reload only reaches the
// applier for a config the gate (or the P1-SEC-04 slot) already passed.
// SPORT: cmd/cascade daemon (CHANGED, P1-SEC-24 [secrets] hot reload).
package main

import (
	"fmt"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// secretsStatusDetail is the egress-firewall manifest detail for cfg, in the
// form wireEgressFirewall writes at start.
func secretsStatusDetail(cfg secrets.DetectionConfig) string {
	return fmt.Sprintf("confidence_threshold=%g entropy_floor=%g", cfg.ConfidenceThreshold, cfg.EntropyFloor)
}

// newSecretsApplier returns the HotReloader applier. set is
// secrets.SetProcessDetectionConfig in production; a test passes a failing
// one. m may be nil.
func newSecretsApplier(m *daemon.Manifest, set func(secrets.DetectionConfig) error) func(map[string]interface{}) error {
	return func(section map[string]interface{}) error {
		cfg, err := secrets.ParseDetectionConfig(section)
		if err == nil {
			err = set(cfg)
		}
		if err != nil {
			if m != nil && cascade.HasKind(err, cascade.KindUnavailable) {
				m.Failed(egressFirewallSubsystem, "[secrets] reload failed: "+err.Error())
			}
			return err
		}
		if m != nil {
			m.Started(egressFirewallSubsystem, secretsStatusDetail(cfg))
		}
		return nil
	}
}

// secretsApplierOption installs the production applier on a new HotReloader
// before its watcher starts, so no reload can run without it.
func secretsApplierOption(m *daemon.Manifest) func(*runtime.HotReloader) {
	return func(hr *runtime.HotReloader) {
		hr.SetSecretsApplier(newSecretsApplier(m, secrets.SetProcessDetectionConfig))
	}
}

// setProcessDetection reloads the one process detector; a variable so a test
// can make the daemon's boot fail.
var setProcessDetection = secrets.SetProcessDetectionConfig

// bootSecrets makes the boot config and the process detector carry the
// thresholds the baseline check decided to run with. A degraded boot keeps
// only a threshold tighter than the default; without this the detector would
// run the on-disk (possibly looser) value. It runs before the HotReloader
// exists, so the reloader's first comparison starts from the effective values.
// It reads nothing from the environment: it works from cfg, which the daemon
// loaded with its own paths and environment. An error stops the daemon: a
// [secrets] table the detector refuses, or a detector that will not take the
// effective values, must not be left running on something looser.
func bootSecrets(cfg *runtime.Config, baseline *configBaseline) error {
	return bootSecretsWith(cfg, baseline, setProcessDetection)
}

// bootSecretsWith is bootSecrets over an injected detector setter.
func bootSecretsWith(cfg *runtime.Config, baseline *configBaseline, set func(secrets.DetectionConfig) error) error {
	if cfg == nil || baseline == nil || baseline.Checker == nil {
		return nil
	}
	boot, ok := baseline.Checker.BootEffective()
	if !ok {
		return nil
	}
	// The table as loaded must parse before the effective values replace it:
	// replacing first would hide an invalid key the operator wrote.
	if _, err := detectionConfigFrom(cfg.Extra); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "boot: [secrets]")
	}
	runtime.ApplySecretsThresholds(cfg, boot.Secrets)
	detection, err := detectionConfigFrom(cfg.Extra)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "boot: effective [secrets]")
	}
	if err := set(detection); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "boot: apply the effective [secrets] thresholds to the detector")
	}
	return nil
}

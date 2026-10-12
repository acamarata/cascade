// Purpose: the ONE secret detector a process runs. Every scan site in the
//
//	process (the outbound firewall, the conversation scrub, the plugin
//	storage gate, the config-literal checks) asks ProcessDetector for it,
//	so the operator's [secrets] settings apply everywhere at once and a
//	later reload changes one detector in place rather than missing copies.
//
// Inputs: the configuration source the composition root installs (it reads
//
//	config.toml's [secrets] section), or nothing.
//
// Outputs: the shared *Detector, or the source's error.
// Constraints: fail closed. A source error (an invalid [secrets] section) is
//
//	returned on every call until SetProcessDetectionConfig installs a valid
//	configuration; it is never replaced by the shipped defaults. The shipped
//	defaults apply only when no source was installed, which in this
//	repository means a test binary: the cascade binary installs one at init,
//	and its tests prove it.
//
// SPORT: SECRETS_DETECTOR: ADD (internal/secrets ProcessDetector).

package secrets

import (
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// processDetection is the process-wide detector state.
var processDetection struct {
	mu       sync.Mutex
	src      func() (DetectionConfig, error)
	detector *Detector
	// err is the source's failure, sticky until SetProcessDetectionConfig.
	err error
}

// SetProcessDetectionConfigSource installs the source ProcessDetector
// resolves its configuration from. It may be called once, and only before
// the detector was first resolved: a second source, or a source arriving
// after a scan site already resolved the defaults, is a KindConflict,
// because either would leave part of the process scanning under rules the
// operator did not set.
func SetProcessDetectionConfigSource(src func() (DetectionConfig, error)) error {
	if src == nil {
		return cascade.New(cascade.KindInvalidInput, "secrets: the detection config source must not be nil")
	}
	processDetection.mu.Lock()
	defer processDetection.mu.Unlock()
	switch {
	case processDetection.src != nil:
		return cascade.New(cascade.KindConflict, "secrets: a detection config source is already installed")
	case processDetection.detector != nil || processDetection.err != nil:
		return cascade.New(cascade.KindConflict,
			"secrets: the process detector was resolved before its config source was installed")
	}
	processDetection.src = src
	return nil
}

// ProcessDetector returns the process's one detector, the same pointer on
// every call. The first call resolves the installed source; with none
// installed it takes the shipped defaults.
func ProcessDetector() (*Detector, error) {
	processDetection.mu.Lock()
	defer processDetection.mu.Unlock()
	if processDetection.detector != nil {
		return processDetection.detector, nil
	}
	if processDetection.err != nil {
		return nil, processDetection.err
	}
	cfg := DefaultDetectionConfig()
	if processDetection.src != nil {
		resolved, err := processDetection.src()
		if err != nil {
			processDetection.err = err
			return nil, err
		}
		cfg = resolved
	}
	detector, err := NewDetector(DefaultRegistry(), cfg)
	if err != nil {
		processDetection.err = err
		return nil, err
	}
	processDetection.detector = detector
	return detector, nil
}

// SetProcessDetectionConfig validates cfg and applies it to the process
// detector in place (the same pointer every holder already has). A
// rejected cfg leaves the running configuration, and any sticky source
// error, exactly as they were.
func SetProcessDetectionConfig(cfg DetectionConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	processDetection.mu.Lock()
	defer processDetection.mu.Unlock()
	if processDetection.detector != nil {
		return processDetection.detector.Reload(cfg)
	}
	detector, err := NewDetector(DefaultRegistry(), cfg)
	if err != nil {
		return err
	}
	processDetection.detector, processDetection.err = detector, nil
	return nil
}

// ProcessDetectionConfig reports the configuration the process detector
// runs under. When the detector cannot be resolved it reports the zero
// DetectionConfig, which Validate refuses, so a caller that builds from it
// fails closed rather than scanning under the defaults.
func ProcessDetectionConfig() DetectionConfig {
	detector, err := ProcessDetector()
	if err != nil {
		return DetectionConfig{}
	}
	return detector.Config()
}

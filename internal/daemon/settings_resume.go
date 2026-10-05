// Purpose: the [daemon] fan-out retention settings the resume registration
//   reads: fanout_record_ttl (how long an unattached fan-out keeps its
//   records before the sweep expires it) and fanout_sweep_interval (how
//   often the sweep runs). Mirrors ResolveSettings (settings.go).
// Inputs: the loaded *runtime.Config (nil means every default).
// Outputs: ResumeSettings, or a typed KindInvalidInput error.
// Constraints: a present value must be a Go duration string or a number of
//   seconds and strictly positive; bad config is refused, never ignored.
//   Settings (daemon.go) is not touched.
// SPORT: internal/daemon (CHANGE, P1-CORE-15).

package daemon

import (
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// defaultFanOutRecordTTL is fanout_record_ttl when unset.
	defaultFanOutRecordTTL = 24 * time.Hour
	// defaultFanOutSweepInterval is fanout_sweep_interval when unset.
	defaultFanOutSweepInterval = time.Hour
)

// ResumeSettings are the resolved fan-out retention settings.
type ResumeSettings struct {
	// RecordTTL is how long after its last journal entry an unattached
	// fan-out keeps its records; the sweep then expires it.
	RecordTTL time.Duration
	// SweepInterval is the period between two fan-out sweeps.
	SweepInterval time.Duration
}

// ResolveResumeSettings reads [daemon] fanout_record_ttl and
// fanout_sweep_interval out of cfg.Extra, defaulting each to 24h and 1h.
func ResolveResumeSettings(cfg *runtime.Config) (ResumeSettings, error) {
	s := ResumeSettings{RecordTTL: defaultFanOutRecordTTL, SweepInterval: defaultFanOutSweepInterval}
	if cfg == nil || cfg.Extra == nil {
		return s, nil
	}
	section, ok := cfg.Extra["daemon"].(map[string]interface{})
	if !ok {
		return s, nil
	}
	for key, dst := range map[string]*time.Duration{
		"fanout_record_ttl":     &s.RecordTTL,
		"fanout_sweep_interval": &s.SweepInterval,
	} {
		raw, present := section[key]
		if !present {
			continue
		}
		d, err := parseGraceValue(raw)
		if err == nil && d <= 0 {
			err = cascade.New(cascade.KindInvalidInput, "must be positive")
		}
		if err != nil {
			return ResumeSettings{}, cascade.Wrapf(cascade.KindInvalidInput, err, "daemon.%s: %v", key, raw)
		}
		*dst = d
	}
	return s, nil
}

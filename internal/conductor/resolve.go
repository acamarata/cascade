// Purpose: the model.execute door's two pure resolution functions:
//   ResolveSensitivity (fail-closed sensitivity, §5.16) and ResolveTaskClass
//   (the frozen nine-class §5.16 enum, validated per R-21.208's terminal-deny
//   rule). Neither function dispatches, writes an audit record, or reaches
//   an approval/elevation flow - execute.go sequences both ahead of any of
//   that.
// Inputs: a provider.ModelRequest.
// Outputs: a resolved provider.SensitivityTier, or a taxonomy error.
// Constraints: pure functions only; no I/O, no clock, no randomness.
// SPORT: conductor.execute/ADD (P1-E11-W3-S22-T1).

package conductor

import "github.com/acamarata/cascade/pkg/provider"

// taskClasses is the frozen nine-row §5.16 enum (R-21.50): classify,
// segment, summarize, extract, chat, code, reason, review, arbitrate. This
// is the closed set ResolveTaskClass validates req.TaskClass against; the
// §5.18 model_class -> task_class mapping table is K/S-22.T4's
// task_classes.go to own, not this file's (R-16.59).
var taskClasses = map[string]bool{
	"classify":  true,
	"segment":   true,
	"summarize": true,
	"extract":   true,
	"chat":      true,
	"code":      true,
	"reason":    true,
	"review":    true,
	"arbitrate": true,
}

// ResolveSensitivity resolves req's sensitivity tier under the §5.16
// fail-closed rule: the zero value (SensitivityRestricted) and any value
// outside the four declared members resolve to SensitivityRestricted. A
// declared SensitivityLocalOnly is returned unchanged - execute.go is
// responsible for requiring a controller-local lane for it, never this
// function silently narrowing or widening it.
func ResolveSensitivity(req provider.ModelRequest) provider.SensitivityTier {
	if !req.Sensitivity.Valid() {
		return provider.SensitivityRestricted
	}
	return req.Sensitivity
}

// ResolveTaskClass validates req.TaskClass against the frozen nine-class
// §5.16 enum. An empty or unknown value is a TERMINAL DENY per R-21.208:
// ErrInvalidRequest, never an approval or elevation path. Conductor
// performs no model_class resolution anywhere in this call path (R-16.59);
// a caller outside conductor that only has a model_class must resolve it to
// a task_class before ever constructing a ModelRequest.
func ResolveTaskClass(req provider.ModelRequest) (string, error) {
	if !taskClasses[req.TaskClass] {
		return "", ErrInvalidRequest
	}
	return req.TaskClass, nil
}

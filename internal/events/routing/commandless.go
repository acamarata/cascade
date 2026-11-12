package routing

import "github.com/acamarata/cascade/pkg/cascade"

// commandLess identifies in-process actions by origin or explicit declaration.
// Empty text alone never makes a command-less action (R-14.211).
func (a Action) commandLess() bool {
	return a.Origin == OriginScheduler || a.CommandLess
}

// validateCommand refuses missing shell text and contradictory declarations.
func (a Action) validateCommand() error {
	if a.CommandLess && a.Command != "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"routing: %s action %q declares command-less with command text (%s)",
			a.Origin, a.Ref, RouteDeniedCode)
	}
	if a.Command == "" && !a.commandLess() {
		return cascade.Newf(cascade.KindInvalidInput,
			"routing: %s action %q has no command text to classify (%s)",
			a.Origin, a.Ref, RouteDeniedCode)
	}
	return nil
}

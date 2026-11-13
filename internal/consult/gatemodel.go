package consult

import "errors"

// ErrUnknownKind is returned when a gate kind is empty or not recognized.
// Callers should treat this as a fail-closed condition and not select a model.
var ErrUnknownKind = errors.New("consult: unknown gate kind")

// GateModel returns the model that should be used for a gate of the given
// kind. Routine gates and cheap review gates are routed to the cheap native
// model, while harder and higher-risk gates are reserved for the strongest
// model. Unknown or empty kinds fail closed with ErrUnknownKind and no model.
func GateModel(kind string) (string, error) {
	switch kind {
	case "routine", "cr", "qa":
		return "haiku", nil
	case "hard", "security", "gate-signoff":
		return "opus", nil
	default:
		return "", ErrUnknownKind
	}
}

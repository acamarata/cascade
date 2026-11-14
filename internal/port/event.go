package port

// Event is the kernel-level message envelope exchanged through ports. It
// carries the event name, schema version, causation metadata, and an
// arbitrary payload.
type Event struct {
	// Name identifies the event type.
	Name string

	// Version is the schema version of the event payload.
	Version int

	// CausationID identifies the event or command that caused this event.
	CausationID string

	// CausationDepth records how many causal hops led to this event.
	CausationDepth int

	// Payload holds the event-specific data.
	Payload map[string]any
}

// ValidateEventName reports whether name is a valid event name. Naming
// rules are not defined yet, so it always returns ErrUnavailable.
func ValidateEventName(_ string) error {
	return ErrUnavailable
}

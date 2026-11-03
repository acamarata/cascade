package coretools

// CapabilityInboxRead covers listing notifications and reading their bodies.
const CapabilityInboxRead = "inbox.read"

// inboxSpecs declares the session-bound inbox reads. Session scope comes
// from the caller's binding; neither tool can widen it or acknowledge a record.
func inboxSpecs() []Spec {
	return []Spec{
		{
			Name:        "cascade_inbox_list",
			V1Name:      "cascade.inbox.list",
			Description: "List notification summaries visible to the current session. Read-only; does not acknowledge notifications.",
			Method:      "inbox.list",
			Capability:  CapabilityInboxRead,
			Schema: object(nil, map[string]any{
				"unread": boolean("Only list notifications this session has not acknowledged."),
			}),
		},
		{
			Name:        "cascade_inbox_read",
			V1Name:      "NEW-IN-V2:cascade_inbox_read",
			Description: "Read a notification body by id when visible to the current session. Read-only; does not acknowledge it.",
			Method:      "inbox.read",
			Capability:  CapabilityInboxRead,
			Schema: object([]string{"id"}, map[string]any{
				"id": str("Notification id from cascade_inbox_list."),
			}),
		},
	}
}

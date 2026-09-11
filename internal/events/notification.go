// Purpose: the notification-class EventKind and its payload
//
//	encode/decode pair, consumed by internal/notify's router (CHANGE
//	this ticket, P1-E23-W5-S49-T1).
//
// Inputs: EncodeNotificationPayload takes a fully-populated
//
//	NotificationPayload; DecodeNotificationPayload takes arbitrary
//	bytes, including adversarial/truncated ones from a fuzzer.
//
// Outputs: EncodeNotificationPayload never fails; DecodeNotificationPayload
//
//	returns a cascade.KindIntegrity error, never a panic, for any input
//	that is not a well-formed encoding.
//
// Constraints: no bare time.Now; NotificationPayload carries no Timestamp
//
//	field of its own — Notifier.Deliver in internal/notify assigns
//	Timestamp from its injected clock, never from decoded event bytes.
//
// SPORT: internal.events.NotificationPayload/ADDED (P1-E23-W5-S49-T1).

package events

import (
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// NotificationPayload is the wire shape internal/notify's router decodes
// off every mapped producer event (attention.raised, supervision.stalled,
// backup.result, conversation.event, provider.auth.expired, node.offline,
// disk.nearly.full, elevation.required — internal/notify/router.go's
// sourceKindMapping table): everything the router needs to build a
// Notification, other than Priority and Class, which it assigns itself
// from that table, keyed on the publishing event's own Kind.
//
// CONTRACT NOTE: the ticket text names a single wrapper Kind literal
// "notify.notification" ("the notification-class event Kind"), while its
// own normative mapping table is keyed on eight distinct, already-existing
// producer Kinds (attention.raised, supervision.stalled, ...) and states a
// Kind absent from that table is dropped. The two readings conflict: one
// wrapper Kind cannot itself carry eight different table lookups unless
// the payload also duplicated the original Kind for re-dispatch, which no
// field in this ticket's Notification schema provides for. This
// implementation follows the mapping table literally — the router
// subscribes across the eight producer Kinds directly, which is also the
// only reading under which "a Kind absent from this table is dropped with
// a slog WARN" is a meaningful, exercisable statement. No EventKind named
// "notify.notification" is minted or published by this ticket.
type NotificationPayload struct {
	ID            string `json:"id"`
	DeepLink      string `json:"deep_link,omitempty"`
	OriginScope   string `json:"origin_scope,omitempty"`
	TargetScope   string `json:"target_scope,omitempty"`
	TargetSession string `json:"target_session,omitempty"`
	TargetTask    string `json:"target_task,omitempty"`
	Visibility    string `json:"visibility,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	Body          []byte `json:"body,omitempty"`
}

// EncodeNotificationPayload serializes p. It never fails: every
// NotificationPayload value, including the zero value, has a valid JSON
// encoding.
func EncodeNotificationPayload(p NotificationPayload) []byte {
	b, err := json.Marshal(p)
	if err != nil {
		// json.Marshal only fails on channels, funcs, or cyclic maps —
		// none of which NotificationPayload's field types can hold.
		panic("events: NotificationPayload is always marshalable: " + err.Error())
	}
	return b
}

// DecodeNotificationPayload parses raw into a NotificationPayload. It
// returns a cascade.KindIntegrity error, never a panic, for malformed,
// truncated, or otherwise adversarial input — the contract
// FuzzNotificationDecode (internal/notify/fuzz_test.go) exercises.
func DecodeNotificationPayload(raw []byte) (NotificationPayload, error) {
	var p NotificationPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return NotificationPayload{}, cascade.Newf(cascade.KindIntegrity,
			"events: malformed notification payload: %v", err)
	}
	if p.ID == "" {
		return NotificationPayload{}, cascade.New(cascade.KindIntegrity,
			"events: notification payload missing required id")
	}
	return p, nil
}

// Purpose: the single place the fan-out journal entity id is formed and
//   parsed: FanOutEntity(fanoutID) = "fanout:<fanoutID>" and its inverse.
// Inputs: a bare fan-out id, or a journal entity id.
// Outputs: the entity id, or the bare id and whether the entity is a
//   fan-out entity.
// Constraints: no other file builds the "fanout:" prefix by hand
//   (contract:fanout-producer, R8g m-b).
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (P1-CORE-18).

package resume

import "strings"

// fanOutEntityPrefix marks a fan-out's journal entity id.
const fanOutEntityPrefix = "fanout:"

// FanOutEntity returns the journal entity id of the fan-out fanoutID.
func FanOutEntity(fanoutID string) string {
	return fanOutEntityPrefix + fanoutID
}

// FanOutIDFromEntity is FanOutEntity's inverse: it returns the bare
// fan-out id and true for a "fanout:<id>" entity with a non-empty id, and
// ("", false) for any other entity id.
func FanOutIDFromEntity(entityID string) (string, bool) {
	id, ok := strings.CutPrefix(entityID, fanOutEntityPrefix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

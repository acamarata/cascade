// Purpose: FanOutEntity and its inverse round-trip, and the inverse
//   refuses every non-fan-out entity id.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (tests) (P1-CORE-18).

package resume

import "testing"

func TestFanOutEntityRoundTrip(t *testing.T) {
	for _, id := range []string{"01J9Z8Q", "a", "fanout:nested"} {
		entity := FanOutEntity(id)
		if entity != "fanout:"+id {
			t.Fatalf("FanOutEntity(%q) = %q, want %q", id, entity, "fanout:"+id)
		}
		got, ok := FanOutIDFromEntity(entity)
		if !ok || got != id {
			t.Fatalf("FanOutIDFromEntity(%q) = (%q, %v), want (%q, true)", entity, got, ok, id)
		}
	}
	for _, entity := range []string{"", "fanout:", "task-1", "Fanout:x", "xfanout:y"} {
		if got, ok := FanOutIDFromEntity(entity); ok || got != "" {
			t.Fatalf("FanOutIDFromEntity(%q) = (%q, %v), want (\"\", false)", entity, got, ok)
		}
	}
}

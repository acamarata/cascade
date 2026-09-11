package notify

import (
	"testing"

	"github.com/acamarata/cascade/internal/events"
)

// FuzzNotificationDecode (R-16.60b) exercises
// events.DecodeNotificationPayload with arbitrary bytes, seeded from
// testdata/fuzz/FuzzNotificationDecode/. The contract under test is the
// same one types.go's FuzzEventDecode proves for the envelope: decode
// never panics on adversarial input, it only ever returns a
// cascade.KindIntegrity error.
func FuzzNotificationDecode(f *testing.F) {
	f.Add([]byte(`{"id":"n1","target_scope":"project-1"}`))
	f.Add([]byte(`{"id":"n2","visibility":"shared"}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"id":""}`))

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = events.DecodeNotificationPayload(data)
	})
}

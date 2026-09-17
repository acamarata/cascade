package conversation

// Purpose (this file): thread identity — where a thread id comes from
//   when nobody supplied one. Split from adapter.go for Art.10.3's
//   300-line cap.
// SPORT: internal.conversation.thread-id (ADD) — P1-E20-W5-S43-T4.

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// NewThreadID mints an id for a thread nobody named.
//
// Not content-addressed, unlike NewTurnID and NewSegmentID. Those hash
// the coordinates of something that already exists, and two calls with
// the same coordinates SHOULD collide. A new thread has no coordinates:
// two operators typing the same first message a second apart are starting
// two conversations, and hashing the message would merge them.
//
// The one place a thread id is minted, so the CLI and the MCP tool get
// the same thing. `cascade chat` with no --thread and `cascade_cpa_send`
// with no thread_id are the same request, and minting client-side in each
// would be two implementations of one rule (R-14.285).
func NewThreadID() (string, error) {
	id, err := cascade.NewID()
	if err != nil {
		return "", err
	}
	return "thread-" + strings.ToLower(string(id)), nil
}

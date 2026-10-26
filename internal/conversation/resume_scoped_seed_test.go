package conversation_test

// Purpose: the scoped-read fixture's seeder for a caller-named thread.
//   Lives here (not in scoped_reads_test.go) so that file stays under
//   Art.10.3's 300-line cap.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// seedOwnerThread writes a thread the caller named, attributed to the owner,
// the way chat.create_thread would had it let the caller choose the id (it
// does not: ids are daemon-minted, and chat.append_turn refuses a named id
// with no thread row). Any principal read applies the thread_scope schema
// without creating a thread, which Store.CreateThread's attribution needs.
func (f *chatFixture) seedOwnerThread(t *testing.T, threadID string) {
	t.Helper()
	if _, eo := f.call(f.as(t, "project:schema"), "chat.list_threads", map[string]any{}); eo != nil {
		t.Fatalf("apply scope schema: %+v", eo)
	}
	if err := f.store.CreateThread(context.Background(), threadID, provider.SensitivityRestricted, "owner", f.clock.Now().Unix()); err != nil {
		t.Fatal(err)
	}
}

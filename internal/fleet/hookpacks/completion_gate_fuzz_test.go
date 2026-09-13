// Purpose: 06-FORGE-SPEC.md §5 rule 7 -- every parser needs a FuzzXxx
// target. FuzzCompletionHookPayload drives ParseCompletionHookPayload
// (via the live fleet.sessions.completion_check RPC method, the same
// "fuzz the real dispatch path" style FuzzHookPayload above uses) with
// arbitrary bytes and asserts only that it never panics.
// SPORT: fleet/hookpacks.ParseCompletionHookPayload/ADD (P1-E32-W6-S66-T1).
package hookpacks_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

func FuzzCompletionHookPayload(f *testing.F) {
	seed, err := json.Marshal(hookpacks.CompletionHookPayload{
		JobID: "seed-job", TaskID: "seed-task", SessionID: "seed-session", EventType: hookpacks.EventStop,
	})
	if err != nil {
		f.Fatalf("marshal seed: %v", err)
	}
	f.Add(seed)
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))
	f.Add([]byte(`{"job_id":1}`))
	f.Add([]byte(`{"unknown_field":"x"}`))

	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	registry := rpc.NewRegistry()
	if err := hookpacks.RegisterCompletionCheckHandler(registry, clock, bus, fakeGate{ok: true}, fakeResolver{jobs: map[string]bool{"seed-job": true}}, 5*time.Second); err != nil {
		f.Fatalf("RegisterCompletionCheckHandler: %v", err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("FuzzCompletionHookPayload panicked on %q: %v", data, r)
			}
		}()
		_, _ = registry.Dispatch(context.Background(), &rpc.Request{Method: hookpacks.MethodCompletionCheck, Params: json.RawMessage(data)})
	})
}

// Purpose: the recorder's safety properties: a failed lane write never
//
//	changes the call's result, no credential reaches a lane row, a log
//	line or an error, and concurrent calls through one provider stay
//	independent.
//
// Inputs: a harness whose fake request carries a canary credential.
// Outputs: none.
// Constraints: no network; the canary is assembled at run time.
//
// SPORT: internal/providers/dispatch lane_outcome/ADD (P1-WID-11).

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestLaneOutcomeWriteErrorDoesNotChangeCall(t *testing.T) {
	canary := canaryCredential()
	h := newHarness(t, registry.DriverOpenAICompat, registry.LaneRecord{}, canary, fixtureResponse(t, "openai_401_live.json"))
	h.store.upsertErr = cascade.New(cascade.KindUnavailable, "test: registry is locked")
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}

	wrapped := h.resolve(t)
	gotResp, gotErr := wrapped.Chat(context.Background(), req)
	bare := wrapped.(interface{ Unwrap() provider.ModelProvider }).Unwrap()
	wantResp, wantErr := bare.Chat(context.Background(), req)

	if gotErr == nil || wantErr == nil || gotErr.Error() != wantErr.Error() ||
		cascade.HasKind(gotErr, cascade.KindPermissionDenied) != cascade.HasKind(wantErr, cascade.KindPermissionDenied) ||
		gotResp != wantResp {
		t.Fatalf("the recorder changed the call: wrapped (%v, %v), unwrapped (%v, %v)", gotResp, gotErr, wantResp, wantErr)
	}
	if h.store.upserts == 0 {
		t.Fatal("the injected write error was never reached")
	}
	if !strings.Contains(h.logs.String(), `lane=lane-1`) || !strings.Contains(h.logs.String(), "registry is locked") {
		t.Fatalf("the failed write was not logged with its lane and error: %q", h.logs.String())
	}
}

func TestLaneOutcomeCanaryCredentialReachesNothingTheRecorderProduces(t *testing.T) {
	canary := canaryCredential()
	for _, driver := range []registry.DriverKind{registry.DriverOpenAICompat, registry.DriverAnthropic} {
		t.Run(string(driver), func(t *testing.T) {
			h := newHarness(t, driver, registry.LaneRecord{}, canary, scriptedResponse{status: 401, body: `{"error":{"message":"bad key"}}`})
			h.store.upsertErr = errors.New("test: write failed") // forces the log path too
			err := h.chat(t)
			var sent strings.Builder
			for _, headers := range h.tr.sent {
				for _, v := range headers {
					sent.WriteString(v)
				}
			}
			if !strings.Contains(sent.String(), canary) {
				t.Fatal("the canary never rode the request; the scan below would be vacuous")
			}
			h.store.upsertErr = nil
			_ = h.chat(t)
			lane, _ := h.store.lane(t)
			produced := fmt.Sprintf("%+v\n%s\n%v", lane, h.logs.String(), err)
			if strings.Contains(produced, canary) || h.logs.Len() == 0 {
				t.Fatalf("canary leaked into the lane row, log or error, or no log line was produced (%d bytes)", h.logs.Len())
			}
		})
	}
}

// TestLaneOutcomeConcurrentSuccessesOnAvailableLaneWriteNothing runs twelve
// parallel successes through one provider under -race. It proves the
// recorder is data-race free and writes nothing on an available lane. It
// does NOT prove per-call evidence isolation (a slot shared by the
// decorator also writes nothing here); the next test guards that.
func TestLaneOutcomeConcurrentSuccessesOnAvailableLaneWriteNothing(t *testing.T) {
	h := newHarness(t, registry.DriverAnthropic, registry.LaneRecord{}, "cred-value", scriptedResponse{status: 200, body: anthropicOK})
	p := h.resolve(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
			if err != nil {
				t.Errorf("chat: %v", err)
			}
		}()
	}
	wg.Wait()
	if lane, upserts := h.store.lane(t); lane.State != registry.LaneStateAvailable || upserts != 0 {
		t.Fatalf("lane = %+v after %d writes: twelve successes on an available lane must write nothing", lane, upserts)
	}
}

// flipCredentials answers the first Resolve with a key and refuses every
// later one, so one resolved provider can send a request on call 1 and
// fail locally, before any HTTP, on call 2.
type flipCredentials struct {
	mu   sync.Mutex
	used bool
	err  error
}

func (f *flipCredentials) Resolve(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.used {
		return "", f.err
	}
	f.used = true
	return "cred-value", nil
}

// TestLaneOutcomeLaterCallDoesNotInheritEarlierCallsEvidence uses ONE
// resolved provider. Call 1 gets a 401 and ambers the lane. The row is then
// reset to available. Call 2 fails on a credential refusal (the same Kind,
// KindPermissionDenied) with no HTTP at all, so it observed no response and
// must write nothing. A decorator that kept one observation slot across
// calls would carry call 1's 401 into call 2 and re-amber the lane.
func TestLaneOutcomeLaterCallDoesNotInheritEarlierCallsEvidence(t *testing.T) {
	h := newHarness(t, registry.DriverOpenAICompat, registry.LaneRecord{}, "unused", fixtureResponse(t, "openai_401_live.json"))
	h.res.credentials = &flipCredentials{err: cascade.New(cascade.KindPermissionDenied, "test: no live grant")}
	p := h.resolve(t)
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}

	if _, err := p.Chat(context.Background(), req); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("call 1 returned %v, want KindPermissionDenied from the 401", err)
	}
	if lane, upserts := h.store.lane(t); lane.State != registry.LaneStateAuthRequired || upserts != 1 {
		t.Fatalf("after call 1 lane = %+v with %d writes, want auth-required after one write", lane, upserts)
	}
	h.store.mu.Lock()
	h.store.lanes[0].State = registry.LaneStateAvailable // a verified reauth cleared the row
	h.store.mu.Unlock()

	if _, err := p.Chat(context.Background(), req); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("call 2 returned %v, want the credential refusal (KindPermissionDenied)", err)
	}
	if h.tr.calls != 1 {
		t.Fatalf("transport saw %d requests, want 1: call 2 must fail before any HTTP", h.tr.calls)
	}
	if lane, upserts := h.store.lane(t); lane.State != registry.LaneStateAvailable || upserts != 1 {
		t.Fatalf("after call 2 lane = %+v with %d writes, want available and still one write: call 2 saw no response", lane, upserts)
	}
}

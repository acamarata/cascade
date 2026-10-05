// Purpose: the lane-outcome acceptance tests: a real Resolver builds a real
//
//	driver, a scripted Transport replays a fixture or constructed response,
//	and the lane row the recorder leaves behind is asserted - the stored
//	state, never an event.
//
// Inputs: testdata/lane_outcome fixtures and constructed responses.
// Outputs: none.
// Constraints: no network; the clock is fixed; credentials are fakes.
//
// SPORT: internal/providers/dispatch lane_outcome/ADD (P1-WID-11).

package dispatch

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// canaryCredential is assembled at run time so no credential-shaped literal
// sits in source.
func canaryCredential() string { return "canary-" + strings.Repeat("Zq4", 8) + "-value" }

func TestLaneOutcome401SetsAuthRequired(t *testing.T) {
	h := newHarness(t, registry.DriverOpenAICompat, registry.LaneRecord{}, "cred-value", fixtureResponse(t, "openai_401_live.json"))
	err := h.chat(t)
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("the real openai driver returned %v, want KindPermissionDenied", err)
	}
	lane, upserts := h.store.lane(t)
	if lane.State != registry.LaneStateAuthRequired || !lane.ResetEstimate.IsZero() || upserts != 1 {
		t.Fatalf("lane = %+v after %d writes, want auth-required with no reset after one write", lane, upserts)
	}
}

func TestLaneOutcomeGeminiKinds(t *testing.T) {
	t.Run("403 billing disabled writes nothing", func(t *testing.T) {
		h := newHarness(t, registry.DriverGemini, registry.LaneRecord{}, "cred-value", fixtureResponse(t, "gemini_403_billing_disabled_transcribed.json"))
		if err := h.chat(t); !cascade.HasKind(err, cascade.KindCapabilityDenied) {
			t.Fatalf("the real gemini driver returned %v, want KindCapabilityDenied", err)
		}
		lane, upserts := h.store.lane(t)
		if lane.State != registry.LaneStateAvailable || upserts != 0 {
			t.Fatalf("lane = %+v after %d writes: a capability denial must leave an available lane alone", lane, upserts)
		}
	})
	t.Run("400 API_KEY_INVALID writes auth-required", func(t *testing.T) {
		h := newHarness(t, registry.DriverGemini, registry.LaneRecord{}, "cred-value", fixtureResponse(t, "gemini_400_api_key_invalid_constructed.json"))
		if err := h.chat(t); !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("the real gemini driver returned %v, want KindPermissionDenied", err)
		}
		if lane, _ := h.store.lane(t); lane.State != registry.LaneStateAuthRequired {
			t.Fatalf("lane state = %q, want auth-required", lane.State)
		}
	})
}

// quota429 is a CONSTRUCTED anthropic 429 (not captured): the vendor's
// documented rate_limit_error envelope.
const quota429 = `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`

func TestLaneOutcome429WithRetryAfter(t *testing.T) {
	huge := "9999999999999999999999999999"
	cases := []struct {
		name    string
		headers map[string][]string
		want    time.Duration // 0 means no estimate
	}{
		{"120 seconds", map[string][]string{"Retry-After": {"120"}}, 120 * time.Second},
		{"header name case-insensitive", map[string][]string{"retry-after": {"120"}}, 120 * time.Second},
		{"absent", nil, 0},
		{"negative", map[string][]string{"Retry-After": {"-5"}}, 0},
		{"beyond 292 years", map[string][]string{"Retry-After": {huge}}, 7 * 24 * time.Hour},
		{"max int64", map[string][]string{"Retry-After": {"9223372036854775807"}}, 7 * 24 * time.Hour},
		{"exactly 7 days", map[string][]string{"Retry-After": {"604800"}}, 7 * 24 * time.Hour},
		{"HTTP-date is not parsed", map[string][]string{"Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}}, 0},
		{"uncaptured vendor reset header only", map[string][]string{"X-Vendor-Ratelimit-Reset": {"120"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, registry.DriverAnthropic, registry.LaneRecord{}, "cred-value",
				scriptedResponse{status: 429, headers: tc.headers, body: quota429})
			if err := h.chat(t); !cascade.HasKind(err, cascade.KindQuotaExhausted) {
				t.Fatalf("the real anthropic driver returned %v, want KindQuotaExhausted", err)
			}
			lane, _ := h.store.lane(t)
			want := time.Time{}
			if tc.want > 0 {
				want = laneHarnessNow.Add(tc.want)
			}
			if lane.State != registry.LaneStateExhausted || !lane.ResetEstimate.Equal(want) {
				t.Fatalf("lane = state %q reset %v, want exhausted with reset %v", lane.State, lane.ResetEstimate, want)
			}
		})
	}
}

func TestLaneOutcomeSuccessClearsToAvailable(t *testing.T) {
	start := registry.LaneRecord{State: registry.LaneStateAuthRequired, ResetEstimate: laneHarnessNow.Add(time.Hour)}
	h := newHarness(t, registry.DriverAnthropic, start, "cred-value", scriptedResponse{status: 200, body: anthropicOK})
	if err := h.chat(t); err != nil {
		t.Fatalf("chat: %v", err)
	}
	lane, upserts := h.store.lane(t)
	if lane.State != registry.LaneStateAvailable || !lane.ResetEstimate.IsZero() || upserts != 1 {
		t.Fatalf("lane = %+v after %d writes, want available with the estimate cleared", lane, upserts)
	}
}

func TestLaneOutcomeStreamRecordsAtTerminalError(t *testing.T) {
	h := newHarness(t, registry.DriverAnthropic, registry.LaneRecord{}, "cred-value", scriptedResponse{status: 401, body: `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`})
	err := h.resolve(t).Stream(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}},
		func(provider.StreamEvent) error { return nil })
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("stream returned %v, want KindPermissionDenied", err)
	}
	if lane, _ := h.store.lane(t); lane.State != registry.LaneStateAuthRequired {
		t.Fatalf("lane state = %q after a stream 401, want auth-required", lane.State)
	}
}

func TestLaneOutcomeNoWriteWithoutResponse(t *testing.T) {
	denied := cascade.New(cascade.KindPermissionDenied, "test: no live grant")
	cases := []struct {
		name   string
		secret string
		creds  CredentialSource
		script scriptedResponse
	}{
		{"credential source refuses", "x", refusingCredentials{denied}, scriptedResponse{status: 200}},
		{"resolved key is empty", "", nil, scriptedResponse{status: 200}},
		{"transport error", "x", nil, scriptedResponse{err: errAlwaysFails}},
		{"5xx is unavailable", "x", nil, scriptedResponse{status: 503, body: `{"error":{"message":"down"}}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, registry.DriverOpenAICompat, registry.LaneRecord{}, tc.secret, tc.script)
			if tc.creds != nil {
				h.res.credentials = tc.creds
			}
			if err := h.chat(t); err == nil {
				t.Fatal("the call succeeded; the case does not exercise a failure")
			}
			if lane, upserts := h.store.lane(t); lane.State != registry.LaneStateAvailable || upserts != 0 {
				t.Fatalf("lane = %+v after %d writes, want an untouched available lane", lane, upserts)
			}
		})
	}
}

func TestLaneOutcomeRepeatedOutcomeWritesOnceAndKeepsOtherFields(t *testing.T) {
	before := registry.LaneRecord{
		ModelFilter: []string{"m-a", "m-b"}, Weight: 3, PoolMembership: "pool-x", PoolIndex: 2,
		Capacity: registry.CapacityInteractiveUsage, State: registry.LaneStateAvailable,
	}
	h := newHarness(t, registry.DriverOpenAICompat, before, "cred-value", fixtureResponse(t, "openai_401_live.json"))
	before.LaneName, before.ProviderName = "lane-1", h.rec.Name
	for i := 0; i < 3; i++ {
		_ = h.chat(t)
	}
	lane, upserts := h.store.lane(t)
	want := before
	want.State = registry.LaneStateAuthRequired
	if upserts != 1 || !reflect.DeepEqual(lane, want) {
		t.Fatalf("after three identical 401s: %d writes, lane %+v, want 1 write and %+v", upserts, lane, want)
	}
}

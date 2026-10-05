// Purpose: the decorator's other verbs and the recorder's edge cases: Embed,
//
//	Count and Stream record like Chat, Capabilities records nothing, a lane
//	the registry no longer holds or an unreadable registry writes nothing,
//	and a changed reset estimate is rewritten while an equal one is not.
//
// Inputs: the lane-outcome harness.
// Outputs: none.
// Constraints: no network; the clock is fixed.
//
// SPORT: internal/providers/dispatch lane_outcome/ADD (P1-WID-11).

package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestLaneOutcomeEmbedAndCountRecordLikeChat(t *testing.T) {
	t.Run("embed 401 on openai", func(t *testing.T) {
		h := newHarness(t, registry.DriverOpenAICompat, registry.LaneRecord{}, "cred-value", fixtureResponse(t, "openai_401_live.json"))
		_, err := h.resolve(t).Embed(context.Background(), provider.ModelEmbedRequest{Inputs: []string{"a"}, Model: "embed-model"})
		if !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("embed returned %v, want KindPermissionDenied", err)
		}
		if lane, _ := h.store.lane(t); lane.State != registry.LaneStateAuthRequired {
			t.Fatalf("lane state = %q after an embed 401, want auth-required", lane.State)
		}
	})
	t.Run("count 429 on anthropic", func(t *testing.T) {
		h := newHarness(t, registry.DriverAnthropic, registry.LaneRecord{}, "cred-value",
			scriptedResponse{status: 429, headers: map[string][]string{"Retry-After": {"30"}}, body: quota429})
		_, err := h.resolve(t).Count(context.Background(), provider.CountRequest{Text: "hello"})
		if !cascade.HasKind(err, cascade.KindQuotaExhausted) {
			t.Fatalf("count returned %v, want KindQuotaExhausted", err)
		}
		lane, _ := h.store.lane(t)
		if lane.State != registry.LaneStateExhausted || !lane.ResetEstimate.Equal(laneHarnessNow.Add(30*time.Second)) {
			t.Fatalf("lane = %+v after a count 429, want exhausted with a 30s estimate", lane)
		}
	})
}

func TestLaneOutcomeCapabilitiesMakesNoCallAndWritesNothing(t *testing.T) {
	h := newHarness(t, registry.DriverAnthropic, registry.LaneRecord{}, "cred-value", scriptedResponse{status: 401, body: quota429})
	caps, err := h.resolve(t).Capabilities(context.Background(), "lane-1")
	if err != nil || caps.Vision != provider.CapabilitySupported {
		t.Fatalf("capabilities = %+v, %v, want the driver's own descriptor", caps, err)
	}
	if lane, upserts := h.store.lane(t); h.tr.calls != 0 || upserts != 0 || lane.State != registry.LaneStateAvailable {
		t.Fatalf("capabilities sent %d requests and wrote %d times (state %q), want none", h.tr.calls, upserts, lane.State)
	}
}

func TestLaneOutcomeLaneMissingOrRegistryUnreadableWritesNothing(t *testing.T) {
	t.Run("lane no longer stored", func(t *testing.T) {
		h := newHarness(t, registry.DriverOpenAICompat, registry.LaneRecord{}, "cred-value", fixtureResponse(t, "openai_401_live.json"))
		p := h.resolve(t)
		h.store.lanes[0].LaneName = "other-lane"
		if err := chatOn(p); !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("chat returned %v, want the driver's own error", err)
		}
		if _, upserts := h.store.lane(t); upserts != 0 {
			t.Fatalf("%d writes for a lane the registry does not hold, want 0", upserts)
		}
	})
	t.Run("registry unreadable", func(t *testing.T) {
		h := newHarness(t, registry.DriverOpenAICompat, registry.LaneRecord{}, "cred-value", fixtureResponse(t, "openai_401_live.json"))
		p := h.resolve(t)
		h.store.listErr = errors.New("test: registry is unreadable")
		if err := chatOn(p); !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("chat returned %v, want the driver's own error", err)
		}
		if !strings.Contains(h.logs.String(), "registry is unreadable") || h.store.upserts != 0 {
			t.Fatalf("unreadable registry: %d writes, log %q, want 0 writes and the error logged", h.store.upserts, h.logs.String())
		}
	})
}

func chatOn(p provider.ModelProvider) error {
	_, err := p.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	return err
}

func TestLaneOutcomeResetEstimateRewrittenOnlyWhenItChanges(t *testing.T) {
	h := newHarness(t, registry.DriverAnthropic, registry.LaneRecord{}, "cred-value",
		scriptedResponse{status: 429, headers: map[string][]string{"Retry-After": {"60"}}, body: quota429})
	p := h.resolve(t)
	_ = chatOn(p)
	_ = chatOn(p) // same instant, same estimate: no second write
	if _, upserts := h.store.lane(t); upserts != 1 {
		t.Fatalf("%d writes for two identical 429s at one instant, want 1", upserts)
	}
	h.clock.Advance(10 * time.Second)
	_ = chatOn(p) // the estimate moved: rewritten
	lane, upserts := h.store.lane(t)
	if upserts != 2 || !lane.ResetEstimate.Equal(laneHarnessNow.Add(70*time.Second)) {
		t.Fatalf("after the clock moved: %d writes, reset %v, want 2 and now+70s", upserts, lane.ResetEstimate)
	}
	h.tr.responses = []scriptedResponse{{status: 429, body: quota429}}
	_ = chatOn(p) // no header any more: estimate cleared, state unchanged
	if lane, upserts = h.store.lane(t); upserts != 3 || !lane.ResetEstimate.IsZero() || lane.State != registry.LaneStateExhausted {
		t.Fatalf("after a header-less 429: %d writes, lane %+v, want 3 writes, exhausted, no estimate", upserts, lane)
	}
}

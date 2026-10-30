// Purpose (this file): P1-CPA-14 acceptance [0]. A bridged send on a
//   restricted, then a local-only, thread delivers exactly one RefusalEvent
//   to the recording DivergenceSink and puts zero sendMessage calls on the
//   recording Doer; the refused body appears on neither side. Proven over
//   the real dispatch, the real egress engine and the real conversation
//   ThreadPrivacy store (acceptance_helpers_test.go). The inbound twin and
//   the internal-tier contrast keep the refusal from being a universal drop.
//
// SPORT: plugins/cascade-pa/telegram TestPABridge_PrivacyModeRefusal/TEST
//   (P1-CPA-14).

package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// privacyBody is the content a refusal must never leak.
const privacyBody = "the Q4 rollout plan is codenamed nightjar"

// privacyCase is one refused tier and the production reason text for it.
type privacyCase struct {
	name   string
	tier   provider.SensitivityTier
	reason string
	why    string // the egress engine's refusal clause for this tier
}

// refusedTiers is restricted then local-only, the contract's order.
func refusedTiers() []privacyCase {
	return []privacyCase{
		{"restricted", provider.SensitivityRestricted, reasonThreadRestricted,
			"the class was not registered to admit restricted content"},
		{"local-only", provider.SensitivityLocalOnly, reasonThreadLocalOnly,
			"the class was not registered to admit local-only content"},
	}
}

func TestPABridge_PrivacyModeRefusal(t *testing.T) {
	for i, c := range refusedTiers() {
		t.Run(c.name, func(t *testing.T) {
			owner := int64(9300 + i)
			r := newAcceptRig(t, "", "")
			threadID := r.pairOwner(t, owner, c.tier)
			if err := r.bridge.Send(context.Background(), threadID, []byte(privacyBody)); err == nil {
				t.Fatal("Send on a refused thread returned nil")
			}
			if sent := r.sendMessages(); len(sent) != 0 {
				t.Fatalf("sendMessage calls = %d (%+v), want 0", len(sent), sent)
			}
			assertOneRefusal(t, r, threadID, c, "send:"+strconv.FormatInt(owner, 10))
			if n := r.chat.callCount(); n != 0 {
				t.Fatalf("AppendTurn calls = %d, want 0", n)
			}
		})
	}
}

// assertOneRefusal requires exactly one RefusalEvent and one RefusalRecord,
// both naming threadID, the resolved tier and the production reason, and
// neither carrying privacyBody.
func assertOneRefusal(t *testing.T, r *acceptRig, threadID string, c privacyCase, corrID string) {
	t.Helper()
	events := r.divergence.snapshot()
	if len(events) != 1 {
		t.Fatalf("RefusalEvents = %d (%+v), want exactly 1", len(events), events)
	}
	e := events[0]
	if e.ThreadID != threadID || e.ResolvedTier != c.tier.String() || e.Reason != c.reason ||
		e.CorrelationID != corrID || !e.At.Equal(t0()) {
		t.Fatalf("RefusalEvent = %+v, want thread %q tier %q reason %q corr %q", e, threadID, c.tier, c.reason, corrID)
	}
	if strings.Contains(fmt.Sprintf("%+v", e), privacyBody) {
		t.Fatal("the RefusalEvent carries the refused content")
	}
	report := r.bridge.RefusalReport()
	if len(report) != 1 || report[0].Reason != c.reason || report[0].ThreadID != threadID {
		t.Fatalf("RefusalReport = %+v, want one %q record", report, c.reason)
	}
}

// TestPABridge_PrivacyModeRefusal_Inbound is the inbound twin: an owner DM
// on a refused thread is never appended or delivered, emits one
// RefusalEvent, and the recording Doer sees zero sendMessage calls.
func TestPABridge_PrivacyModeRefusal_Inbound(t *testing.T) {
	for i, c := range refusedTiers() {
		t.Run(c.name, func(t *testing.T) {
			owner := int64(9320 + i)
			r := newAcceptRig(t, "", "")
			threadID := r.pairOwner(t, owner, c.tier)
			r.module.dispatch(context.Background(), textUpdate(1, owner, owner, privacyBody))
			assertOneRefusal(t, r, threadID, c, "chat:"+strconv.FormatInt(owner, 10))
			if n := r.chat.callCount(); n != 0 {
				t.Fatalf("AppendTurn calls = %d, want 0", n)
			}
			if sent := r.sendMessages(); len(sent) != 0 {
				t.Fatalf("sendMessage calls = %d, want 0 (privacy fails closed)", len(sent))
			}
			ch, _ := r.bridge.Receive(context.Background())
			if len(ch) != 0 {
				t.Fatalf("Receive holds %d messages, want 0", len(ch))
			}
		})
	}
}

// TestPABridge_PrivacyModeRefusal_AllowPathContrast runs the identical send
// and dispatch on an internal thread: one sendMessage carrying the body to
// the owner, one AppendTurn on the mapped thread, no RefusalEvent.
func TestPABridge_PrivacyModeRefusal_AllowPathContrast(t *testing.T) {
	const owner = int64(9310)
	r := newAcceptRig(t, "", "")
	threadID := r.pairOwner(t, owner, provider.SensitivityInternal)
	if err := r.bridge.Send(context.Background(), threadID, []byte(privacyBody)); err != nil {
		t.Fatalf("Send on an internal thread: %v", err)
	}
	if sent := r.sendMessages(); len(sent) != 1 || sent[0].Text != privacyBody || sent[0].ChatID != owner {
		t.Fatalf("sendMessage calls = %+v, want one carrying the body to the owner", sent)
	}
	r.module.dispatch(context.Background(), textUpdate(1, owner, owner, privacyBody))
	r.chat.mu.Lock()
	calls := append([]appendTurnCall(nil), r.chat.calls...)
	r.chat.mu.Unlock()
	if len(calls) != 1 || calls[0].threadID != threadID || calls[0].text != privacyBody {
		t.Fatalf("AppendTurn calls = %+v, want one on %q", calls, threadID)
	}
	if ev := r.divergence.snapshot(); len(ev) != 0 {
		t.Fatalf("an internal thread was refused: %+v", ev)
	}
}

// TestPABridge_OutboundPrivacyRefusal_Send asserts the refusal Send returns
// by identity and message: a KindPolicyDenied cascade error wrapping the
// egress sentinel ErrSensitivityViolation, with the engine's exact text.
func TestPABridge_OutboundPrivacyRefusal_Send(t *testing.T) {
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassBridge)
	if !ok {
		t.Fatal("the bridge egress class is not registered")
	}
	for i, c := range refusedTiers() {
		t.Run(c.name, func(t *testing.T) {
			r := newAcceptRig(t, "", "")
			threadID := r.pairOwner(t, int64(9330+i), c.tier)
			err := r.bridge.Send(context.Background(), threadID, []byte(privacyBody))
			var ce *cascade.Error
			if !errors.As(err, &ce) || ce.Kind != cascade.KindPolicyDenied {
				t.Fatalf("Send error = %v, want a KindPolicyDenied cascade error", err)
			}
			if ce.Unwrap() != egress.ErrSensitivityViolation {
				t.Fatalf("Send error wraps %v, want egress.ErrSensitivityViolation by identity", ce.Unwrap())
			}
			want := fmt.Sprintf("egress: class %q (owner %s) refused tier %q (resolved %q): %s",
				string(egress.EgressClassBridge), cfg.Owner, c.tier.String(), c.tier.String(), c.why)
			if ce.Msg != want {
				t.Fatalf("Send error message = %q, want %q", ce.Msg, want)
			}
			if strings.Contains(err.Error(), privacyBody) {
				t.Fatal("the refusal error carries the refused content")
			}
		})
	}
}

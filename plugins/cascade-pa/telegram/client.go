// Purpose (this file): BotClient — the long-poll loop and every outbound
//   call, each of which crosses the CONTENT-BEARING egress gate before a
//   byte reaches the transport.
//
// Inputs: a subject id, a Doer (transport seam), an EgressGate, and the
//   durable cascadepa.UpdateLedger.
//
// Outputs: decoded, DEDUPLICATED Updates delivered to onUpdate; outbound
//   text that the firewall has already filtered.
//
// Constraints:
//   - EGRESS IS THE REAL FIREWALL, NOT A TOKEN. Guard takes the sensitivity
//     tier AND the bytes, and returns the bytes that may be written — the
//     same shape internal/hooks/egress.Engine.InterceptClass has, because
//     the production adapter is a direct call to it (see
//     internal/plugins/cascadepa_bridge_wiring.go). An earlier draft's
//     Guard(ctx) took neither, so AllowRestricted:false and
//     AllowedTiers{internal,public} were configured and never consulted on
//     this path, and a stored secret in a reply crossed verbatim. What
//     SendMessage posts is now Guard's RETURN value, so the substitution
//     pass is load-bearing rather than advisory.
//   - This file imports no "net"/"net/http": the real transport lives in
//     transport.go and reaches this package only through Doer, which is
//     what keeps every untagged _test.go beside it free of the net imports
//     Art.7.2 forbids there.
//   - DEDUP IS DURABLE. The offset comes from the ledger, not from an
//     in-memory int, and every update id is checked against the persisted
//     replay window before onUpdate runs: Telegram redelivers unconfirmed
//     updates for 24h, so a restart with an in-memory offset of 0 replayed
//     a day of traffic through the handlers.
//
// SPORT: plugins/cascade-pa/telegram BotClient/ADDED, EgressGate/ADDED
//   (P1-E23-W5-S48-T1).

package telegram

import (
	"context"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// telegramAPIBase is the ONLY host this module ever dials — the exact scope
// of the EgressClassBridge registration this ticket owns. apicall.go's
// TestHTTPDoer host-pin assertions read it back from the built URL, so
// repointing this constant fails the suite instead of silently moving every
// bot call to another server.
const telegramAPIBase = "https://api.telegram.org"

// DefaultPollTimeout is getUpdates' production long-poll wait (the
// "configurable timeout" the contract's task 2 names).
const DefaultPollTimeout = 30 * time.Second

// pollBackoffFloor/Cap bound the delay after a TRANSIENT transport error —
// never a busy-spin, never a wait ctx cancellation cannot interrupt.
const (
	pollBackoffFloor = 1 * time.Second
	pollBackoffCap   = 30 * time.Second
)

// Doer is the transport seam: one Telegram Bot API method call. httpDoer is
// the real implementation; tests use a canned-response fake fed from the
// testdata fixtures.
type Doer interface {
	Do(ctx context.Context, method string, params, out any) error
}

// EgressGate is the bridge's outbound firewall. It is content-bearing on
// purpose: see this file's doc comment.
type EgressGate interface {
	// Guard admits content classified at tier onto the bridge egress class
	// and returns the bytes that may actually be written. A refusal returns
	// an error and NOTHING to write.
	Guard(ctx context.Context, tier cascadepa.SensitivityTier, content []byte) ([]byte, error)
}

// ErrNoEgressCapability is the fail-closed refusal with no gate wired.
var ErrNoEgressCapability = cascade.New(cascade.KindUnavailable,
	"cascade-pa/telegram: no bridge egress capability is wired; every call refuses")

// unconfiguredEgressGate refuses every call, so BotClient never reaches the
// transport until a real capability is wired.
type unconfiguredEgressGate struct{}

func (unconfiguredEgressGate) Guard(
	context.Context, cascadepa.SensitivityTier, []byte,
) ([]byte, error) {
	return nil, ErrNoEgressCapability
}

// BotClient runs the getUpdates long-poll loop and makes every outbound
// call. sleep is the injected backoff primitive (LANE-RULES §8: no bare
// time.Sleep outside tests); production uses sleepOrDone's real timer.
type BotClient struct {
	subject string
	doer    Doer
	egress  EgressGate
	ledger  *cascadepa.UpdateLedger
	sleep   func(ctx context.Context, d time.Duration) bool
}

// NewBotClient constructs a client for subject. A nil egress resolves to
// unconfiguredEgressGate and a nil ledger to a fail-closed one, so neither
// nil can widen what this client admits.
func NewBotClient(subject string, doer Doer, egress EgressGate, ledger *cascadepa.UpdateLedger) *BotClient {
	if egress == nil {
		egress = unconfiguredEgressGate{}
	}
	if ledger == nil {
		ledger = cascadepa.NewUpdateLedger(nil)
	}
	return &BotClient{subject: subject, doer: doer, egress: egress, ledger: ledger, sleep: sleepOrDone}
}

// Poll runs the long-poll loop until ctx is done, calling onUpdate for each
// NEW update in arrival order. It returns nil on a clean ctx cancellation
// (the caller's stop/drain signal) and the typed refusal on a TERMINAL API
// error (401 token rejected, 409 another poller); a transient error backs
// off and retries.
func (c *BotClient) Poll(ctx context.Context, onUpdate func(context.Context, Update)) error {
	backoff := pollBackoffFloor
	for {
		if ctx.Err() != nil {
			return nil
		}
		updates, err := c.fetchOne(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if terminalPollError(err) {
				return err
			}
			if !c.sleep(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = pollBackoffFloor
		c.deliver(ctx, updates, onUpdate)
	}
}

// deliver hands each NEW update to onUpdate. A duplicate (Telegram replay,
// or a proxy resending a batch) and an unreadable ledger both DROP the
// update: processing an update twice is worse than missing one, because the
// handlers behind this are not idempotent.
func (c *BotClient) deliver(ctx context.Context, updates []Update, onUpdate func(context.Context, Update)) {
	for _, u := range updates {
		fresh, err := c.ledger.Accept(ctx, c.subject, u.UpdateID)
		if err != nil || !fresh {
			continue
		}
		onUpdate(ctx, u)
	}
}

// fetchOne performs one egress-gated getUpdates call at the DURABLE offset.
func (c *BotClient) fetchOne(ctx context.Context) ([]Update, error) {
	// Inbound polling holds the same capability every outbound call does
	// (R-21.227: "polling and replies route through the firewall"); there is
	// no content to filter on the request, so the guarded payload is empty
	// and only the capability/tier checks run.
	if _, err := c.egress.Guard(ctx, cascadepa.TierInternal, nil); err != nil {
		return nil, err
	}
	offset, err := c.ledger.Offset(ctx, c.subject)
	if err != nil {
		return nil, err
	}
	var result []Update
	params := getUpdatesParams{
		Offset: offset, Timeout: int(DefaultPollTimeout.Seconds()),
		AllowedUpdates: []string{"message", "callback_query"},
	}
	if err := c.doer.Do(ctx, MethodGetUpdates, params, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SendMessage posts text to chatID after the firewall has passed it. What
// is posted is the firewall's OUTPUT, so a redacted secret is redacted on
// the wire and not merely reported as such.
func (c *BotClient) SendMessage(
	ctx context.Context, chatID int64, tier cascadepa.SensitivityTier, text string,
) error {
	safe, err := c.egress.Guard(ctx, tier, []byte(text))
	if err != nil {
		return err
	}
	return c.doer.Do(ctx, MethodSendMessage, sendMessageParams{ChatID: chatID, Text: string(safe)}, nil)
}

// AnswerCallbackQuery answers a bare callback — the only reply channel a
// callback_query has, since it carries no chat to send into. It crosses the
// identical gate: an outbound byte is an outbound byte.
func (c *BotClient) AnswerCallbackQuery(
	ctx context.Context, callbackID string, tier cascadepa.SensitivityTier, text string,
) error {
	safe, err := c.egress.Guard(ctx, tier, []byte(text))
	if err != nil {
		return err
	}
	return c.doer.Do(ctx, MethodAnswerCallbackQuery,
		answerCallbackQueryParams{CallbackQueryID: callbackID, Text: string(safe)}, nil)
}

// sleepOrDone waits d or until ctx is done, reporting whether it slept the
// full duration (false means ctx ended first — the caller's cue to stop).
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// nextBackoff doubles, capped at pollBackoffCap.
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > pollBackoffCap {
		return pollBackoffCap
	}
	return d
}

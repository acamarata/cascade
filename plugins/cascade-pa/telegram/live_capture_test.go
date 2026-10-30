//go:build integration

// Purpose (this file): the AMD-20260922/3 (R-14.295) / T0 decision R6 owner
//   run — TestTelegramLiveCapture identifies the owner's own chat through a
//   REAL pairing handshake (round-2, T0 RULING R10 -- replacing the prior
//   fixed CASCADE_ACCEPT_TELEGRAM_CHANNEL_ID targeting), then sends one
//   prompt naming the remaining actions live_fixture_test.go's decode proof
//   needs, waits up to ten minutes for all four, and writes the four
//   redacted testdata/getupdates_live_*.json fixtures plus the
//   testdata/README.md provenance entry.
//
// Inputs: CASCADE_ACCEPT_CAPTURE=1 (this test is opt-in even under
//   -tags integration: it is interactive and mutates testdata/, so it must
//   never run inside an ordinary acceptance pass) plus
//   CASCADE_ACCEPT_TELEGRAM_BOT_TOKEN (R1/R10: mapped from the vault by the
//   INVOKING SHELL; the vault variable name never appears in this file).
//   There is no channel env-ref: the owner's chat is established by pairing.
//
// Outputs: TestTelegramLiveCapture. NEVER RUN BY AN AUTOMATED BUILD LANE —
//   this ticket's own build proves it compiles and that its filter/
//   redaction logic is unit-tested indirectly by go vet/compile only, per
//   the ticket's explicit instruction not to run capture mode.
//
// Constraints:
//   - REDACTS IDS AND THE TOKEN. The token never leaves BotClient/httpDoer
//     (apicall.go's own redaction rule). Every numeric id this file WRITES
//     to testdata (chat, sender, message, update) is a fixed synthetic
//     placeholder distinct from every existing fixture's — never the
//     owner's real ids.
//   - OWNER IDENTITY IS ESTABLISHED BY PAIRING (R10), NOT A PRE-KNOWN CHAT.
//     This file mints a one-use code through the REAL pairing path
//     (cascadepa.PairCodeStore.IssueCode), prints it LOCALLY, and admits
//     the FIRST inbound "/pair <code>" message whose code the REAL
//     VerifyAndConsume binds — isPairCommand (pairing.go) is this
//     package's own production parser, reused rather than re-derived
//     (LANE-RULES §5). That message's chat/sender become the ONLY chat
//     every later action (plain text, approve, reject) is accepted from.
//   - NO SECURITY DECISION LIVES HERE beyond the real pairing verify
//     above. The follow-up prompt is fixed, non-sensitive instructional
//     text sent through the real EgressClassBridge gate
//     (acceptance_helpers_test.go's realAcceptEgressGate over a real engine).
//   - NEVER LONG-POLLS AN UNFILTERED BACKLOG. The offset is primed past
//     any backlog (offset=-1 then +1, the identical R10 idiom
//     bridge_acceptance_integration_peek_test.go uses) before the pairing
//     wait begins, and every observed update — pairing included — is
//     filtered before it is ever classified or written.
//
// SPORT: plugins/cascade-pa/telegram TestTelegramLiveCapture/TEST
//   (P1-E23-W5-S49-T4, R-14.295/AMD-20260922-3, T0 RULING R10).

package telegram

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// The env-refs this file reads. captureEnvVar is THIS file's own opt-in
// gate — separate from the ordinary integration acceptance run, which must
// never trip an interactive 10-minute wait or mutate testdata/. There is no
// channel env-ref (R10): the owner's chat comes from pairing.
const (
	captureEnvVar   = "CASCADE_ACCEPT_CAPTURE"
	captureTokenVar = "CASCADE_ACCEPT_TELEGRAM_BOT_TOKEN"
)

// capturePairingTimeout/captureTimeout bound how long this test waits for
// the owner's pairing DM, and then for the remaining three actions.
const (
	capturePairingTimeout = 5 * time.Minute
	captureTimeout        = 10 * time.Minute
)

// capturePollTimeoutSeconds is each individual getUpdates long-poll's own
// server-side wait — short enough that this file's own deadline loop
// re-checks liveliness and ctx cancellation regularly.
const capturePollTimeoutSeconds = 20

// captureRealClock reads the wall clock — this file is a live, interactive
// tool (never a hermetic unit test), so an injected fixed clock would only
// make its own TTL/timeout math lie about real elapsed time.
type captureRealClock struct{}

func (captureRealClock) Now() time.Time { return time.Now() }

// captureResult accumulates the RAW JSON bytes of the first observed
// update of each of the four kinds this capture needs — raw, not a
// re-marshaled Update struct (round-2 fix item 3: a struct round-trip
// silently drops any wire field this package's Update type does not
// declare, which defeats the whole point of a live capture).
type captureResult struct {
	text, pair, approve, reject json.RawMessage
}

// missing names every action not yet observed, in the fixed order the
// prompt lists them.
func (c *captureResult) missing() []string {
	var out []string
	if c.text == nil {
		out = append(out, "a plain text message")
	}
	if c.pair == nil {
		out = append(out, "a \"/pair \" message")
	}
	if c.approve == nil {
		out = append(out, "the Approve button tap")
	}
	if c.reject == nil {
		out = append(out, "the Reject button tap")
	}
	return out
}

// TestTelegramLiveCapture is the interactive owner-run capture. See this
// file's header for the full contract.
func TestTelegramLiveCapture(t *testing.T) {
	if os.Getenv(captureEnvVar) != "1" {
		t.Skipf("owner-run only (P1-E23-W5-S49-T4 R6/R10/AMD-20260922-3): set %s=1 alongside the vault-mapped "+
			"%s to run the interactive capture; never set in an automated build lane", captureEnvVar, captureTokenVar)
	}
	token := captureCredentials(t)
	subject := SubjectFromToken(token)

	ctx, cancel := context.WithTimeout(context.Background(), capturePairingTimeout+captureTimeout+time.Minute)
	defer cancel()

	doer := newAPIDoer(token, newHTTPPoster(nil))
	pairKey, err := cascadepa.DerivePairCodeKey(token)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: kind %v", errKind(err))
	}
	stores := cascadepa.NewStores(captureRealClock{}, &okRegistrar{}, newMemState(), pairKey)

	offset := capturePrimeOffsetPastBacklog(ctx, t, doer)
	code, err := stores.Pairing.IssueCode(ctx, rand.Reader, subject)
	if err != nil {
		t.Fatalf("IssueCode (real pairing path): kind %v", errKind(err))
	}
	t.Logf("PAIRING CODE (valid 10 min, never sent to Telegram by this test): %s", code)
	t.Log("ACTION NEEDED: from your Telegram account, open a DIRECT MESSAGE with the bot and send: /pair " + code)

	pairRaw, ownerID, nextOffset := waitForCapturePairing(ctx, t, doer, stores, subject, offset)
	got := &captureResult{pair: pairRaw}

	_, engine := acceptRealFirewall(t, t.TempDir(), "", "")
	client := NewBotClient(subject, doer, realAcceptEgressGate{engine: engine}, cascadepa.NewUpdateLedger(nil))
	approveData, denyData := "capture-request|approve", "capture-request|reject"
	if _, err := client.sendApprovalButtons(ctx, ownerID, cascadepa.TierInternal,
		capturePromptText(), approveData, denyData); err != nil {
		t.Fatalf("send capture prompt: kind %v", errKind(err))
	}
	t.Logf("capture prompt sent to the paired chat; waiting up to %s for the remaining actions", captureTimeout)

	runCapturePoll(ctx, t, doer, nextOffset, ownerID, approveData, denyData, got)
	if missing := got.missing(); len(missing) != 0 {
		t.Fatalf("capture ended without observing: %s", strings.Join(missing, "; "))
	}
	writeCaptureFixtures(t, got)
	recordCaptureProvenance(t)
}

// captureCredentials reads and validates this file's one env-ref (R10).
func captureCredentials(t *testing.T) (token string) {
	t.Helper()
	token = os.Getenv(captureTokenVar)
	if token == "" {
		t.Fatalf("%s=1 requires %s (mapped from the vault by the invoking shell)", captureEnvVar, captureTokenVar)
	}
	return token
}

// capturePromptText is the fixed instructional message — no summary, no
// owner-identifying content, matching approval_send.go's own "the bridge
// is never an oracle" posture. It no longer asks for the pairing DM: that
// already happened (waitForCapturePairing) before this prompt is sent.
func capturePromptText() string {
	return "cascade acceptance capture (P1-E23-W5-S49-T4): within the next 10 minutes, please do both " +
		"of: (1) send any plain text message here; (2) tap Approve, then Reject, below."
}

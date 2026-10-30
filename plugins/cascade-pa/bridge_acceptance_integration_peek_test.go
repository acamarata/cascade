//go:build integration

// Purpose (this file): bridge_acceptance_integration_helpers_test.go's own
//   raw-HTTP half, split out to stay under Art.10.3's 300-line cap — the
//   recordingRoundTripper that observes sendMessage's real response (R5),
//   and the raw getUpdates/getMe calls this package needs since it cannot
//   reach telegram's unexported single-fetch Doer internals (see that
//   file's header for the full boundary reasoning).
//
// Round-2 rewrite (T0 ruling R10): peekAuthorizedReply/peekedReply/
//   rawResultHasChannelPost — the fixed-CHANNEL_ID peek this file used to
//   carry — are GONE. R10 replaces the channel counterpart with the
//   owner's own bot over the owner's DIRECT MESSAGES, identified by the
//   real pairing handshake (bridge_acceptance_integration_test.go), not by
//   a peeked chat id. What remains here is primeOffsetPastBacklog, the raw
//   getUpdates(offset=-1) then (offset=last+1) sequence R10 names
//   verbatim, so Start()'s real long-poll never touches or answers
//   anything that arrived before this run.
//
// SPORT: plugins/cascade-pa bridge-acceptance-integration-peek/TEST
//   (P1-E23-W5-S49-T4).

package cascadepa_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// recordingRoundTripper wraps a real http.RoundTripper and, for a
// sendMessage call only, records the request body (the echoed-back text
// this test compares against) and the response bytes (message_id) —
// R5's "capture sendMessage's result through a recording RoundTripper"
// — without altering either in any way: RoundTrip always returns exactly
// what the wrapped transport returned.
type recordingRoundTripper struct {
	next http.RoundTripper
	mu   sync.Mutex
	last capturedSend
}

type capturedSend struct {
	requestBody  []byte
	responseBody []byte
	captured     bool
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	isSend := req.URL != nil && strings.HasSuffix(req.URL.Path, "/"+telegram.MethodSendMessage)
	var reqBody []byte
	if isSend && req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	next := r.next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(req)
	if isSend && err == nil && resp != nil && resp.Body != nil {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		r.mu.Lock()
		r.last = capturedSend{requestBody: reqBody, responseBody: respBody, captured: true}
		r.mu.Unlock()
	}
	return resp, err
}

func (r *recordingRoundTripper) snapshot() capturedSend {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// acceptMarker mints a unique, non-guessable marker for one test run's
// outbound message -- unique so this run's own message is findable in a
// shared live bot's history, but never derived from a bare time.Now() (the
// CR-C finding this rework closes): pkg/cascade's own random id generator,
// the same primitive approval_send.go's mintNonce already uses.
func acceptMarker(t *testing.T) string {
	t.Helper()
	id, err := cascade.NewID()
	mustNot(t, "cascade.NewID", err)
	return "cascade-acceptance-" + id.String()
}

// acceptRawEnvelope mirrors the Bot API's own {"ok":...} wrapper for this
// file's raw HTTP calls -- R5's "fail on ok:false" clause: every caller
// below checks OK explicitly rather than assuming a 200 means success.
type acceptRawEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Description string          `json:"description,omitempty"`
}

// acceptRawCallAttempts/Backoff bound the retry this file applies to a
// TRANSPORT-level failure only (a dropped connection, a DNS hiccup) --
// never to an application-level refusal: an ok:false response (R10's own
// "a 409 conflict fails the test with a clear message, never spins")
// decodes successfully at the HTTP layer and is the CALLER's job to fail
// on, hard, with no retry.
const (
	acceptRawCallAttempts = 3
	acceptRawCallBackoff  = 2 * time.Second
)

// acceptRawCall POSTs one Bot API method with body and returns the raw
// response bytes. httpClient nil uses http.DefaultClient. This package
// cannot reach telegram's own unexported single-fetch Doer internals (this
// file's header), so a raw call is the only way to peek/prime a getUpdates
// response without starting the real long-poll.
func acceptRawCall(ctx context.Context, t *testing.T, token string, httpClient *http.Client, method string, body any) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	mustNot(t, "marshal "+method+" request", err)
	url := "https://api.telegram.org/bot" + token + "/" + method
	client := httpClient
	if client == nil {
		client = http.DefaultClient
	}
	var lastErr error
	for attempt := 1; attempt <= acceptRawCallAttempts; attempt++ {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if rerr != nil {
			t.Fatalf("build %s request: %v", method, redact(rerr.Error(), token))
		}
		req.Header.Set("Content-Type", "application/json")
		resp, derr := client.Do(req)
		if derr != nil {
			lastErr = derr
			if attempt < acceptRawCallAttempts && ctx.Err() == nil {
				time.Sleep(acceptRawCallBackoff)
				continue
			}
			t.Fatalf("%s: %v (after %d attempt(s))", method, redact(derr.Error(), token), attempt)
		}
		raw, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			t.Fatalf("read %s response: %v", method, redact(rerr.Error(), token))
		}
		return raw
	}
	t.Fatalf("%s: exhausted %d attempts: %v", method, acceptRawCallAttempts, redact(lastErr.Error(), token))
	return nil
}

// acceptGetMe performs one raw getMe call and decodes it into out,
// hard-failing on ok:false (R5) rather than leaving a zero-value identity
// the caller could mistake for "not yet fetched".
func acceptGetMe(ctx context.Context, t *testing.T, token string, httpClient *http.Client, out *telegram.User) {
	t.Helper()
	raw := acceptRawCall(ctx, t, token, httpClient, "getMe", map[string]any{})
	var env acceptRawEnvelope
	mustNot(t, "decode getMe envelope", json.Unmarshal(raw, &env))
	if !env.OK {
		t.Fatalf("getMe returned ok=false (error_code %d %s)", env.ErrorCode, env.Description)
	}
	mustNot(t, "decode getMe result", json.Unmarshal(env.Result, out))
}

// primeOffsetPastBacklog is R10 verbatim: "prime the offset past existing
// updates first (getUpdates with offset=-1 then +1) so no other chat's
// update is consumed or answered". offset=-1 asks the Bot API for the
// single LAST update outstanding (if any) with no side effect; offset=
// last+1 then acknowledges everything through it server-side, so the real
// long-poll Start() begins next observes nothing from before this call
// ran. It returns the offset the caller's durable ledger should be primed
// to (0 when there is no backlog at all) -- pure observation, never a
// reply, never a dispatch.
func primeOffsetPastBacklog(ctx context.Context, t *testing.T, token string, httpClient *http.Client) int64 {
	t.Helper()
	peekRaw := acceptRawCall(ctx, t, token, httpClient, telegram.MethodGetUpdates,
		map[string]any{"offset": -1, "timeout": 0})
	var peekEnv acceptRawEnvelope
	mustNot(t, "decode getUpdates(offset=-1) envelope", json.Unmarshal(peekRaw, &peekEnv))
	if !peekEnv.OK {
		t.Fatalf("getUpdates(offset=-1) returned ok=false (error_code %d %s)", peekEnv.ErrorCode, peekEnv.Description)
	}
	var updates []telegram.Update
	mustNot(t, "decode getUpdates(offset=-1) result", json.Unmarshal(peekEnv.Result, &updates))
	if len(updates) == 0 {
		return 0
	}
	last := updates[len(updates)-1].UpdateID
	ackRaw := acceptRawCall(ctx, t, token, httpClient, telegram.MethodGetUpdates,
		map[string]any{"offset": last + 1, "timeout": 0})
	var ackEnv acceptRawEnvelope
	mustNot(t, "decode getUpdates(offset=last+1) envelope", json.Unmarshal(ackRaw, &ackEnv))
	if !ackEnv.OK {
		t.Fatalf("getUpdates(offset=%d) returned ok=false (error_code %d %s)", last+1, ackEnv.ErrorCode, ackEnv.Description)
	}
	return last + 1
}

// primeLedgerOffset sets subject's durable offset directly on the test's
// own in-memory BridgeState (integrationState.Load/Save, its sibling
// file), via a real CAS round trip -- the Poll loop that follows therefore
// starts exactly at offset, never touching anything primeOffsetPastBacklog
// already acknowledged (R10/R5).
func primeLedgerOffset(ctx context.Context, t *testing.T, state *integrationState, subject string, offset int64) {
	t.Helper()
	st, ok, err := state.Load(ctx, subject)
	if err != nil {
		t.Fatalf("Load subject state: %v", err)
	}
	if !ok {
		st.Subject = subject
	}
	st.Offset = offset
	if err := state.Save(ctx, st); err != nil {
		t.Fatalf("prime ledger offset: %v", err)
	}
}

// randomNonce mints an 8-character hex nonce for the TG->CLI reply leg
// (R10's own "polls up to 10 minutes ... asserts AppendTurn"). It never
// touches package telegram's pairing entropy path (that is a distinct
// credential, per pairing.go's own doc comment) -- this is a plain,
// unguessable marker to search an inbound reply for, nothing more.
func randomNonce(t *testing.T) string {
	t.Helper()
	id, err := cascade.NewID()
	mustNot(t, "cascade.NewID (nonce)", err)
	s := id.String()
	if len(s) < 8 {
		t.Fatalf("cascade.NewID produced a %d-byte id, want at least 8", len(s))
	}
	return s[:8]
}

// fmtPairInstruction is the one place this file renders the owner-facing
// pairing instruction, so the pairing prompt and any failure message that
// repeats it can never drift apart.
func fmtPairInstruction(code string) string {
	return fmt.Sprintf("reply to the bot's DIRECT MESSAGE thread with: /pair %s", code)
}

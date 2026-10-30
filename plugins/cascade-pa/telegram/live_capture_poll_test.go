//go:build integration

// Purpose (this file): live_capture_test.go's own poll half, split out to
//   stay under Art.10.3's 300-line cap — priming the offset past any
//   backlog, waiting for the owner's real "/pair <code>" DM (verified
//   through the REAL cascadepa.PairCodeStore, never a string compare this
//   file invents), and then polling for the remaining three actions,
//   filtered strictly to that one paired identity throughout (R10: for a
//   Telegram private chat, chat.id and the sender's from.id are the SAME
//   number, so one id serves as both the chat to send into and the sender
//   to filter on).
//
// SPORT: plugins/cascade-pa/telegram live-capture-poll/TEST
//   (P1-E23-W5-S49-T4, T0 RULING R10).

package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// capturePrimeOffsetPastBacklog is the identical R10 idiom
// bridge_acceptance_integration_peek_test.go's primeOffsetPastBacklog
// applies over the cascadepa_test package boundary: getUpdates(offset=-1)
// observes the single last outstanding update with no side effect, then
// getUpdates(offset=last+1) acknowledges everything through it, so the
// pairing wait that follows never sees anything from before this run.
func capturePrimeOffsetPastBacklog(ctx context.Context, t *testing.T, doer Doer) int64 {
	t.Helper()
	var peek []Update
	if err := doer.Do(ctx, MethodGetUpdates, map[string]any{"offset": -1, "timeout": 0}, &peek); err != nil {
		t.Fatalf("getUpdates(offset=-1): kind %v", errKind(err))
	}
	if len(peek) == 0 {
		return 0
	}
	last := peek[len(peek)-1].UpdateID
	var ack []Update
	if err := doer.Do(ctx, MethodGetUpdates, map[string]any{"offset": last + 1, "timeout": 0}, &ack); err != nil {
		t.Fatalf("getUpdates(offset=%d): kind %v", last+1, errKind(err))
	}
	return last + 1
}

// waitForCapturePairing polls from offset until an inbound "/pair <code>"
// message verifies through the REAL stores.Pairing.VerifyAndConsume (the
// same production verifier module.go/pairing.go call, reached here via the
// package-private constructor rather than a duplicated string compare —
// LANE-RULES §5). It returns the pairing message's own raw JSON (one of
// the four required fixtures), the owner's numeric id (chat.id == from.id
// for a private chat), and the offset the caller's own poll should resume
// from.
func waitForCapturePairing(ctx context.Context, t *testing.T, doer Doer, stores *cascadepa.Stores,
	subject string, offset int64) (pairRaw json.RawMessage, ownerID int64, nextOffset int64) {
	t.Helper()
	deadline := time.Now().Add(capturePairingTimeout)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var raws []json.RawMessage
		params := map[string]any{"offset": offset, "timeout": capturePollTimeoutSeconds}
		if err := doer.Do(ctx, MethodGetUpdates, params, &raws); err != nil {
			t.Fatalf("getUpdates while waiting for pairing: kind %v", errKind(err))
		}
		for _, raw := range raws {
			var u Update
			if err := json.Unmarshal(raw, &u); err != nil {
				continue
			}
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if bound, id := tryCapturePairCandidate(ctx, t, stores, subject, u); bound {
				return raw, id, offset
			}
		}
	}
	t.Fatal("no \"/pair <code>\" DM was verified within the pairing window; the printed code above was never redeemed")
	return nil, 0, offset
}

// tryCapturePairCandidate is waitForCapturePairing's per-update body, split
// out for the 50-line function cap: a non-pairing update or a wrong/expired
// code is not fatal (a stray message keeps the wait going), only the
// deadline in the caller's loop is.
func tryCapturePairCandidate(ctx context.Context, t *testing.T, stores *cascadepa.Stores, subject string, u Update) (bool, int64) {
	t.Helper()
	if u.Message == nil || !isOwnerDMMessage(u.Message) {
		return false, 0 // only a private chat whose id is its sender's is ever a pairing candidate
	}
	candidate, isPair := isPairCommand(u.Message.Text)
	if !isPair {
		return false, 0
	}
	outcome, err := stores.Pairing.VerifyAndConsume(ctx, subject, candidate)
	if err != nil {
		t.Fatalf("VerifyAndConsume: kind %v", errKind(err))
	}
	if !outcome.Bound {
		return false, 0
	}
	t.Log("real pairing bound: the owner's \"/pair\" DM was verified by the production PairCodeStore")
	return true, u.Message.Chat.ID
}

// runCapturePoll long-polls getUpdates from offset until every remaining
// action is observed or captureTimeout elapses, filtering every update to
// ownerID before it is ever classified (this file's header: "filtered
// strictly to that one paired identity throughout").
func runCapturePoll(ctx context.Context, t *testing.T, doer Doer, offset, ownerID int64,
	approveData, denyData string, result *captureResult) {
	t.Helper()
	deadline := time.Now().Add(captureTimeout)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var raws []json.RawMessage
		params := map[string]any{"offset": offset, "timeout": capturePollTimeoutSeconds}
		if err := doer.Do(ctx, MethodGetUpdates, params, &raws); err != nil {
			t.Fatalf("getUpdates during capture: kind %v (no retry loop: an ok:false fails the capture)", errKind(err))
		}
		for _, raw := range raws {
			var u Update
			if err := json.Unmarshal(raw, &u); err != nil {
				continue
			}
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			classifyCapturedUpdate(result, raw, u, ownerID, approveData, denyData)
		}
		if len(result.missing()) == 0 {
			return
		}
	}
}

// classifyCapturedUpdate records raw as one of the two remaining pending
// actions (text, approve, reject — pairing was already captured by
// waitForCapturePairing) when it matches AND its chat/sender is the
// authorized owner — every other update is ignored outright, never
// admitted, never replied to.
func classifyCapturedUpdate(result *captureResult, raw json.RawMessage, u Update, ownerID int64, approveData, denyData string) {
	switch {
	case u.Message != nil && isOwnerDMMessage(u.Message) && u.Message.Chat.ID == ownerID:
		classifyCapturedMessage(result, raw, u)
	case ownerCallback(u, ownerID) && u.CallbackQuery.Data == approveData:
		if result.approve == nil {
			result.approve = raw
		}
	case ownerCallback(u, ownerID) && u.CallbackQuery.Data == denyData:
		if result.reject == nil {
			result.reject = raw
		}
	}
}

// classifyCapturedMessage is classifyCapturedUpdate's text half — a
// "/pair" retry is ignored here (already captured, and re-verifying a
// consumed code would only ever refuse).
func classifyCapturedMessage(result *captureResult, raw json.RawMessage, u Update) {
	if strings.HasPrefix(u.Message.Text, "/pair") {
		return
	}
	if u.Message.Text != "" && result.text == nil {
		result.text = raw
	}
}

// ownerCallback: a button tap from the owner on a message in the owner's
// own DM; a tap from any other chat or user is never captured.
func ownerCallback(u Update, ownerID int64) bool {
	cq := u.CallbackQuery
	return cq != nil && cq.From.ID == ownerID && cq.Message != nil && cq.Message.Chat.ID == ownerID
}

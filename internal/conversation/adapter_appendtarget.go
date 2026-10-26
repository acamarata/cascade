package conversation

// Purpose (this file): which thread one chat.append_turn writes to, and
//   how the turn is committed. resolveAppendThread decides the target (it
//   is scopedAppendThread's resolver, scope_attr.go); commitTurn writes it
//   (journaled when a JournalStore is wired).
// Inputs: the decoded appendTurnParams; the built Turn and its Segments.
// Outputs: the thread id, or a typed refusal; nil once committed.
// Constraints: THREAD IDS ARE MINTED DAEMON-SIDE ONLY (R-14.285). An empty
//   thread_id starts a new thread: the daemon mints the id, and the
//   request's privacy_mode (restricted when absent) becomes the thread's
//   marker, written in the SAME transaction as the thread row and its first
//   turn (Store.AppendFirstTurn). A non-empty thread_id must name a thread
//   that already exists (created by chat.create_thread or by an earlier
//   append) and may not carry a privacy_mode at all: a caller-named id is
//   never "creating", so no request can mark a thread the caller named.
//   Every refusal happens before the thread row, marker or turn is written
//   (a creating call's thread_scope attribution row, written earlier by
//   scopedAppendThread, can outlive a later refusal: it is inert, the id is
//   random and never used).
// SPORT: internal.conversation.adapter (chat.append_turn target).

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// resolveAppendThread returns the thread id one append writes to. See this
// file's Constraints for the rules; nothing is written here.
func (a *Adapter) resolveAppendThread(ctx context.Context, params appendTurnParams) (string, error) {
	if params.ThreadID == "" {
		if params.PrivacyMode != "" {
			// An unrecognised tier NAME is refused, not coerced: storing
			// restricted for "local_only" would WIDEN what the caller asked
			// for (restricted permits external lanes, local-only does not).
			if _, err := provider.ParseSensitivityTier(params.PrivacyMode); err != nil {
				return "", cascade.Wrapf(cascade.KindInvalidInput, err,
					"%s: %q is not a sensitivity tier; expected one of local-only, restricted, internal, public",
					MethodAppendTurn, params.PrivacyMode)
			}
		}
		return NewThreadID()
	}
	if params.PrivacyMode != "" {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"%s: privacy_mode may only be set on the request that creates a thread (no thread_id); "+
				"thread %s was named by the caller and its mode is unchanged", MethodAppendTurn, params.ThreadID)
	}
	_, ok, err := a.store.GetThread(ctx, params.ThreadID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", cascade.Newf(cascade.KindNotFound,
			"%s: thread %s does not exist; start a thread with %s or with an append that names no thread_id",
			MethodAppendTurn, params.ThreadID, MethodCreateThread)
	}
	return params.ThreadID, nil
}

// existingTurns returns threadID's stored turns for seq numbering. A thread
// this call is creating has none and is not read.
func (a *Adapter) existingTurns(ctx context.Context, threadID string, creating bool) ([]Turn, error) {
	if creating {
		return nil, nil
	}
	return a.store.ListTurns(ctx, threadID)
}

// firstTurnTier is the marker a creating append writes: the request's
// privacy_mode, already validated by resolveAppendThread, or the explicit
// restricted default (absence is never left to the reader's fallback).
func firstTurnTier(mode string) provider.SensitivityTier {
	if mode == "" {
		return provider.SensitivityRestricted
	}
	return ParseSensitivityTier(mode)
}

// commitTurn writes turn and segments: through Store.AppendFirstTurn (one
// transaction with the thread row and its marker) when creating, else
// through the plain AppendTurn/AppendSegment path. With a JournalStore
// wired the write runs between the journal's intent and ack records
// (AppendTurnJournaled, or appendJournaledWith around the first-turn
// transaction).
func (a *Adapter) commitTurn(ctx context.Context, turn Turn, segments []Segment, creating bool, mode string) error {
	if !creating {
		if a.journal != nil {
			return AppendTurnJournaled(ctx, a.journal, a.store, turn, segments)
		}
		return writeTurn(ctx, a.store, turn, segments)
	}
	tier := firstTurnTier(mode)
	firstTurn := func() error { return a.store.AppendFirstTurn(ctx, tier, turn, segments) }
	if a.journal != nil {
		return appendJournaledWith(ctx, a.journal, turn, segments, firstTurn)
	}
	return firstTurn()
}

// buildTurnSegments builds one append's stored segments WITHOUT writing
// anything: a non-empty LeadingContext becomes segment 0 (SegmentText,
// buildLeadingContextSegment in adapter_wire.go) ahead of the caller's own
// segments, and the whole set passes the S-44.T1 scrub. Every refusal here
// (an unknown segment kind, a scrub failure) therefore happens before the
// thread, its marker or the turn is written.
func (a *Adapter) buildTurnSegments(ctx context.Context, params appendTurnParams, turnID string, now int64) ([]Segment, error) {
	seqOffset := int64(0)
	if params.LeadingContext != "" {
		seqOffset = 1
	}
	segments, err := decodeSegments(params.Segments, turnID, now, seqOffset)
	if err != nil {
		return nil, err
	}
	if params.LeadingContext != "" {
		segments = append([]Segment{buildLeadingContextSegment(turnID, params.LeadingContext, now)}, segments...)
	}
	return a.scrubSegments(ctx, segments) // S-44.T1: before journal/store/SSE
}

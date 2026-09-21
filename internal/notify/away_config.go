// Purpose: AwayController's injected seams and supporting declarations,
//
//	split out of away.go to stay under Art.10.3's 300-line file cap: the
//	AwayDeps collaborator set, the
//	AwayState enum, the [notify] away/digest config plus its validator,
//	the three narrow interfaces the controller consumes (PresenceSource,
//	StallSource, AdmissionIdleSource), the value types they carry
//	(ActivityEvent, StallNotice), the journal payload shape, and the
//	restart-restore seam (ReplayResult, ReplayState).
//
// Inputs: none at this layer — every declaration here is either a value
//
//	object or an interface a composition root implements; ReplayState's
//	only input is an M/S-27.T1 journal.Store.
//
// Outputs: none beyond ReplayState's journal read.
//
// Constraints: this package decodes NO event Kind and consumes NO bus
//
//	payload shape for presence or for stalls. That is deliberate. The
//	contract's named upstream symbols (supervision.Subscribe, event Kind
//	"attention.idle", AdmissionController.IdleState, a
//	"supervision.stalled" payload carrying the stalled notification's
//	id) do not exist in this tree, and machine idleness is not operator
//	presence, so guessing a wire format nobody publishes would repeat
//	the dialect-from-a-paraphrase defect. Presence and stalls are taken
//	as injected interfaces instead; their production implementations
//	land with the wiring ticket named in away.go's header.
//
// SPORT: internal.notify.AwayConfig/ADDED, internal.notify.PresenceSource/ADDED,
//
//	internal.notify.StallSource/ADDED, internal.notify.ReplayState/ADDED
//	(P1-E23-W5-S49-T2).

package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// AwayDeps are AwayController's injected collaborators — the whole set of
// seams this file exists to declare (away.go keeps the state machine that
// consumes them). Router and Clock are required. Digest, Presence, Stalls,
// Admission and Journal may each be nil, and every nil is fail-closed rather
// than fail-open: a nil Admission reads as permanently busy (never confirms
// Away), a nil Presence reports no activity, a nil Stalls yields no notices,
// a nil Journal skips journaling, and a nil Digest skips digest compilation
// while still re-queueing the drained items (away_signals.go).
type AwayDeps struct {
	Router    *NotificationRouter
	Digest    *DigestCompiler
	Presence  PresenceSource
	Stalls    StallSource
	Admission AdmissionIdleSource
	Clock     runtime.Clock
	Journal   journal.Store
	// EntityID is the local node identity (Q/S-36.T1), used as the
	// journal entity id and as the digest's OriginScope.
	EntityID string
	Log      *slog.Logger
}

// AwayState is the closed three-member away-mode state set.
type AwayState int

// The closed AwayState members, in transition order.
const (
	StateActive AwayState = iota
	StateAwayPending
	StateAway
)

// String returns s's lowercase-hyphenated name, or "unknown" for a value
// outside the closed set (never a panic or an empty string).
func (s AwayState) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateAwayPending:
		return "away-pending"
	case StateAway:
		return "away"
	default:
		return "unknown"
	}
}

// AwayConfig holds this ticket's two [notify] hot-reloadable keys
// (08-INIT-CONFIG-SPEC §3; R-14.109 ratifies the row). It is a validated
// value object only: no TOML loader in this tree reads a [notify] section
// yet for EITHER this ticket's keys or S-49.T1's own Config (dispatch.go),
// so "validate before apply" is proven here on the struct — Validate
// refuses an unsafe value before any caller can install it — and the
// loading half belongs to the wiring ticket, not to this one.
type AwayConfig struct {
	// Threshold is [notify.away_threshold]: how long BOTH the presence
	// side and the admission side must stay idle, continuously, before
	// the controller enters Away.
	Threshold time.Duration
	// DigestUrgentDeepLinks is [notify.digest_urgent_deeplinks]: the
	// maximum number of Urgent DeepLinks embedded in one session's
	// digest payload.
	DigestUrgentDeepLinks int
}

// DefaultAwayConfig returns the contract's literal defaults: threshold
// "30m", digest_urgent_deeplinks 10.
func DefaultAwayConfig() AwayConfig {
	return AwayConfig{Threshold: 30 * time.Minute, DigestUrgentDeepLinks: 10}
}

// errInvalidAwayConfig is Validate's typed failure for a config value the
// hot-reload validate-before-apply step must reject before ever applying.
var errInvalidAwayConfig = cascade.New(cascade.KindInvalidInput, "notify: invalid [notify] away config")

// Validate reports whether c is safe to apply: Threshold must be positive
// and DigestUrgentDeepLinks non-negative. Never apply an AwayConfig to a
// running controller without passing this first (08 §3).
func (c AwayConfig) Validate() error {
	if c.Threshold <= 0 {
		return cascade.Wrapf(cascade.KindInvalidInput, errInvalidAwayConfig, "notify: away_threshold must be positive, got %s", c.Threshold)
	}
	if c.DigestUrgentDeepLinks < 0 {
		return cascade.Wrapf(cascade.KindInvalidInput, errInvalidAwayConfig, "notify: digest_urgent_deeplinks must be >= 0, got %d", c.DigestUrgentDeepLinks)
	}
	return nil
}

// PresenceSource reports when the OPERATOR was last observed active. It is
// the only presence signal AwayController consumes, and it is an injected
// interface rather than a bus subscription on purpose: no Kind published
// anywhere in this tree means "the operator is here", and the nearest
// candidates are not substitutes for it — a long single-session compile
// emits no session-change event while the operator is present, and a
// cron-driven session change at 03:00 emits one while the operator is
// asleep. A composition root that can genuinely observe operator activity
// implements this; nothing in this package guesses at it.
type PresenceSource interface {
	// LastActivity returns the instant of the most recent operator
	// activity, or the zero Time when this source has never observed any
	// (which the controller treats as "no news", never as activity).
	LastActivity() time.Time
}

// StallNotice is one stall the R/S-39.T5 escalation side reports to the
// controller, carrying the identity of the notification it concerns. The
// id is required: a notice with an empty NotificationID and an empty
// CorrelationID matches nothing and is reported as such rather than
// silently counted as handled.
type StallNotice struct {
	// NotificationID is the ID of the accumulated notification this
	// stall concerns.
	NotificationID string
	// CorrelationID optionally identifies the wider episode the stall
	// belongs to, matched against an accumulated notification's own
	// CorrelationID. May be empty.
	CorrelationID string
}

// StallSource delivers stall notices to the controller. It is injected for
// the same reason PresenceSource is: R/S-39.T5's detector advances an
// escalation ladder directly and publishes no bus event, and its own stall
// value carries no notification id at all, so there is no payload shape
// for this package to decode. A composition root adapts whatever the
// escalation side really exposes into StallNotices.
type StallSource interface {
	// DrainStallNotices returns every stall notice observed since the
	// previous call and clears the source's pending set, so a notice is
	// delivered to the controller exactly once.
	DrainStallNotices() []StallNotice
}

// ActivityEvent is one operator-activity observation pushed into
// AwayController.HandleEvent by a composition root that learns about
// activity as it happens rather than by polling PresenceSource. It carries
// no event Kind and no bus payload: the controller needs exactly one fact
// from it.
type ActivityEvent struct {
	// At is when the activity happened. The zero value means "now",
	// resolved against the controller's injected clock.
	At time.Time
}

// AdmissionIdleSource is the seam AwayController needs from M/S-26.T2's
// admission controller: the machine side is idle when Inflight()==0 &&
// QueueDepth()==0. This is machine idleness, NOT presence — it is the
// confirming second signal, never a presence substitute.
type AdmissionIdleSource interface {
	Inflight() int
	QueueDepth() int
}

// Compile-time proof that the real M/S-26.T2 admission controller
// structurally satisfies AdmissionIdleSource with no adapter needed.
var _ AdmissionIdleSource = (*governor.AdmissionController)(nil)

// awayJournalPayload is the JSON body of every M/S-27.T1 journal entry
// this controller appends.
type awayJournalPayload struct {
	EpisodeID  string    `json:"episode_id"`
	EnteredAt  time.Time `json:"entered_at,omitempty"`
	ReturnedAt time.Time `json:"returned_at,omitempty"`
}

// ReplayResult is the reconstructed away-mode state a restart installs
// into a new controller via NewAwayControllerFrom. It deliberately carries
// no accumulated notifications: the journal records STATE TRANSITIONS, not
// buffer contents, so after a restart the accumulation buffer starts empty
// and only post-restart notifications enter the resumed episode's digest.
// That gap is recorded rather than papered over with a field nothing can
// honestly fill.
type ReplayResult struct {
	// State is the state the controller was in when the process died.
	State AwayState
	// EpisodeID is the open away-episode id when State is StateAway, so
	// the resumed controller writes its KindAck against the SAME episode
	// the pre-restart KindIntent opened. Empty for any other State.
	EpisodeID string
}

// ReplayState reconstructs away-mode state after a restart by replaying
// entityID's KindIntent/KindAck entries from j. The LAST unmatched
// KindIntent wins: a process killed twice mid-away leaves several open
// episodes, and the newest one is the live one — returning the first would
// resurrect a stale episode forever. A matched pair, or no entries at all,
// means Active.
func ReplayState(ctx context.Context, j journal.Store, entityID string) (ReplayResult, error) {
	entries, err := j.Replay(ctx, entityID, journal.Cursor{}, []journal.Kind{journal.KindIntent, journal.KindAck})
	if err != nil {
		return ReplayResult{State: StateActive}, err
	}
	open := map[string]bool{}
	for _, e := range entries {
		switch e.Kind {
		case journal.KindIntent:
			open[e.OperationID] = true
		case journal.KindAck:
			delete(open, e.OperationID)
		case journal.KindCheckpoint, journal.KindEscalation, journal.KindResumeCursor,
			journal.KindFanOutLegStarted, journal.KindFanOutLegDone, journal.KindNodeStream:
			// Unreachable given Replay's kinds filter above; named for
			// exhaustive-switch compliance only.
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == journal.KindIntent && open[entries[i].OperationID] {
			return ReplayResult{State: StateAway, EpisodeID: entries[i].OperationID}, nil
		}
	}
	return ReplayResult{State: StateActive}, nil
}

// Purpose: DigestCompiler — the S-49.T2 return-digest compiler. On an
//
//	Away->Active transition, AwayController (away.go) calls Compile,
//	which drains the accumulation buffer and delivers ONE ADDRESSED
//	digest per candidate SESSION: Class addressed, TargetSession that
//	session, Visibility private. Each digest is built only from the
//	accumulated items that session was already eligible to receive
//	(R-21.227's per-session compilation); everything else contributes an
//	opaque count with no title, scope name, correlation id or DeepLink.
//
// Inputs: the drained []Notification the return transition snapshotted, the
//
//	live Registry snapshot of candidate sessions (deduplicated by
//	session id — two subscribers on one session are one session, not two
//	digests), and the away-episode id and origin scope AwayController
//	supplies.
//
// Outputs: one Notifier.DeliverNow call per candidate session (none at all
//
//	for an empty drain), plus a re-queue into the router's normal queues of
//	every drained original that no SUCCESSFULLY-delivered digest represented.
//
// Constraints — the two rules that make "no silent discard" true rather
// than claimed:
//   - PRIVACY. The digest is addressed, not global-critical. A
//     global-critical digest is deliverable to every session by
//     ScopeDeliveryPredicate's own rule, so a session-specific digest
//     carrying another project's DeepLink would reach that other project's
//     session: the exact cross-scope leak R-21.227 exists to forbid.
//     Class addressed + TargetSession + Visibility private reaches exactly
//     the one session the digest was built for, through the ordinary
//     predicate, with no new Notification field and no scope.go change.
//   - NO DESTRUCTION. An item leaves the buffer for good only when a
//     digest that ACTUALLY DELIVERED counted it. An item withheld from
//     every registered session (its scope has no session today) is
//     re-queued, so it is still delivered once such a session appears; the
//     "N withheld" count reports it, it does not consume it. A failure for
//     one session re-queues only the items no other session's successful
//     digest already represented, so a delivered digest's items are never
//     double-counted.
//
// SPORT: internal.notify.DigestCompiler/ADDED (P1-E23-W5-S49-T2).

package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// digestPayload is the JSON body of a DigestNotification's Payload field: a
// human-readable Summary plus the machine-usable fields a consuming surface
// needs, so a surface never has to parse free text for the deep links.
type digestPayload struct {
	Summary         string   `json:"summary"`
	UrgentDeepLinks []string `json:"urgent_deep_links,omitempty"`
	Withheld        int      `json:"withheld"`
}

// DigestCompiler compiles and delivers the per-session return digest. The
// zero value is not usable; construct with NewDigestCompiler.
type DigestCompiler struct {
	registry *Registry
	notifier *Notifier
	router   *NotificationRouter
	clock    runtime.Clock
	cfg      AwayConfig
	log      *slog.Logger
}

// NewDigestCompiler returns a DigestCompiler delivering through notifier,
// reading candidate sessions from registry and the accumulation buffer from
// router, capping Urgent DeepLinks per cfg.DigestUrgentDeepLinks.
func NewDigestCompiler(registry *Registry, notifier *Notifier, router *NotificationRouter, clock runtime.Clock, cfg AwayConfig, log *slog.Logger) *DigestCompiler {
	if log == nil {
		log = slog.Default()
	}
	return &DigestCompiler{registry: registry, notifier: notifier, router: router, clock: clock, cfg: cfg, log: log}
}

// errDigestWhileAccumulating refuses a Compile call made while accumulation
// is still on. Compile drains the LIVE buffer, and while the gate is on that
// buffer belongs to an OPEN away episode: draining it would take another
// episode's items and summarise an episode that has not returned. The away
// controller never does this — it turns the gate off and drains inside the
// same critical section, then calls CompileDrained with that snapshot — so a
// bare Compile here is a programming error and is reported as one.
var errDigestWhileAccumulating = cascade.New(cascade.KindInvalidInput,
	"notify: return digest compiled while accumulation is still on")

// Compile drains router's accumulation buffer and delivers one addressed
// digest per candidate session. It is the entry point for a caller that has
// NOT snapshotted the buffer itself, and refuses while accumulation is on;
// AwayController uses CompileDrained instead.
func (dc *DigestCompiler) Compile(ctx context.Context, episodeID, originScope string) error {
	if dc.router.Accumulating() {
		// Refused before draining, so the buffer is left exactly as it was
		// and a correct later call still sees every accumulated item.
		return errDigestWhileAccumulating
	}
	return dc.CompileDrained(ctx, episodeID, originScope, dc.router.DrainAccumulated())
}

// CompileDrained delivers one addressed digest per candidate session from
// drained, the set the return transition snapshotted inside its own critical
// section. It returns the first delivery error encountered, if any. Whatever
// the outcome, every item no successfully-delivered digest represented is
// re-queued into the router's normal queues before it returns.
//
// It does NOT consult the accumulation gate: the episode drained here has
// already returned, so refusing because away mode has since re-entered would
// deny AC#4's digest to an episode that genuinely ended. Delivery goes
// through Notifier.DeliverNow for the same reason — a return summary buffered
// into the NEXT episode would be reduced to a count inside that episode's own
// digest, which is the loss the refusal was trying to prevent.
//
// An EMPTY drain delivers nothing at all. There is no summary to give: no
// item was accumulated, none was withheld, so "no notifications while away"
// as a Priority-Urgent alert to every session on every return is noise, not
// information.
func (dc *DigestCompiler) CompileDrained(ctx context.Context, episodeID, originScope string, drained []Notification) error {
	if len(drained) == 0 {
		return nil
	}
	sessions := dc.candidateSessions()
	if len(sessions) == 0 {
		dc.requeueUnrepresented(drained, nil)
		return nil
	}

	represented := make([]bool, len(drained))
	var firstErr error
	for _, session := range sessions {
		n, inScope := dc.buildFor(session, drained, episodeID, originScope)
		if err := dc.notifier.DeliverNow(ctx, n); err != nil {
			dc.log.Error("notify: digest dispatch failed for session",
				"session", session.SessionID, "episode", episodeID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, i := range inScope {
			represented[i] = true
		}
	}
	dc.requeueUnrepresented(drained, represented)
	return firstErr
}

// requeueUnrepresented re-enqueues into the router's normal per-priority
// queues every drained original that no delivered digest counted: the
// items withheld from every session, and the items belonging only to a
// session whose digest failed to dispatch. A nil represented slice means
// nothing was represented at all. Re-queued items keep their own priority,
// class, scope and expiry, so they are gated by ScopeDeliveryPredicate
// again at the next ordinary Drain and are never re-widened.
func (dc *DigestCompiler) requeueUnrepresented(drained []Notification, represented []bool) {
	for i, n := range drained {
		if represented != nil && represented[i] {
			continue
		}
		dc.router.requeue(n)
	}
}

// candidateSessions returns the registry's distinct candidate sessions,
// ordered by session id for determinism. Registry.Snapshot is per
// REGISTRATION, so two subscribers on one session appear twice; one session
// gets one digest, not one per subscriber. A registration with no session id
// cannot be addressed at all and is skipped with a WARN — its items simply
// stay unrepresented and are re-queued.
func (dc *DigestCompiler) candidateSessions() []CandidateSession {
	seen := map[string]bool{}
	var out []CandidateSession
	for _, reg := range dc.registry.Snapshot() {
		id := reg.Session.SessionID
		if id == "" {
			dc.log.Warn("notify: skipping return digest for a registration with no session id",
				"subscriber", reg.Sub.ID())
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, reg.Session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// buildFor compiles session's own digest from drained and returns it
// together with the drained indices that session was eligible to receive.
// ScopeDeliveryPredicate runs FIRST for every item (R-21.227): in-scope
// items contribute to the priority counts and, if Urgent and carrying a
// DeepLink, to UrgentDeepLinks (capped at cfg.DigestUrgentDeepLinks);
// everything else contributes only to the opaque Withheld count.
func (dc *DigestCompiler) buildFor(session CandidateSession, drained []Notification, episodeID, originScope string) (Notification, []int) {
	counts := map[Priority]int{}
	var urgentLinks []string
	var inScope []int
	for i, item := range drained {
		if !ScopeDeliveryPredicate(session, item) {
			continue
		}
		inScope = append(inScope, i)
		counts[item.Priority]++
		if item.Priority == PriorityUrgent && item.DeepLink != "" && len(urgentLinks) < dc.cfg.DigestUrgentDeepLinks {
			urgentLinks = append(urgentLinks, item.DeepLink)
		}
	}
	withheld := len(drained) - len(inScope)
	return Notification{
		ID:            episodeID + ":" + session.SessionID,
		Source:        "notify.away.digest",
		Priority:      PriorityUrgent,
		Class:         ClassAddressed,
		TargetSession: session.SessionID,
		Visibility:    VisibilityPrivate,
		OriginScope:   originScope,
		CorrelationID: episodeID,
		Payload:       dc.encode(session.SessionID, episodeID, counts, urgentLinks, withheld),
		Timestamp:     dc.clock.Now(),
	}, inScope
}

// encode renders one session's digestPayload as JSON, falling back to a
// minimal valid body (never a nil Payload) if marshalling ever fails.
func (dc *DigestCompiler) encode(sessionID, episodeID string, counts map[Priority]int, urgentLinks []string, withheld int) []byte {
	body, err := json.Marshal(digestPayload{
		Summary:         summarizeCounts(counts, withheld),
		UrgentDeepLinks: urgentLinks,
		Withheld:        withheld,
	})
	if err != nil {
		dc.log.Error("notify: digest payload marshal failed", "session", sessionID, "episode", episodeID, "error", err)
		return []byte(`{"summary":"digest unavailable","withheld":0}`)
	}
	return body
}

// priorityDisplayOrder and priorityDisplayName give summarizeCounts a
// stable, capitalized rendering (the contract's own example: "3 Urgent, 12
// High, 4 Normal while away") independent of Priority.String()'s lowercase
// form (used elsewhere in this package for logging).
var priorityDisplayOrder = []Priority{PriorityUrgent, PriorityHigh, PriorityNormal, PriorityLow}

func priorityDisplayName(p Priority) string {
	switch p {
	case PriorityUrgent:
		return "Urgent"
	case PriorityHigh:
		return "High"
	case PriorityNormal:
		return "Normal"
	case PriorityLow:
		return "Low"
	default:
		return "Unknown"
	}
}

// summarizeCounts renders counts (by Priority) and withheld into the
// digest's human-readable Summary. A priority with zero count is omitted;
// withheld is appended only when non-zero; an entirely empty result reads
// "no notifications while away" rather than an empty string.
func summarizeCounts(counts map[Priority]int, withheld int) string {
	var parts []string
	for _, p := range priorityDisplayOrder {
		if c := counts[p]; c > 0 {
			parts = append(parts, strconv.Itoa(c)+" "+priorityDisplayName(p))
		}
	}
	if withheld > 0 {
		parts = append(parts, strconv.Itoa(withheld)+" withheld")
	}
	if len(parts) == 0 {
		return "no notifications while away"
	}
	return strings.Join(parts, ", ") + " while away"
}

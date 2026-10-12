//go:build !windows

// Purpose: the daemon's stall-escalation composition (P1-SUP-04). It
//
//	gives supervision.NewStallDetector its production caller: the
//	detector's bus loop and its poll loop run under the daemon run context
//	through Manifest.GoSupervised, its human rung files an L2 approval for
//	fleet.session.stall through the daemon's one approval queue, and
//	context.slice gains the directive drain its retry and context rungs
//	deliver through (wire_context_drain.go).
//
// Inputs: the daemon wiring's store, clock, bus, run context, manifest, RPC
//
//	registry, loaded config and policy wiring.
//
// Outputs: supervision.stall and supervision.stall.poll in the manifest, the
//
//	fleet.session.stall capability, and a drain-capable context.slice.
//
// Constraints: a construction error marks supervision.stall Failed and the
//
//	daemon still boots: a missing detector is a missing observation, not a
//	reason to refuse service. A lost detector subscription is recorded
//	Failed, stops the poll loop and files one error attention item. No
//	bare goroutine, no second approval queue, no directive text in logs.
//
// SPORT: cmd/cascade daemon stall supervision (ADDED, P1-SUP-04).
package main

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	stallSubsystem     = "supervision.stall"
	stallPollSubsystem = "supervision.stall.poll"
	stallPollInterval  = 30 * time.Second
	stallRungDelay     = 10 * time.Minute
	stallFailureSource = "subsystem:" + stallSubsystem
	// stallCapability is what a stalled session's human-rung approval is
	// filed under.
	stallCapability = "fleet.session.stall"
)

// stallRuntime is what wireStallDetector takes from outside the wiring: the
// bus the detector subscribes on and the poll ticker. Production passes the
// daemon bus and a system ticker; tests inject a bus they can kill and a
// ticker they drive.
type stallRuntime struct {
	bus       supervision.StallBus
	newTicker func(time.Duration) runtime.Ticker
}

// stallParts is everything buildStall constructs before anything starts.
type stallParts struct {
	detector  *supervision.Detector
	attention *supervision.Store
	drainer   daemon.DirectiveDrainer
}

// wireStallDetector is the supervision-stall registration's Wire.
func wireStallDetector(w *daemonWiring) error {
	return wireStallDetectorWith(w, stallRuntime{bus: w.Bus, newTicker: runtime.NewSystemTicker})
}

// wireStallDetectorWith builds, registers and starts the detector. Only a
// capability registration conflict fails the wiring; every construction
// error is recorded Failed and the daemon boots without the detector.
func wireStallDetectorWith(w *daemonWiring, rt stallRuntime) error {
	if w.Deps.Policy == nil {
		w.Manifest.Failed(stallSubsystem, "no policy wiring: approval queue unavailable")
		return nil
	}
	if err := registerStallCapability(w.Ctx, w.Deps.Policy.Registry); err != nil {
		return err
	}
	parts, err := buildStall(w)
	if err == nil {
		err = registerContextDrain(w, parts.drainer)
	}
	if err != nil {
		w.Manifest.Failed(stallSubsystem, err.Error())
		return nil
	}
	startStall(w, rt, parts)
	return nil
}

// buildStall constructs the detector and its collaborators. Config is read
// once here: [context.hydration] for the rungs' delivery channel and
// budget, [fleet.supervision].stall_threshold for the cutoff.
func buildStall(w *daemonWiring) (stallParts, error) {
	hyd, threshold, err := stallSettings(w.Deps.Config)
	if err != nil {
		return stallParts{}, err
	}
	sess := sessions.New(w.Store, w.Clock, w.Bus)
	directives := supervision.NewDirectiveStore(w.Store, w.Clock)
	attention := supervision.NewStore(w.Store, w.Clock, w.Bus, supervision.NewSystemIDGenerator(), 0)
	det, err := supervision.NewStallDetector(
		journal.New(w.Store, w.Clock, journal.DefaultNamespace),
		supervision.RungConfig{Sessions: sess, Directives: directives, HydrationEnabled: hyd.Enabled, BudgetTokens: hyd.BudgetTokens},
		attention, stallApprovalRequester{queue: w.Deps.Policy.Queue}, stallEscalationPolicy(), threshold, w.Clock,
		supervision.NewBusStallPublisher(w.Bus))
	if err != nil {
		return stallParts{}, err
	}
	return stallParts{detector: det, attention: attention, drainer: daemon.NewLiveDirectiveDrainer(sess, directives)}, nil
}

// stallSettings reads [context.hydration] and [fleet.supervision]
// stall_threshold from cfg. A nil cfg is the documented defaults.
func stallSettings(cfg *runtime.Config) (hydration.Config, time.Duration, error) {
	var extra map[string]interface{}
	if cfg != nil {
		extra = cfg.Extra
	}
	hyd, err := hydration.LoadSection(extra[hydration.ParentSectionName])
	if err != nil {
		return hydration.Config{}, 0, err
	}
	threshold, err := supervision.ParseStallThreshold(fleetSupervisionTable(extra))
	if err != nil {
		return hydration.Config{}, 0, err
	}
	return hyd, threshold, nil
}

// fleetSupervisionTable is extra["fleet"]["supervision"], or nil when the
// table is absent or not a table (ParseStallThreshold then applies its
// default).
func fleetSupervisionTable(extra map[string]interface{}) map[string]interface{} {
	fleet, _ := extra["fleet"].(map[string]interface{})
	sup, _ := fleet["supervision"].(map[string]interface{})
	return sup
}

// stallEscalationPolicy is the ladder the contract fixes: two retries, one
// context grant, one supervisor task, one human approval, ten minutes apart.
func stallEscalationPolicy() governor.EscalationPolicy {
	return governor.EscalationPolicy{
		MaxAttempts: map[governor.EscalationRung]int{
			governor.RungRetry: 2, governor.RungContext: 1,
			governor.RungSupervisorTask: 1, governor.RungHuman: 1,
		},
		RungDelay:           stallRungDelay,
		ConfidenceThreshold: 0.5,
	}
}

// startStall runs the bus loop and the poll loop as supervised subsystems.
// The poll loop's context is cancelled when the bus loop fails, so a dead
// subscription stops polling instead of escalating on stale state.
func startStall(w *daemonWiring, rt stallRuntime, parts stallParts) {
	pollCtx, stopPoll := context.WithCancel(w.Ctx)
	w.Manifest.GoSupervised(w.Ctx, stallSubsystem, "watching fleet sessions", func(ctx context.Context) error {
		defer stopPoll()
		err := parts.detector.Run(ctx, rt.bus)
		if err != nil {
			pushStallFailure(ctx, w, parts.attention, err)
		}
		return err
	})
	w.Manifest.GoSupervised(pollCtx, stallPollSubsystem, "every "+stallPollInterval.String(), func(ctx context.Context) error {
		parts.detector.RunPoll(ctx, rt.newTicker(stallPollInterval))
		return nil
	})
}

// pushStallFailure files the one global error item a lost detector leaves.
// A push failure is logged; the manifest still records the loss.
func pushStallFailure(ctx context.Context, w *daemonWiring, attention *supervision.Store, cause error) {
	_, err := attention.Push(ctx, supervision.AttentionItem{
		Kind:      supervision.KindError,
		SourceRef: stallFailureSource,
		ScopeRef:  scope.Ref{Kind: scope.ScopeKindGlobal},
	})
	if err != nil && w.Logger != nil {
		w.Logger.Error("stall detector failure not filed", "subsystem", stallSubsystem,
			"cause", cause.Error(), "err", cascade.Wrap(cascade.KindUnavailable, err, "attention push").Error())
	}
}

// registerStallCapability adds fleet.session.stall to the daemon's one
// capability registry. An identical existing entry is accepted, so a second
// start over the same registry succeeds; any other error is returned.
func registerStallCapability(ctx context.Context, reg policy.CapabilityRegistry) error {
	want := policy.Capability{
		Name:          stallCapability,
		Desc:          "ask an operator about a fleet session the stall ladder could not recover",
		DefaultPolicy: policy.ClassLocalDev,
	}
	err := reg.Add(ctx, want)
	if err == nil || !cascade.HasKind(err, cascade.KindConflict) {
		return err
	}
	if have, lerr := reg.Lookup(ctx, want.Name); lerr == nil && have == want {
		return nil
	}
	return err
}

// stallApprovalRequester files the human rung's L2 approval in the daemon's
// one approval queue, as the daemon service subject.
type stallApprovalRequester struct {
	queue policy.ApprovalQueue
}

// stallApprovalParams is the approval's canonical parameter object: a fixed
// field order with no maps, so the same event always hashes the same.
type stallApprovalParams struct {
	SessionID      string `json:"session_id"`
	StallKind      string `json:"stall_kind"`
	StalledSince   int64  `json:"stalled_since"`
	ElapsedSeconds int64  `json:"elapsed_seconds"`
}

// RequestApproval implements supervision.ApprovalRequester.
func (r stallApprovalRequester) RequestApproval(ctx context.Context, sessionID string, ev supervision.StallEvent) (string, error) {
	if r.queue == nil {
		return "", cascade.New(cascade.KindUnavailable, "stall approval: no approval queue")
	}
	params, err := json.Marshal(stallApprovalParams{
		SessionID: sessionID, StallKind: string(ev.StallKind),
		StalledSince: ev.StalledSince, ElapsedSeconds: ev.ElapsedSeconds,
	})
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "stall approval: encode params")
	}
	res, err := r.queue.Enqueue(ctx, policy.EnqueueRequest{
		Subject: schedulerSubject(), Capability: stallCapability, Verb: "", Level: policy.L2,
		Action: stallAction(sessionID), Params: params, Summary: stallSummary(sessionID, ev),
	})
	if err != nil {
		return "", err
	}
	return res.RequestID, nil
}

// approvalTextLimit is the approval queue's display limit for Action and
// Summary: it refuses longer text (internal/policy sanitize, 64 bytes).
const approvalTextLimit = 64

// stallAction is "fleet.session.stall <session>", the session id shortened
// only when the whole would pass the queue's display limit. Params carry
// the full id, and they are hashed with the action.
func stallAction(sessionID string) string {
	prefix := stallCapability + " "
	return prefix + fitID(sessionID, approvalTextLimit-len(prefix))
}

// stallSummary is "<session> stalled (<kind>) since <RFC3339|unknown>", the
// session id shortened only when the whole would pass the display limit.
func stallSummary(sessionID string, ev supervision.StallEvent) string {
	since := "unknown"
	if ev.StalledSince > 0 {
		since = time.UnixMilli(ev.StalledSince).UTC().Format(time.RFC3339)
	}
	rest := " stalled (" + string(ev.StallKind) + ") since " + since
	return fitID(sessionID, approvalTextLimit-len(rest)) + rest
}

// fitID returns id when it fits in room bytes, else its longest prefix that
// fits with a "..." marker (never splitting a rune), and at least the marker.
func fitID(id string, room int) string {
	if len(id) <= room {
		return id
	}
	const marker = "..."
	keep := room - len(marker)
	if keep < 0 {
		keep = 0
	}
	for keep > 0 && !utf8.RuneStart(id[keep]) {
		keep--
	}
	return id[:keep] + marker
}

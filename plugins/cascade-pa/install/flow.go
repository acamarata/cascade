package install

// Purpose (this file): the propose -> confirm -> deterministic-install ->
//   resume orchestration. Flow.Run drives S-50.T3's plugin.Resolver, an
//   injected ConfirmGate standing in for S-43.T2's ask-gate, and an
//   injected Installer standing in for the daemon's `plugin.add` RPC
//   (O/S-32.T4) -- the same RPC `cascade plugin add` calls, never a
//   second install code path.
// Inputs: a RunRequest naming the unresolved intent plus the caller-
//   supplied plugin.ManifestSet/plugin.VerifiedIndex snapshot Resolve
//   needs (Art.7: Resolve is a pure function of its arguments, and Run
//   preserves that by taking the same snapshot as an explicit argument
//   rather than reading live state itself).
// Outputs: a RunResult reporting whether the run resumed, or the error
//   Resolve/Confirm/Install/Elevate produced -- AmbiguousIntent and
//   ErrUnverifiedIndex propagate untouched (the caller's disambiguation
//   UX), never swallowed.
// Constraints: fail-closed at every step (Art.2/Art.3): a decline, a
//   failed install, or an unapproved elevation never reaches
//   EventConversationResume. Imports pkg/plugin and pkg/cascade only
//   (Art.10.2); cascade-pa never imports internal/ (R-16.62, R-14.69).
// SPORT: plugins/cascade-pa/install (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// IntentName is the hook action type this flow registers under
// (commands.go's handlers.DispatchIntent routes it here).
const IntentName = "cascade.install_flow"

// Deps are Flow's caller-supplied collaborators. Every field but Snapshot
// is required; tests construct Deps explicitly with fakes. Snapshot is
// the optional seam DispatchIntent uses to source a live ManifestSet/
// VerifiedIndex when invoked over the wire (see RunIntent below) -- a nil
// Snapshot is a documented no-op (zero ManifestSet, nil Index), which
// Resolve already turns into a fail-closed ErrUnverifiedIndex/
// ErrIntentNotFound rather than a fabricated result.
type Deps struct {
	Resolver plugin.Resolver
	Confirm  ConfirmGate
	Install  Installer
	Elevate  Elevator
	Events   EventBus
	// Getenv reads the process environment (CASCADE_NO_INPUT). Nil reads
	// as unset, matching cmd/cascade's own Getenv-dependency convention.
	Getenv   func(string) string
	Snapshot func(ctx context.Context) (plugin.ManifestSet, *plugin.VerifiedIndex, error)
}

// Flow is the conversational install orchestrator. The zero value is not
// usable; construct with NewFlow.
type Flow struct {
	deps Deps
}

// NewFlow returns a Flow over deps.
func NewFlow(deps Deps) *Flow { return &Flow{deps: deps} }

// RunRequest is Flow.Run's input.
type RunRequest struct {
	Intent    string
	Installed plugin.ManifestSet
	Index     *plugin.VerifiedIndex
	// ThreadID names the conversation thread this run was triggered from,
	// when known (round-2 rework, T0 decision D4). Every Event this run
	// publishes carries it (see publish below), so a real EventBus's
	// CLIENT-LOCAL ECHO lands on the SAME thread the operator is already
	// looking at instead of minting a fresh one per event.
	ThreadID string
}

// RunResult is Flow.Run's success-path answer. Resumed is false on every
// non-resuming return (decline, no-input, failure) even when err is nil:
// an explicit decline is not an error (Sec5.9/Sec5.15 treat "the operator
// said no" as a normal, complete outcome).
type RunResult struct {
	Resumed bool
}

// Run drives the four ordered phases documented on this file's header.
// Every branch that does not end in EventConversationResume returns
// before any install side effect, or (for Failed) after one that
// already failed closed.
func (f *Flow) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	candidates, err := f.deps.Resolver.Resolve(ctx, req.Installed, req.Index, req.Intent)
	if err != nil {
		// AmbiguousIntent / ErrUnverifiedIndex / ErrIntentNotFound /
		// ErrIntentEmpty: the caller's disambiguation UX, never swallowed,
		// no install attempted (this file's Constraints).
		return RunResult{}, err
	}
	winner := candidates[0]
	proposal := buildProposal(req.Intent, winner)

	if err := f.publish(ctx, req.ThreadID, Event{Kind: EventInstallProposal, Proposal: &proposal}); err != nil {
		return RunResult{}, err
	}

	if noInput(f.deps.Getenv) {
		_ = f.publish(ctx, req.ThreadID, Event{Kind: EventInstallDeclined,
			Declined: &Declined{PluginID: winner.PluginID, Reason: DeclineNoInput}})
		return RunResult{}, cascade.New(cascade.KindPermissionDenied,
			"cascade-pa install: CASCADE_NO_INPUT=1; confirmation was not attempted, install declined")
	}

	outcome, err := f.deps.Confirm.Confirm(ctx, proposal)
	if err != nil {
		return RunResult{}, err
	}
	if outcome != ConfirmYes {
		reason := DeclineExplicit
		if outcome == ConfirmAmbiguous {
			reason = DeclineAmbiguous
		}
		if err := f.publish(ctx, req.ThreadID, Event{Kind: EventInstallDeclined,
			Declined: &Declined{PluginID: winner.PluginID, Reason: reason}}); err != nil {
			return RunResult{}, err
		}
		return RunResult{}, nil
	}

	result, err := f.install(ctx, req.ThreadID, winner)
	if err != nil {
		return RunResult{}, err
	}

	already := result.Outcome == AddOutcomeAlreadyInstalled
	if err := f.publish(ctx, req.ThreadID, Event{Kind: EventConversationResume,
		Resume: &ResumeEvent{Intent: req.Intent, PluginID: winner.PluginID, AlreadyInstalled: already}}); err != nil {
		return RunResult{}, err
	}
	return RunResult{Resumed: true}, nil
}

// install calls the Installer and, on AddOutcomeElevationRequired, runs
// the elevation phase before retrying exactly once. A failure at the
// initial add, elevation, or the retried add publishes Failed and
// returns a non-nil error -- Run never resumes after this returns one.
// threadID is Run's own req.ThreadID, threaded through so every event this
// phase publishes lands on the same originating thread (T0 decision D4).
func (f *Flow) install(ctx context.Context, threadID string, winner plugin.Candidate) (AddResult, error) {
	result, err := f.deps.Install.Add(ctx, AddRequest{Candidate: winner})
	if err != nil {
		return AddResult{}, f.fail(ctx, threadID, winner.PluginID, err)
	}
	if result.Outcome != AddOutcomeElevationRequired {
		return result, nil
	}

	if err := f.publish(ctx, threadID, Event{Kind: EventElevationRequired,
		Elevation: &ElevationRequired{PluginID: winner.PluginID, Runtime: winner.Manifest.Runtime}}); err != nil {
		return AddResult{}, err
	}
	elevated, err := f.deps.Elevate.Elevate(ctx, ElevationRequest{PluginID: winner.PluginID, Runtime: winner.Manifest.Runtime})
	if err == nil && !elevated.Approved {
		err = cascade.New(cascade.KindElevationRequired,
			"cascade-pa install: local elevation flow did not approve this install; chat confirmation alone cannot satisfy an elevated add")
	}
	if err != nil {
		return AddResult{}, f.fail(ctx, threadID, winner.PluginID, err)
	}

	result, err = f.deps.Install.Add(ctx, AddRequest{Candidate: winner, Witness: elevated.Witness})
	if err != nil {
		return AddResult{}, f.fail(ctx, threadID, winner.PluginID, err)
	}
	return result, nil
}

// fail publishes Failed (value-free: Reason is err's taxonomy
// Kind, never its message) and returns err unchanged.
func (f *Flow) fail(ctx context.Context, threadID, pluginID string, err error) error {
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindInternal
	}
	_ = f.publish(ctx, threadID, Event{Kind: EventInstallFailed, Failed: &Failed{PluginID: pluginID, Reason: kind}})
	return err
}

// publish sets evt.ThreadID to threadID (Run's own req.ThreadID) before
// delivering it, so every event a single run produces carries the SAME
// originating thread (T0 decision D4) -- a caller never sets ThreadID on
// the Event value it builds itself.
func (f *Flow) publish(ctx context.Context, threadID string, evt Event) error {
	evt.ThreadID = threadID
	if f.deps.Events == nil {
		return activeEventBus().Publish(ctx, evt)
	}
	return f.deps.Events.Publish(ctx, evt)
}

// RunIntent is the wire-level entry point: it sources a live ManifestSet/
// VerifiedIndex from Deps.Snapshot (nil Snapshot -> the documented
// zero/nil pair, which Resolve turns into a fail-closed error, never a
// fabricated candidate) and delegates to Run. threadID names the
// conversation thread this intent came from (T0 decision D4); an empty
// threadID (no live wire-level caller supplies one today -- see
// intentInput's own doc comment) still runs, just with no thread to echo
// into.
func (f *Flow) RunIntent(ctx context.Context, intent, threadID string) (RunResult, error) {
	var installed plugin.ManifestSet
	var index *plugin.VerifiedIndex
	if f.deps.Snapshot != nil {
		var err error
		if installed, index, err = f.deps.Snapshot(ctx); err != nil {
			return RunResult{}, err
		}
	}
	return f.Run(ctx, RunRequest{Intent: intent, Installed: installed, Index: index, ThreadID: threadID})
}

// buildProposal renders a resolved Candidate as the Propose phase's
// payload. A registry-sourced candidate has no parsed Manifest yet (the
// registry index never ships one): Version comes from the index's
// LatestVersion and Runtime is left the zero value, since only the
// manifest fetched during install declares it.
func buildProposal(intent string, c plugin.Candidate) Proposal {
	p := Proposal{Intent: intent, PluginID: c.PluginID, Name: c.Name, Source: c.Source}
	if c.Source == plugin.CandidateSourceInstalled {
		p.Version = c.Manifest.Version
		p.Runtime = c.Manifest.Runtime
		p.Permissions = append([]string(nil), c.Manifest.Requires...)
		return p
	}
	p.Version = c.RegistryEntry.LatestVersion
	return p
}

// noInput reports whether CASCADE_NO_INPUT=1 forbids the Confirm phase
// from ever asking (06 Sec5.8). A nil getenv reads as unset, matching
// cmd/cascade's own convention (e.g. plugin.go's pluginNoInput).
func noInput(getenv func(string) string) bool {
	return getenv != nil && getenv("CASCADE_NO_INPUT") == "1"
}

// flowState guards the package-level Flow seam DispatchIntent reaches.
var flowState struct {
	mu sync.RWMutex
	f  *Flow
}

// SetFlow injects the Flow a composition root built over its real
// Resolver/ConfirmGate/Installer/Elevate/Events/Snapshot. Tests call it
// directly, or pass nil to reset.
func SetFlow(f *Flow) {
	flowState.mu.Lock()
	flowState.f = f
	flowState.mu.Unlock()
}

func activeFlow() *Flow {
	flowState.mu.RLock()
	defer flowState.mu.RUnlock()
	return flowState.f
}

// intentInput is the wire shape of the cascade.install_flow intent bytes.
// ThreadID names the originating conversation thread (T0 decision D4);
// disclosed gap, matching this file's own Resolver/VerifiedIndex
// disclosure elsewhere -- no production caller in this tree populates it
// yet (Q5's own disclosed "no live conversation->intent router" finding),
// so it is forward-compatible wiring, not a live behavior change today.
type intentInput struct {
	Intent   string `json:"intent"`
	ThreadID string `json:"thread_id"`
}

// DispatchIntent is the plugin.BuiltinHandlers.DispatchIntent bridge for
// IntentName (commands.go's handlers.DispatchIntent routes here). Without
// a prior SetFlow call it fails closed with KindUnavailable rather than
// panicking or fabricating a result -- wiring the real Flow is the
// composition root's job (internal/plugins, outside this ticket's
// files_scope; see the ticket journal for the recorded deviation).
func DispatchIntent(ctx context.Context, name string, input []byte) ([]byte, error) {
	if name != IntentName {
		return nil, cascade.New(cascade.KindNotFound, "cascade-pa install: unknown intent "+name)
	}
	f := activeFlow()
	if f == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"cascade-pa install: no install flow wired into this session; the composition root has not called install.SetFlow")
	}
	var in intentInput
	if err := json.Unmarshal(input, &in); err != nil || in.Intent == "" {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "cascade-pa install: malformed intent request")
	}
	result, err := f.RunIntent(ctx, in.Intent, in.ThreadID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

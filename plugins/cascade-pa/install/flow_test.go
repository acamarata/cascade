package install

// Purpose (this file): exercises Flow.Run's four phases against the nine
//   acceptance criteria the ticket names -- one fake per collaborator
//   (Resolver/ConfirmGate/Installer/Elevator/EventBus), never a real
//   daemon or adapter, so every assertion is about THIS package's
//   orchestration, not a real counterpart's behavior.
// SPORT: plugins/cascade-pa/install (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

type fakeResolver struct {
	candidates []plugin.Candidate
	err        error
	calls      int
}

func (f *fakeResolver) Resolve(context.Context, plugin.ManifestSet, *plugin.VerifiedIndex, string) ([]plugin.Candidate, error) {
	f.calls++
	return f.candidates, f.err
}

type fakeConfirm struct {
	outcome ConfirmOutcome
	err     error
	calls   int
}

func (f *fakeConfirm) Confirm(context.Context, Proposal) (ConfirmOutcome, error) {
	f.calls++
	return f.outcome, f.err
}

// fakeInstaller answers Add sequentially from results/errs, so a test can
// script an elevation-required response followed by a retry's success.
type fakeInstaller struct {
	results []AddResult
	errs    []error
	calls   int
}

func (f *fakeInstaller) Add(context.Context, AddRequest) (AddResult, error) {
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return AddResult{}, f.errs[i]
	}
	if i < len(f.results) {
		return f.results[i], nil
	}
	return AddResult{}, errors.New("fakeInstaller: no scripted response")
}

type fakeElevator struct {
	result ElevationResult
	err    error
	calls  int
}

func (f *fakeElevator) Elevate(context.Context, ElevationRequest) (ElevationResult, error) {
	f.calls++
	return f.result, f.err
}

type fakeBus struct {
	events []Event
	err    error
}

func (f *fakeBus) Publish(_ context.Context, evt Event) error {
	f.events = append(f.events, evt)
	return f.err
}

func (f *fakeBus) has(kind EventKind) bool {
	for _, e := range f.events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func newCandidate(id string, runtime plugin.RuntimeMode) plugin.Candidate {
	return plugin.Candidate{
		PluginID: id,
		Name:     id,
		Source:   plugin.CandidateSourceInstalled,
		Manifest: plugin.Manifest{ID: id, Name: id, Version: "1.0.0", Runtime: runtime, Requires: []string{"read"}},
	}
}

// deps bundles the five fakes plus a ready-to-use Deps value.
type deps struct {
	resolver *fakeResolver
	confirm  *fakeConfirm
	install  *fakeInstaller
	elevate  *fakeElevator
	bus      *fakeBus
}

func newDeps(cand plugin.Candidate) (*Flow, *deps) {
	d := &deps{
		resolver: &fakeResolver{candidates: []plugin.Candidate{cand}},
		confirm:  &fakeConfirm{outcome: ConfirmYes},
		install:  &fakeInstaller{results: []AddResult{{Outcome: AddOutcomeInstalled}}},
		elevate:  &fakeElevator{},
		bus:      &fakeBus{},
	}
	f := NewFlow(Deps{Resolver: d.resolver, Confirm: d.confirm, Install: d.install, Elevate: d.elevate, Events: d.bus})
	return f, d
}

func TestConversationalInstallHappyPath(t *testing.T) {
	cand := newCandidate("git-tools", plugin.RuntimeBuiltin)
	f, d := newDeps(cand)

	result, err := f.Run(context.Background(), RunRequest{Intent: "git push"})
	if err != nil || !result.Resumed {
		t.Fatalf("Run() = (%+v, %v), want resumed with no error", result, err)
	}
	if len(d.bus.events) == 0 || d.bus.events[0].Kind != EventInstallProposal {
		t.Fatalf("first event = %v, want EventInstallProposal before any install side effect", d.bus.events)
	}
	if last := d.bus.events[len(d.bus.events)-1]; last.Kind != EventConversationResume {
		t.Fatalf("last event = %v, want EventConversationResume", last)
	}
	if d.install.calls != 1 {
		t.Fatalf("Installer.Add called %d times, want exactly 1", d.install.calls)
	}

	t.Run("EventBusFailureAbortsBeforeInstall", func(t *testing.T) {
		f, d := newDeps(cand)
		d.bus.err = errors.New("adapter unreachable")
		if _, err := f.Run(context.Background(), RunRequest{Intent: "git push"}); err == nil {
			t.Fatal("Run() = nil error, want the echo failure propagated")
		}
		if d.confirm.calls != 0 || d.install.calls != 0 {
			t.Fatalf("confirm/install called (%d/%d) after a failed CLIENT-LOCAL ECHO, want zero",
				d.confirm.calls, d.install.calls)
		}
	})
}

func TestConversationalInstallUserDecline(t *testing.T) {
	cand := newCandidate("git-tools", plugin.RuntimeBuiltin)
	f, d := newDeps(cand)
	d.confirm.outcome = ConfirmDeclined

	result, err := f.Run(context.Background(), RunRequest{Intent: "git push"})
	if err != nil || result.Resumed {
		t.Fatalf("Run() = (%+v, %v), want a clean non-resuming return", result, err)
	}
	if d.install.calls != 0 {
		t.Fatalf("Installer.Add called %d times on decline, want 0 (no side effect)", d.install.calls)
	}
	if !d.bus.has(EventInstallDeclined) || d.bus.has(EventConversationResume) {
		t.Fatalf("events = %v, want Declined and no resume", d.bus.events)
	}
}

func TestConversationalInstallFailure(t *testing.T) {
	cand := newCandidate("git-tools", plugin.RuntimeBuiltin)
	f, d := newDeps(cand)
	d.install.results = nil
	d.install.errs = []error{cascade.New(cascade.KindUnavailable, "daemon unreachable")}

	_, err := f.Run(context.Background(), RunRequest{Intent: "git push"})
	if err == nil {
		t.Fatal("Run() = nil error, want the install failure propagated")
	}
	if !d.bus.has(EventInstallFailed) || d.bus.has(EventConversationResume) {
		t.Fatalf("events = %v, want Failed and no resume (fail-closed)", d.bus.events)
	}

	t.Run("ConfirmError", func(t *testing.T) {
		f, d := newDeps(cand)
		d.confirm.err = cascade.New(cascade.KindUnavailable, "ask-gate unreachable")
		if _, err := f.Run(context.Background(), RunRequest{Intent: "git push"}); err == nil {
			t.Fatal("Run() = nil error, want the ask-gate failure propagated")
		}
		if d.install.calls != 0 {
			t.Fatalf("Installer.Add called %d times after an ask-gate error, want 0", d.install.calls)
		}
	})
}

func TestConversationalInstallIdempotent(t *testing.T) {
	cand := newCandidate("git-tools", plugin.RuntimeBuiltin)
	f, d := newDeps(cand)
	d.install.results = []AddResult{{Outcome: AddOutcomeAlreadyInstalled}, {Outcome: AddOutcomeAlreadyInstalled}}

	for i := 0; i < 2; i++ {
		result, err := f.Run(context.Background(), RunRequest{Intent: "git push"})
		if err != nil || !result.Resumed {
			t.Fatalf("call %d: Run() = (%+v, %v), want resumed with no error", i, result, err)
		}
	}
	if d.install.calls != 2 {
		t.Fatalf("Installer.Add called %d times, want one attempt per invocation (delta reported each time)", d.install.calls)
	}
	last := d.bus.events[len(d.bus.events)-1]
	if last.Kind != EventConversationResume || last.Resume == nil || !last.Resume.AlreadyInstalled {
		t.Fatalf("last event = %v, want a resume reporting AlreadyInstalled", last)
	}
}

func TestConversationalInstallNoInput(t *testing.T) {
	cand := newCandidate("git-tools", plugin.RuntimeBuiltin)
	f, d := newDeps(cand)
	f.deps.Getenv = func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}

	_, err := f.Run(context.Background(), RunRequest{Intent: "git push"})
	if err == nil {
		t.Fatal("Run() = nil error under CASCADE_NO_INPUT=1, want a clear auto-decline error")
	}
	if d.confirm.calls != 0 {
		t.Fatalf("ConfirmGate.Confirm called %d times under CASCADE_NO_INPUT=1, want 0 (never asked)", d.confirm.calls)
	}
	if !d.bus.has(EventInstallDeclined) {
		t.Fatalf("events = %v, want Declined(no_input)", d.bus.events)
	}
}

func TestConversationalInstallAmbiguous(t *testing.T) {
	cand := newCandidate("git-tools", plugin.RuntimeBuiltin)
	f, d := newDeps(cand)
	ambErr := &plugin.AmbiguousIntent{Intent: "git", Candidates: []plugin.Candidate{cand, cand}, Tied: 2}
	d.resolver.err = ambErr
	d.resolver.candidates = nil

	_, err := f.Run(context.Background(), RunRequest{Intent: "git"})
	var got *plugin.AmbiguousIntent
	if !errors.As(err, &got) || got != ambErr {
		t.Fatalf("Run() error = %v, want the exact *plugin.AmbiguousIntent propagated untouched", err)
	}
	if d.confirm.calls != 0 || d.install.calls != 0 || len(d.bus.events) != 0 {
		t.Fatalf("side effects after ambiguous resolve: confirm=%d install=%d events=%v, want none",
			d.confirm.calls, d.install.calls, d.bus.events)
	}

	t.Run("ErrUnverifiedIndex", func(t *testing.T) {
		f, d := newDeps(cand)
		d.resolver.err = plugin.ErrUnverifiedIndex
		d.resolver.candidates = nil
		_, err := f.Run(context.Background(), RunRequest{Intent: "git"})
		if err != plugin.ErrUnverifiedIndex { //nolint:errorlint // identity compare is this sentinel's documented contract
			t.Fatalf("Run() error = %v, want plugin.ErrUnverifiedIndex by identity", err)
		}
		if d.install.calls != 0 {
			t.Fatalf("Installer.Add called %d times, want 0 (no install attempted)", d.install.calls)
		}
	})
}

func TestConversationalInstallProcessTierElevation(t *testing.T) {
	cand := newCandidate("agent-runner", plugin.RuntimeProcess)
	f, d := newDeps(cand)
	d.install.results = []AddResult{{Outcome: AddOutcomeElevationRequired}}
	d.elevate.result = ElevationResult{Approved: false}

	_, err := f.Run(context.Background(), RunRequest{Intent: "run agent"})
	if err == nil {
		t.Fatal("Run() = nil error on an unapproved elevation, want it refused")
	}
	if !d.bus.has(EventElevationRequired) || !d.bus.has(EventInstallFailed) || d.bus.has(EventConversationResume) {
		t.Fatalf("events = %v, want ElevationRequired+Failed and no resume", d.bus.events)
	}
	if d.install.calls != 1 {
		t.Fatalf("Installer.Add called %d times, want exactly 1 (no retry without approval)", d.install.calls)
	}

	t.Run("ApprovedElevationRetrySucceeds", func(t *testing.T) {
		f, d := newDeps(cand)
		d.install.results = []AddResult{{Outcome: AddOutcomeElevationRequired}, {Outcome: AddOutcomeInstalled}}
		d.elevate.result = ElevationResult{Approved: true}

		result, err := f.Run(context.Background(), RunRequest{Intent: "run agent"})
		if err != nil || !result.Resumed {
			t.Fatalf("Run() = (%+v, %v), want resumed once elevation is approved", result, err)
		}
		if d.install.calls != 2 || d.elevate.calls != 1 {
			t.Fatalf("install/elevate calls = %d/%d, want exactly one retry after one elevation", d.install.calls, d.elevate.calls)
		}
	})
}

package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ev builds a frame from the dispatcher's own payload type, so a tag drift
// between streamEventPayload and watchFrame fails here.
func ev(t *testing.T, kind events.EventKind, job, cp string, k RequirementKind, acceptance, passed bool) CIEventFrame {
	t.Helper()
	data, err := json.Marshal(streamEventPayload{JobID: job, CheckpointID: cp, Kind: string(k), Acceptance: acceptance, Passed: passed})
	if err != nil {
		t.Fatal(err)
	}
	return CIEventFrame{Event: string(kind), Data: data}
}

// plan builds a plan frame listing kinds.
func plan(t *testing.T, job, cp string, acceptance bool, kinds ...string) CIEventFrame {
	t.Helper()
	data, err := json.Marshal(streamEventPayload{JobID: job, CheckpointID: cp, Acceptance: acceptance, Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	return CIEventFrame{Event: string(EventKindCheckpointPlanned), Data: data}
}

var replayed = CIEventFrame{Event: watchReplayedEvent}

const (
	disp = EventKindCheckpointDispatched
	term = EventKindCheckpointTerminal
)

// runWatch runs `ci watch job-1` over frames. Sends are unbuffered, so a
// completed send means the watch consumed the frame; a trailing frame of
// another job proves it was still reading after the last frame of
// interest. closeAfter closes the channel (a disconnect).
func runWatch(t *testing.T, frames []CIEventFrame, closeAfter bool) (stdout string, returned bool, err error) {
	t.Helper()
	ch := make(chan CIEventFrame)
	closed := false
	cmd := NewCICmd(CmdDeps{WatchEvents: func(context.Context, string) (<-chan CIEventFrame, func(), error) {
		return ch, func() { closed = true }, nil
	}})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"watch", "job-1"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	feed := append(append([]CIEventFrame{}, frames...), ev(t, disp, "job-other", "cp-x", RequirementLint, true, false))
	for _, f := range feed {
		select {
		case ch <- f:
		case err = <-done:
			return out.String(), true, err
		case <-time.After(5 * time.Second):
			t.Fatal("watch stopped reading")
		}
	}
	if !closeAfter {
		select {
		case err = <-done:
			return out.String(), true, err
		default:
			return out.String(), false, nil
		}
	}
	close(ch)
	select {
	case err = <-done:
		if !closed {
			t.Error("watch returned without running the stream closer")
		}
		return out.String(), true, err
	case <-time.After(5 * time.Second):
		return out.String(), false, nil
	}
}

func TestCIWatchExitsOnAcceptanceTerminal(t *testing.T) {
	frames := []CIEventFrame{
		replayed,
		plan(t, "job-1", "cp-1", false, "lint"),
		ev(t, term, "job-1", "cp-1", RequirementLint, false, true),
		plan(t, "job-2", "cp-9", true, "unit"),
		ev(t, term, "job-2", "cp-9", RequirementUnit, true, true),
		plan(t, "job-1", "cp-2", true, "lint", "unit"),
		ev(t, disp, "job-1", "cp-2", RequirementLint, true, false),
		ev(t, disp, "job-1", "cp-2", RequirementUnit, true, false),
		ev(t, term, "job-1", "cp-2", RequirementLint, true, true),
	}
	last := ev(t, term, "job-1", "cp-2", RequirementUnit, true, true)
	out, returned, err := runWatch(t, append(frames, last), false)
	if !returned || err != nil {
		t.Fatalf("every planned kind terminal and passed: returned=%v err=%v, want exit 0", returned, err)
	}
	if !strings.Contains(out, "ci.checkpoint.planned checkpoint=cp-2 kinds=lint,unit acceptance=true") ||
		!strings.Contains(out, "ci.checkpoint.terminal checkpoint=cp-2 kind=unit acceptance=true passed=true") || strings.Contains(out, "cp-9") {
		t.Fatalf("output must list job-1's frames only:\n%s", out)
	}
	if _, returned, err := runWatch(t, frames, false); returned {
		t.Fatalf("one planned kind not terminal: watch returned %v, want it still waiting", err)
	}
	_, returned, err = runWatch(t, frames, true)
	if !returned || output.ExitCode(err) != 1 {
		t.Fatalf("closed channel: returned=%v exit=%d (%v), want exit 1", returned, output.ExitCode(err), err)
	}
}

// The race the review found: the fast kind is dispatched and terminal
// before the slow kind's dispatched event exists. The plan still lists it.
func TestCIWatchWaitsForSlowKind(t *testing.T) {
	frames := []CIEventFrame{
		replayed,
		plan(t, "job-1", "cp-2", true, "lint", "unit"),
		ev(t, disp, "job-1", "cp-2", RequirementLint, true, false),
		ev(t, term, "job-1", "cp-2", RequirementLint, true, true),
	}
	if _, returned, err := runWatch(t, frames, false); returned {
		t.Fatalf("watch returned %v with the slow kind not started, want it still waiting", err)
	}
}

func TestCIWatchFailedKindExitsNonZero(t *testing.T) {
	frames := []CIEventFrame{
		replayed,
		plan(t, "job-1", "cp-2", true, "lint", "unit"),
		ev(t, term, "job-1", "cp-2", RequirementLint, true, true),
		ev(t, term, "job-1", "cp-2", RequirementUnit, true, false),
	}
	_, returned, err := runWatch(t, frames, false)
	if !returned || output.ExitCode(err) != 1 || !strings.Contains(err.Error(), "unit") {
		t.Fatalf("a failed planned kind: returned=%v err=%v, want exit 1 naming unit", returned, err)
	}
}

func TestCIWatchAttachAfterRunEnded(t *testing.T) {
	ended := []CIEventFrame{
		plan(t, "job-1", "cp-2", true, "lint"),
		ev(t, term, "job-1", "cp-2", RequirementLint, true, true),
	}
	if _, returned, err := runWatch(t, ended, false); returned {
		t.Fatalf("a verdict was taken during the replay: %v", err)
	}
	if _, returned, err := runWatch(t, append(ended, replayed), false); !returned || err != nil {
		t.Fatalf("a run that ended before the watch: returned=%v err=%v, want exit 0 at the end of the replay", returned, err)
	}
	newer := append(append([]CIEventFrame{}, ended...), plan(t, "job-1", "cp-3", true, "lint"), replayed)
	if _, returned, err := runWatch(t, newer, false); returned {
		t.Fatalf("an older passed run answered for a newer running one: %v", err)
	}
}

func TestCIWatchRefusesUnreadableFrame(t *testing.T) {
	cases := map[string][]CIEventFrame{
		"bad json":       {{Event: string(term), Data: []byte("{")}},
		"unknown kind":   {ev(t, term, "job-1", "cp-2", RequirementKind("bogus"), true, true)},
		"no checkpoint":  {ev(t, term, "job-1", "", RequirementLint, true, true)},
		"empty plan":     {plan(t, "job-1", "cp-2", true)},
		"outside plan":   {plan(t, "job-1", "cp-2", true, "lint"), ev(t, term, "job-1", "cp-2", RequirementUnit, true, true)},
		"unknown plan k": {plan(t, "job-1", "cp-2", true, "lint", "bogus")},
	}
	for name, frames := range cases {
		_, returned, err := runWatch(t, frames, false)
		if !returned || output.ExitCode(err) != 1 {
			t.Errorf("%s: returned=%v err=%v, want exit 1", name, returned, err)
		}
	}
}

func TestDispatchPublishesPlanFirst(t *testing.T) {
	r := newRig(t)
	r.useLocal(&lockedExec{}, nil)
	r.commit(map[string]string{"docs/a.md": "a"})
	if _, err := r.checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	all, err := r.bus.Replay(context.Background(), EventNamespace, 0)
	if err != nil {
		t.Fatal(err)
	}
	var planSeq uint64
	for _, e := range all {
		if e.Kind == EventKindCheckpointPlanned {
			var p streamEventPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil || strings.Join(p.Kinds, ",") != "format,lint" || p.JobID != "job-1" {
				t.Fatalf("plan payload %s (%v), want job-1 kinds format,lint", e.Payload, err)
			}
			planSeq = e.Seq
		} else if (e.Kind == EventKindCheckpointDispatched || e.Kind == EventKindCheckpointTerminal) && planSeq == 0 {
			t.Fatalf("%s at seq %d precedes the plan event", e.Kind, e.Seq)
		}
	}
	if planSeq == 0 {
		t.Fatal("no plan event published")
	}
}

func TestCIWatchNilBindingRefuses(t *testing.T) {
	cmd := NewCICmd(CmdDeps{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"watch", "job-1"})
	err := cmd.Execute()
	if !isKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("nil WatchEvents: %v, want KindUnavailable naming `cascade daemon run`", err)
	}
	cmd.SetArgs([]string{"watch"})
	if err := cmd.Execute(); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("no job id: %v, want KindInvalidInput", err)
	}
}

func TestCIWatchOpenErrorPropagates(t *testing.T) {
	want := cascade.New(cascade.KindPermissionDenied, "owner-only")
	cmd := NewCICmd(CmdDeps{WatchEvents: func(context.Context, string) (<-chan CIEventFrame, func(), error) {
		return nil, func() {}, want
	}})
	cmd.SetArgs([]string{"watch", "job-1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); !errors.Is(err, want) {
		t.Fatalf("open error: %v, want %v", err, want)
	}
}

func TestCIAttestVerifyAttachPoint(t *testing.T) {
	cmd := NewCICmd(CmdDeps{})
	cmd.SetArgs([]string{"attest", "verify", "abc123"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if !isKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "ci attest verify") {
		t.Fatalf("nil AttestVerify: %v, want KindUnavailable naming the verb", err)
	}
	var got []string
	cmd = NewCICmd(CmdDeps{AttestVerify: func(_ *cobra.Command, args []string) error { got = args; return nil }})
	cmd.SetArgs([]string{"attest", "verify", "abc123"})
	if err := cmd.Execute(); err != nil || len(got) != 1 || got[0] != "abc123" {
		t.Fatalf("bound AttestVerify: err=%v args=%v, want it called with [abc123]", err, got)
	}
}

func isKind(err error, want cascade.Kind) bool {
	got, ok := cascade.KindOf(err)
	return ok && got == want
}

type abnormalExec struct{} // errors on format, panics on lint

func (abnormalExec) Run(_ context.Context, sj SubJob) (SubJobResult, error) {
	if sj.Kind == RequirementLint {
		panic("lint executor crashed")
	}
	return SubJobResult{}, errors.New("format executor failed")
}

// An erroring or panicking kind publishes ci.checkpoint.aborted; ci watch exits 1.
func TestDispatchPublishesTerminalForAbnormalKind(t *testing.T) {
	r := newRig(t)
	r.build(abnormalExec{})
	r.commit(map[string]string{"docs/a.md": "a"})
	if _, err := r.checkpoint(); err == nil {
		t.Fatal("Checkpoint succeeded with every kind failing abnormally")
	}
	got := map[string]streamEventPayload{}
	for _, p := range r.streamEvents(EventKindCheckpointAborted) {
		got[p.Kind] = p
	}
	for _, kind := range []string{"format", "lint"} {
		if p, ok := got[kind]; !ok || p.Passed || p.Error == "" || p.JobID != "job-1" {
			t.Fatalf("aborted event for %s = %+v (present %v), want one naming the error", kind, p, ok)
		}
	}
	frames := []CIEventFrame{replayed, plan(t, "job-1", "cp-2", true, "format", "lint")}
	for _, p := range got {
		p.CheckpointID, p.Acceptance = "cp-2", true
		data, _ := json.Marshal(p)
		frames = append(frames, CIEventFrame{Event: string(EventKindCheckpointAborted), Data: data})
	}
	if _, returned, err := runWatch(t, frames, false); !returned || output.ExitCode(err) != 1 {
		t.Fatalf("watch over abnormal terminals: returned=%v err=%v, want exit 1", returned, err)
	}
}

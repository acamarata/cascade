package antigravity_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	ag "github.com/acamarata/cascade/providers/agents/antigravity"
	"github.com/acamarata/cascade/providers/agents/conformance"
)

// conformanceCaseCount is the conformance inventory at the base.
const conformanceCaseCount = 20

// recorded is the captured agy 1.4.12 print-mode stream the replay serves.
const recorded = "testdata/stream-json/agy-1.4.12-print.jsonl"

// replayBinary writes an agy stand-in that answers the probe, replays the
// recorded stream line by line and then stays alive for a second, so a
// Cancel issued right after Spawn always finds a live group to kill.
func replayBinary(t *testing.T) string {
	t.Helper()
	stream, err := filepath.Abs(recorded)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then printf '%s' '" + fixtureHelp + "'; exit 0; fi\n" +
		"while IFS= read -r line; do printf '%s\\n' \"$line\"; done < '" + stream + "'\nexec sleep 1\n"
	bin := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestAntigravityConformanceReplay runs every Suite case against the
// recorded print stream with Suite{Env, Worktree, Filter}, then checks one
// replayed job end to end.
func TestAntigravityConformanceReplay(t *testing.T) {
	requireProcessGroups(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := ag.Config{Binary: replayBinary(t), RuntimeHome: home, Clock: fixedClock{fixtureNow},
		Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}
	s := &conformance.Suite{
		Worktree: home, Filter: func(b []byte) ([]byte, error) { return b, nil },
		Env: provider.SpawnEnv{Home: home, Driver: []string{"HOME=" + home, "LANG=C", "PATH=/usr/bin:/bin"}},
		New: func() (provider.AgentProvider, error) { return ag.New(cfg) },
	}
	v := reflect.ValueOf(s)
	ran := 0
	for i := 0; i < v.Type().NumMethod(); i++ {
		fn, ok := v.Method(i).Interface().(func(*testing.T))
		if !ok || !strings.HasPrefix(v.Type().Method(i).Name, "Test") {
			continue
		}
		ran++
		t.Run(v.Type().Method(i).Name, fn)
	}
	if ran != conformanceCaseCount {
		t.Fatalf("ran %d conformance cases, want %d", ran, conformanceCaseCount)
	}
	assertReplayed(t, cfg, s)
}

// assertReplayed collects one replayed job: the agent reply is the
// recording's text deltas and every native event type arrives in order.
func assertReplayed(t *testing.T, cfg ag.Config, s *conformance.Suite) {
	t.Helper()
	d, err := ag.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	spec := provider.AgentJobSpec{Prompt: "replay", Env: s.Env, WorktreeRoot: s.Worktree, Filter: s.Filter,
		Envelope: provider.SpawnEnvelope{Digest: strings.Repeat("ab", 32), Principal: "fixture",
			Roots: []string{s.Worktree}, ExpiresAt: fixtureNow.Add(time.Minute)}}
	r, err := d.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	cr, err := d.Collect(context.Background(), r.JobID)
	if err != nil || cr.Output != "ok\n" {
		t.Fatalf("Collect = %q, %v; want the recorded reply", cr.Output, err)
	}
	evs, err := d.Events(r.JobID)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		if e.Native != nil {
			types = append(types, e.Native.Type)
		}
	}
	want := "init step_update step_update step_update result"
	if got := strings.Join(types, " "); got != want || evs[len(evs)-1].Kind != provider.DriverEventDone {
		t.Fatalf("native events %q (last %v), want %q then done", got, evs[len(evs)-1].Kind, want)
	}
}

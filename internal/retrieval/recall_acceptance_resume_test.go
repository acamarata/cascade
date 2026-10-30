//go:build integration && !windows

// Purpose: Steps 3 and 4 of the recall-to-resume acceptance (P1-CPA-09): the
// resume of the top recall hit into a fresh session, asserted on stored
// state, the chat.resume socket round trip recorded as a fixture, and the
// fail-closed resume of a local-only thread into a restricted session.
// SPORT: internal.retrieval recall_acceptance (ADD) (P1-CPA-09).
package retrieval_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

var updateFixture = flag.Bool("update-acceptance", false, "rewrite resume-fixtures/v2-acceptance.json")

// refusalKind returns the typed kind a failed cascade run printed: the CLI
// writes "error: <kind>: ..." to stderr.
func refusalKind(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(line, "error: "); ok {
			kind, _, _ := strings.Cut(rest, ":")
			return kind
		}
	}
	return ""
}

// step3Resume resumes the top hit and reads what the daemon stored.
func (r *acceptRun) step3Resume(t *testing.T) {
	e, s := r.e, r.seed
	if got := e.turns(t, r.convID)[0].Segments[0].Content; !strings.Contains(got, s.VaultValue) {
		t.Fatalf("the seed thread does not hold the vaulted literal, the absence below is vacuous: %q", got)
	}
	before := e.threadIDs(t)
	out, errb, code := e.cli(t, "", "chat", "--resume-topic", r.topPath, "--json", "-m", "continue")
	switch kind := refusalKind(errb); {
	case code == 0:
	case kind == "unsupported" || kind == "unavailable" || kind == "not-found":
	default:
		t.Fatalf("resume exited %d with %q, want 0 or a typed reply-path refusal\nstdout: %s\nstderr: %s", code, kind, out, errb)
	}
	var added []string
	for id := range e.threadIDs(t) {
		if !before[id] {
			added = append(added, id)
		}
	}
	if len(added) != 1 {
		t.Fatalf("resume created %d session threads besides the seed, want exactly 1: %v", len(added), added)
	}
	turns := e.turns(t, added[0])
	if len(turns) == 0 || len(turns[0].Segments) == 0 {
		t.Fatalf("the session thread has no first turn: %+v", turns)
	}
	seg0 := turns[0].Segments[0].Content
	if !strings.Contains(seg0, "quartz lighthouse rollout behind the staged gate") {
		t.Errorf("segment 0 lacks the seeded content: %q", seg0)
	}
	if strings.Contains(seg0, s.VaultValue) || strings.Contains(out, s.VaultValue) || strings.Contains(errb, s.VaultValue) {
		t.Errorf("the vaulted literal reached the resumed session or the CLI output")
	}
	r.recordRoundTrip(t, added[0])
}

// roundTrip is the recorded chat.resume exchange, ids normalised.
type roundTrip struct {
	Method   string         `json:"method"`
	Request  map[string]any `json:"request"`
	Response map[string]any `json:"response"`
}

// recordRoundTrip calls chat.resume over the socket into the session thread
// the CLI created (no further thread is added), normalises the ids and compares (or, with
// -update-acceptance, rewrites) resume-fixtures/v2-acceptance.json.
func (r *acceptRun) recordRoundTrip(t *testing.T, sessionID string) {
	e := r.e
	req := map[string]any{"topic_id": r.topPath, "session_thread_id": sessionID}
	var resp map[string]any
	e.rpc(t, "chat.resume", req, &resp)
	rt := roundTrip{Method: "chat.resume", Request: req, Response: resp}
	raw, err := json.Marshal(rt)
	if err != nil {
		t.Fatal(err)
	}
	norm := strings.NewReplacer(r.topPath, "<seed-thread>", sessionID, "<session-thread>").Replace(string(raw))
	var got roundTrip
	if err := json.Unmarshal([]byte(norm), &got); err != nil {
		t.Fatal(err)
	}
	if lc, _ := got.Response["leading_context"].(string); !strings.Contains(lc, "quartz lighthouse rollout") || strings.Contains(lc, r.seed.VaultValue) {
		t.Fatalf("chat.resume leading_context is wrong: %q", lc)
	}
	path := filepath.Join("..", "conversation", "testdata", "resume-fixtures", "v2-acceptance.json")
	if *updateFixture {
		writeFixture(t, path, got)
	}
	compareFixture(t, path, got)
}

// fixtureFile is the on-disk shape: provenance plus the round trip.
type fixtureFile struct {
	Provenance string `json:"_provenance"`
	roundTrip
}

func writeFixture(t *testing.T, path string, rt roundTrip) {
	t.Helper()
	f := fixtureFile{
		Provenance: "Recorded by TestEpicV_RecallResumeAcceptance/Step3 (internal/retrieval/recall_acceptance_resume_test.go) " +
			"from a devkeys build of ./cmd/cascade (version dev) on a live daemon, 2026-10-30, via -update-acceptance. " +
			"Thread ids are normalised to <seed-thread> and <session-thread>; the default run compares this file with a fresh round trip.",
		roundTrip: rt,
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil { //nolint:gosec // a committed fixture.
		t.Fatal(err)
	}
}

func compareFixture(t *testing.T, path string, got roundTrip) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the recorded fixture: %v", err)
	}
	var want fixtureFile
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if want.Provenance == "" || !reflect.DeepEqual(want.roundTrip, got) {
		t.Errorf("chat.resume round trip differs from %s\nwant %+v\ngot  %+v", path, want.roundTrip, got)
	}
}

// step4LocalOnly: a local-only thread into a no-flag (restricted) session
// fails closed with the actionable message and changes nothing stored.
func (r *acceptRun) step4LocalOnly(t *testing.T) {
	e := r.e
	id := e.seedThread(t, "local-only", r.seed.LocalOnly)
	before := e.totalTurns(t)
	out, errb, code := e.cli(t, "", "chat", "--resume-topic", id, "--json", "-m", "continue")
	if want := cascade.KindPolicyDenied.ExitCode(); code != want || refusalKind(errb) != "policy-denied" {
		t.Fatalf("exit %d kind %q, want the policy-denied exit code and kind\nstdout: %s\nstderr: %s", code, refusalKind(errb), out, errb)
	}
	for _, frag := range []string{
		"is local-only, which is more restrictive than the destination session's restricted",
		"resume it into a session created at least as strict as local-only",
	} {
		if !strings.Contains(errb, frag) {
			t.Errorf("the refusal lacks %q:\n%s", frag, errb)
		}
	}
	if after := e.totalTurns(t); after != before {
		t.Errorf("turn count changed across a refused resume: %d -> %d", before, after)
	}
}

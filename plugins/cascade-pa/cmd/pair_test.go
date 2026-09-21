package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stubPairClient is an in-memory PairClient a test injects via
// SetPairClient — no real socket, matching the SAME kind of seam
// chat_test.go injects for Client.
type stubPairClient struct {
	res PairCodeResult
	err error
}

func (s stubPairClient) IssueCode(context.Context, string) (PairCodeResult, error) {
	return s.res, s.err
}

// withPairClient installs c for the duration of the test and restores
// the previous client on cleanup, so tests never leak state into each
// other (t.Parallel is deliberately NOT used here for that reason — the
// seam is a package global).
func withPairClient(t *testing.T, c PairClient) {
	t.Helper()
	SetPairClient(c)
	t.Cleanup(func() { SetPairClient(nil) })
}

func fixedExpiry() time.Time {
	return time.Date(2026, 9, 20, 12, 10, 0, 0, time.UTC)
}

// TestPaPairCLI is the check-named root test (contract check #5: `go
// test -run '^TestPaPairCLI$'`) covering every `cascade pa pair`
// behavioral scenario as a subtest.
func TestPaPairCLI(t *testing.T) {
	t.Run("HumanOutput", testPaPairCLIHumanOutput)
	t.Run("JSONOutput", testPaPairCLIJSONOutput)
	t.Run("NoInputNoPromptDifference", testPaPairCLINoInputNoPromptDifference)
	t.Run("CompletesWithinDeadline", testPaPairCLICompletesWithinDeadline)
	t.Run("UnconfiguredClientRefuses", testPaPairCLIUnconfiguredClientRefuses)
	t.Run("ClientErrorPropagates", testPaPairCLIClientErrorPropagates)
	t.Run("OmittedSubjectReachesTheClientEmpty", testPaPairCLIOmittedSubjectIsEmpty)
	t.Run("PositionalSubjectReachesTheClient", testPaPairCLIPositionalSubject)
}

// recordingPairClient captures the subject the command passed down.
type recordingPairClient struct {
	got string
	res PairCodeResult
}

func (r *recordingPairClient) IssueCode(_ context.Context, subject string) (PairCodeResult, error) {
	r.got = subject
	r.res.Subject = subject
	if r.res.Subject == "" {
		r.res.Subject = "resolved-by-the-client"
	}
	return r.res, nil
}

// testPaPairCLIOmittedSubjectIsEmpty is the fix for a defect this command
// would otherwise have: an omitted subject must reach the client as the EMPTY
// string so the client can resolve the configured bridge, never as an invented
// literal like "default" that no bridge module ever verifies.
func testPaPairCLIOmittedSubjectIsEmpty(t *testing.T) {
	rec := &recordingPairClient{res: PairCodeResult{Code: "7ZQK3M9F", ExpiresAt: fixedExpiry()}}
	withPairClient(t, rec)
	c := NewPairCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs(nil)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.got != "" {
		t.Fatalf("the command passed subject %q for an omitted argument, want the empty string", rec.got)
	}
	if !strings.Contains(out.String(), "resolved-by-the-client") {
		t.Fatalf("output %q does not name the subject the client resolved", out.String())
	}
}

// testPaPairCLIPositionalSubject is the other direction: a named subject is
// passed through verbatim.
func testPaPairCLIPositionalSubject(t *testing.T) {
	rec := &recordingPairClient{res: PairCodeResult{Code: "7ZQK3M9F", ExpiresAt: fixedExpiry()}}
	withPairClient(t, rec)
	c := NewPairCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"tg-abc123"})
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.got != "tg-abc123" {
		t.Fatalf("the command passed subject %q, want tg-abc123", rec.got)
	}
}

// testPaPairCLIHumanOutput proves the default (non-JSON) render carries
// the code, subject and expiry, and the exact instruction line the
// operator needs.
func testPaPairCLIHumanOutput(t *testing.T) {
	withPairClient(t, stubPairClient{res: PairCodeResult{Code: "7ZQK3M9F", Subject: "default", ExpiresAt: fixedExpiry()}})
	c := NewPairCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs(nil)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.String()
	for _, want := range []string{"7ZQK3M9F", "default", "2026-09-20T12:10:00Z", `send "/pair 7ZQK3M9F"`} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got %q", want, got)
		}
	}
}

// testPaPairCLIJSONOutput proves --json emits the structured object with
// the exact field names the ticket's acceptance criteria name.
func testPaPairCLIJSONOutput(t *testing.T) {
	withPairClient(t, stubPairClient{res: PairCodeResult{Code: "ABCDEFGH", Subject: "tg-1", ExpiresAt: fixedExpiry()}})
	c := NewPairCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"tg-1", "--json"})
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got struct {
		Code      string `json:"code"`
		Subject   string `json:"subject"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal(%q): %v", out.String(), err)
	}
	if got.Code != "ABCDEFGH" || got.Subject != "tg-1" || got.ExpiresAt != "2026-09-20T12:10:00Z" {
		t.Errorf("got %+v", got)
	}
}

// testPaPairCLINoInputNoPromptDifference is the R-14.71 automation-parity
// assertion: setting CASCADE_NO_INPUT=1 changes NOTHING about this
// command's behavior, because it has no positional-prompt branch to
// refuse in the first place (unlike `chat`). Both runs against an
// identical PairClient must render byte-identical output.
func testPaPairCLINoInputNoPromptDifference(t *testing.T) {
	run := func(t *testing.T, noInput bool) string {
		t.Helper()
		withPairClient(t, stubPairClient{res: PairCodeResult{Code: "MZQ7H2XP", Subject: "default", ExpiresAt: fixedExpiry()}})
		if noInput {
			t.Setenv("CASCADE_NO_INPUT", "1")
		} else {
			t.Setenv("CASCADE_NO_INPUT", "")
		}
		c := NewPairCommand()
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetIn(bytes.NewReader(nil)) // empty stdin: a prompt reading it would block/hang
		c.SetArgs(nil)
		if err := c.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		return out.String()
	}
	withEnv := run(t, true)
	withoutEnv := run(t, false)
	if withEnv != withoutEnv {
		t.Fatalf("CASCADE_NO_INPUT changed output:\nwith=%q\nwithout=%q", withEnv, withoutEnv)
	}
	if withEnv == "" {
		t.Fatal("expected non-empty output")
	}
}

// testPaPairCLICompletesWithinDeadline is the mutation-provable half of
// the no-prompt claim above: with empty stdin and a short context
// deadline, a command that ever grew a "wait for input" branch would
// hang past the deadline instead of returning. The meaningful mutation
// is adding a stdin read to runPair, which would make cc.Execute block
// until the real test process's stdin hits EOF or hangs — this test's
// deadline turns that into an observable failure rather than a silent
// hang.
func testPaPairCLICompletesWithinDeadline(t *testing.T) {
	withPairClient(t, stubPairClient{res: PairCodeResult{Code: "QQQQ1111", Subject: "default", ExpiresAt: fixedExpiry()}})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c := NewPairCommand()
	c.SetContext(ctx)
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetIn(bytes.NewReader(nil))
	c.SetArgs(nil)
	done := make(chan error, 1)
	go func() { done <- c.Execute() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("pair command did not complete before deadline; it may be waiting on input")
	}
}

// testPaPairCLIUnconfiguredClientRefuses proves the honest default: with
// no SetPairClient call, the command returns the typed KindUnavailable
// error, never a fabricated code.
func testPaPairCLIUnconfiguredClientRefuses(t *testing.T) {
	SetPairClient(nil)
	c := NewPairCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs(nil)
	err := c.Execute()
	if err == nil {
		t.Fatal("expected an error with no PairClient wired")
	}
	if !errors.Is(err, errPairClientUnconfigured) {
		t.Fatalf("got %v, want errPairClientUnconfigured", err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("got kind of %v, want KindUnavailable", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output on refusal, got %q", out.String())
	}
}

// testPaPairCLIClientErrorPropagates proves a real transport failure
// surfaces to the caller unmodified rather than being swallowed.
func testPaPairCLIClientErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindConflict, "cascade-pa: pair code already pending for subject")
	withPairClient(t, stubPairClient{err: wantErr})
	c := NewPairCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs(nil)
	err := c.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
}

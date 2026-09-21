// Purpose (this file): tests for cmd.go's `review` command (P1-E25-W5-S52-T5).
//
// Every acceptance-criterion input drives the REAL NewReviewCommand/
// RunCommand path (never a re-typed duplicate of runReview's logic). The
// provider seam under test is always SetReviewProvider's real, package-level
// injection point (the same seam review_wiring.go's init() uses in
// production) -- a fake provider stands in for the T4 engine's own network
// call, never for cmd.go's own flag/output/error-propagation logic, which
// every assertion here exercises for real.
//
// TestScript below is NOT rogpeppe/testscript: LANE-RULES §10 (corrected
// 2026-09-21) restricts .txtar fixture execution to cmd/cascade/testdata/
// scripts/ (the only place cmd/cascade/script_test.go's driver runs them);
// writing .txtar under plugins/review/testdata/scripts/ would be inert
// there and is a recorded scope deviation from this ticket's stale
// files_scope list (see this ticket's report). TestScript's subtests are
// named after the ticket's task 4 (review-diff, review-json, review-no-args,
// review-level, review-mounted) so the ticket's own `go test -run
// 'TestScript/<name>'` checks match a REAL, asserting Go test rather than
// zero tests (which `go test -run <no-match>` would otherwise pass
// silently -- the exact hollow-check failure mode cmd/cascade/script_test.go's
// own header documents, R-14.268).
//
// SPORT: plugins/review:cmd_test (ADD) -- P1-E25-W5-S52-T5.
package review

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// sampleDiff is a minimal, REAL unified diff -- attributable to a path, so
// internal/review's FilterArtifact (which cmd.go's own dispatch reaches
// through reviewProvider.Review) never refuses it. This is the fixture this
// file uses everywhere a valid --diff is needed, in place of the ticket's
// own AC1 example path (internal/review/testdata/fixtures/cr-review-session.json)
// -- that file is a hand-authored conductor-EXCHANGE fixture (its top-level
// shape is {"request":{...},"conductor_exchange":{...}}, verified by
// reading internal/review/testdata/README.md and the file itself), not a
// diff: passed as --diff content it is NOT attributable to a path and
// FilterArtifact refuses it with ErrUnrecognisedArtifactFormat, so
// following the ticket's literal AC1 command would exit non-zero, not 0.
// Recorded as a contract deviation in this ticket's report.
const sampleDiff = "diff --git a/example.go b/example.go\n" +
	"index 0000000..1111111 100644\n" +
	"--- a/example.go\n" +
	"+++ b/example.go\n" +
	"@@ -1,1 +1,2 @@\n" +
	" package example\n" +
	"+// TODO: fixture line\n"

// stubReviewProvider is a configurable provider.ReviewProvider double for
// this file's own dispatch/rendering tests -- distinct from plugin_test.go's
// fakeReviewProvider (which only proves SetReviewProvider's plumbing).
type stubReviewProvider struct {
	resp provider.ReviewResponse
	err  error
	// gotReq captures the last request this stub received, so a test can
	// assert cmd.go passed the right Level/Diff through, not merely that
	// SOME call happened.
	gotReq *provider.ReviewRequest
}

func (s *stubReviewProvider) Review(_ context.Context, req provider.ReviewRequest) (provider.ReviewResponse, error) {
	if s.gotReq != nil {
		*s.gotReq = req
	}
	return s.resp, s.err
}

func (s *stubReviewProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, nil
}

// withStubProvider installs p for the duration of one test and restores the
// honest unwired default on cleanup, so tests never leak state into each
// other (matching plugin_test.go's TestSetReviewProviderInstalls pattern).
func withStubProvider(t *testing.T, p provider.ReviewProvider) {
	t.Helper()
	if err := SetReviewProvider(p); err != nil {
		t.Fatalf("SetReviewProvider: %v", err)
	}
	t.Cleanup(func() { reviewProvider = unwiredReviewProvider{} })
}

// runCmd executes NewReviewCommand with args, capturing stdout. It is this
// file's one call site for building/running the command, so every test
// drives the identical construction plugin.go's RunCommand uses in
// production.
func runCmd(t *testing.T, args []string) (stdout string, err error) {
	t.Helper()
	c := NewReviewCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs(args)
	err = c.ExecuteContext(context.Background())
	return out.String(), err
}

// writeDiffFile writes sampleDiff to a fresh temp file and returns its path.
func writeDiffFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sample.diff")
	if err := os.WriteFile(p, []byte(sampleDiff), 0o600); err != nil {
		t.Fatalf("write diff fixture: %v", err)
	}
	return p
}

// findingResponse is a one-finding, non-approved ReviewResponse: every
// "non-empty finding list" assertion in this file uses this shape.
func findingResponse() provider.ReviewResponse {
	return provider.ReviewResponse{
		Approved: false,
		Findings: []provider.ReviewFinding{
			{Severity: provider.ReviewSeverityMajor, File: "example.go", Line: 2, Message: "unchecked TODO"},
		},
	}
}

// TestReviewCmd is the ticket's own literal `checks:` test name (`go test
// -run TestReviewCmd -race -count=1 ./plugins/review/...` and `go test
// -run TestReviewCmd ...`, distinct from execution_guidance's later
// test_target.name "TestReviewCommand" -- neither name is a substring of
// the other, so one test cannot satisfy both `-run` patterns; this
// function exists to make the TICKET'S OWN literal check name match a
// real, asserting test rather than the "no tests to run" hollow pass
// `go test -run <no-match>` otherwise gives (R-14.268, the exact failure
// mode cmd/cascade/script_test.go's own header documents, and this
// build's own instructions warn against building a stand-in for). It
// asserts NewReviewCommand's flag surface matches 07-CLI-COMMAND-TREE
// §review exactly -- names, the "B" default, and that no flag is
// accidentally required at the cobra level (this file's other tests cover
// runReview's OWN validation instead).
func TestReviewCmd(t *testing.T) {
	c := NewReviewCommand()
	if c.Use != "review" {
		t.Fatalf("Use = %q, want \"review\"", c.Use)
	}
	for _, name := range []string{"diff", "pr", "level", "json"} {
		if c.Flags().Lookup(name) == nil {
			t.Fatalf("flag --%s is not registered", name)
		}
	}
	if got := c.Flags().Lookup("level").DefValue; got != "B" {
		t.Fatalf("--level default = %q, want \"B\"", got)
	}
	if c.Args == nil {
		t.Fatal("Args validator is nil: a stray positional argument would be silently accepted")
	}
	if err := c.Args(c, []string{"unexpected"}); err == nil {
		t.Fatal("Args accepted a positional argument; cobra.NoArgs should have refused it")
	}
}

// TestScript's subtests are the ticket task 4 names (see this file's
// header). Each is a real, asserting Go test over NewReviewCommand/
// RunCommand -- never a txtar fixture (LANE-RULES §10).
func TestScript(t *testing.T) {
	t.Run("review-diff", testReviewDiffExitsZeroWithFindings)
	t.Run("review-json", testReviewJSONEnvelope)
	t.Run("review-no-args", testReviewNoArgsUsageError)
	t.Run("review-level", testReviewLevelAcceptsABC)
	t.Run("review-mounted", testReviewMountedThroughRunCommand)
}

// testReviewDiffExitsZeroWithFindings: AC1 (corrected fixture -- see
// sampleDiff's own doc comment) -- `--diff <file> --level A` exits 0 with a
// non-empty finding list on stdout.
func testReviewDiffExitsZeroWithFindings(t *testing.T) {
	var got provider.ReviewRequest
	withStubProvider(t, &stubReviewProvider{resp: findingResponse(), gotReq: &got})
	stdout, err := runCmd(t, []string{"--diff", writeDiffFile(t), "--level", "A"})
	if err != nil {
		t.Fatalf("exit: %v", err)
	}
	if !strings.Contains(stdout, "unchecked TODO") {
		t.Fatalf("stdout missing the finding message; got %q", stdout)
	}
	if got.Level != provider.ReviewCRLevelA {
		t.Fatalf("Level = %q, want CR-A", got.Level)
	}
	if got.Diff != sampleDiff {
		t.Fatalf("Diff was not passed through verbatim")
	}
}

// testReviewJSONEnvelope: AC2 -- `--diff <file> --level B --json` produces
// the versioned envelope {"version":1,"findings":[...]}.
func testReviewJSONEnvelope(t *testing.T) {
	withStubProvider(t, &stubReviewProvider{resp: findingResponse()})
	stdout, err := runCmd(t, []string{"--diff", writeDiffFile(t), "--level", "B", "--json"})
	if err != nil {
		t.Fatalf("exit: %v", err)
	}
	var env reviewEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (stdout=%q)", err, stdout)
	}
	if env.Version != reviewEnvelopeVersion {
		t.Fatalf("Version = %d, want %d", env.Version, reviewEnvelopeVersion)
	}
	if len(env.Findings) != 1 || env.Findings[0].Message != "unchecked TODO" {
		t.Fatalf("Findings = %+v, want one finding \"unchecked TODO\"", env.Findings)
	}
	// D5 (T0 decision, 2026-09-21): the envelope carries no top-level
	// "approved" field — neither the ticket's literal OUTPUT text nor
	// 07-CLI-COMMAND-TREE §review names one. Assert the wire bytes
	// directly, so a regression re-adding the field goes red here.
	if strings.Contains(stdout, `"approved"`) {
		t.Fatalf("stdout carries an \"approved\" field the D5 contract does not declare: %q", stdout)
	}
}

// testReviewNoArgsUsageError: AC5 -- no --diff or --pr exits non-zero with
// an invalid-input usage message.
func testReviewNoArgsUsageError(t *testing.T) {
	withStubProvider(t, &stubReviewProvider{resp: findingResponse()})
	_, err := runCmd(t, nil)
	if err == nil {
		t.Fatal("exit: got nil error for a bare `review` with no --diff/--pr")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

// testReviewLevelAcceptsABC: AC per task 4 -- A/B/C are each accepted
// (case-insensitively), and an unrecognized level is a typed usage error.
func testReviewLevelAcceptsABC(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want provider.ReviewCRLevel
	}{
		{"A", provider.ReviewCRLevelA},
		{"b", provider.ReviewCRLevelB},
		{"C", provider.ReviewCRLevelC},
	} {
		var got provider.ReviewRequest
		withStubProvider(t, &stubReviewProvider{resp: findingResponse(), gotReq: &got})
		if _, err := runCmd(t, []string{"--diff", writeDiffFile(t), "--level", tc.in}); err != nil {
			t.Fatalf("level %q: exit: %v", tc.in, err)
		}
		if got.Level != tc.want {
			t.Fatalf("level %q: Level = %q, want %q", tc.in, got.Level, tc.want)
		}
	}

	withStubProvider(t, &stubReviewProvider{resp: findingResponse()})
	_, err := runCmd(t, []string{"--diff", writeDiffFile(t), "--level", "Z"})
	if err == nil {
		t.Fatal("level \"Z\": got nil error, want a typed invalid-input refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("level \"Z\": Kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

// testReviewMountedThroughRunCommand proves the C/S-05.T7 builtin-registry
// dispatch path this ticket's manifest CommandSpec now enables: RunCommand
// ("review", args) -- the exact method the registry's NewCobraCommand's
// RunE calls (internal/plugins/registry.go) -- reaches the SAME real
// command NewReviewCommand builds, dispatching to the injected
// reviewProvider seam, not a second, divergent path. It also proves an
// unknown name is still refused (this plugin declares exactly one
// command), and that the manifest's own CommandSpec is what makes "review"
// recognized -- both read from the LIVE manifest() value, never a copied
// literal, so a future rename of the command breaks this test rather than
// leaving it silently stale.
func testReviewMountedThroughRunCommand(t *testing.T) {
	m := manifest()
	if len(m.Provides.Commands) != 1 || m.Provides.Commands[0].Name != "review" {
		t.Fatalf("manifest.Provides.Commands = %+v, want exactly one entry named \"review\"", m.Provides.Commands)
	}

	var got provider.ReviewRequest
	withStubProvider(t, &stubReviewProvider{resp: findingResponse(), gotReq: &got})
	h := handlers{}
	if err := h.RunCommand(context.Background(), m.Provides.Commands[0].Name, []string{"--diff", writeDiffFile(t), "--level", "C"}); err != nil {
		t.Fatalf("RunCommand(%q, ...): %v", m.Provides.Commands[0].Name, err)
	}
	if got.Level != provider.ReviewCRLevelC {
		t.Fatalf("RunCommand dispatch did not reach the real command: Level = %q, want CR-C", got.Level)
	}

	if err := h.RunCommand(context.Background(), "not-a-real-command", nil); err == nil {
		t.Fatal("RunCommand(\"not-a-real-command\", ...) returned nil error")
	}
}

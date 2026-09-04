// Purpose: unit tests for `cascade memory review` — the wiring proof
//
//	against the REAL root command, the flag-and-environment resolution of
//	the one action an invocation carries out, the proof that a listing
//	CANNOT act, the rendered output, and the redaction canary over the one
//	diagnostic this command can print. It imports neither "net" nor
//	"net/http", so it runs in the fast, no-network unit lane (Art.7.2).
//
// SPORT: cmd.cascade.cmd.memory.review (ADD, P1-E07-W2-S14-T3).
package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/review"
	"github.com/acamarata/cascade/pkg/cascade"
)

// reviewNow is the instant the fixtures below are rendered at.
var reviewNow = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// sampleListResult is a queue with one row in each section.
func sampleListResult() review.ListResult {
	promoted := reviewNow.Add(-24 * time.Hour)
	return review.ListResult{
		At:          reviewNow,
		MinRefCount: memory.PromotionMinRefCount,
		MinSessions: memory.PromotionMinSessions,
		Pending: []memory.CandidateSummary{{
			ID: "project/below", Kind: memory.KindProject, RefCount: 1,
			Sessions: 1, Status: memory.CandidatePending,
		}},
		Promoted: []memory.CandidateSummary{{
			ID: "user/standing", Kind: memory.KindUser, RefCount: 3,
			Sessions: 2, Status: memory.CandidatePromoted, PromotedAt: &promoted,
		}},
	}
}

// TestMemoryReviewResolvesOnTheRealRootCommand is the reachability proof:
// deleting the newMemoryReviewCmd line from newMemoryCmd fails this.
func TestMemoryReviewResolvesOnTheRealRootCommand(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"memory", "review"})
	if err != nil {
		t.Fatalf("memory review did not resolve on the real root: %v", err)
	}
	if cmd.Name() != "review" || cmd.Parent() == nil || cmd.Parent().Name() != "memory" {
		t.Fatalf("resolved %q under %v", cmd.Name(), cmd.Parent())
	}
	if cmd.RunE == nil {
		t.Fatal("memory review resolved but has no RunE")
	}
	for _, flag := range []string{"section", "auto-approve", "auto-skip", "defer-days", "revert"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("memory review has no --%s flag", flag)
		}
	}
}

func TestMemoryReviewListRendersTheEvidenceAndTheThreshold(t *testing.T) {
	h := &memoryHarness{result: sampleListResult()}
	stdout, _, err := h.run(t, "review")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(h.calls) != 1 || h.calls[0].Method != review.MethodReviewList {
		t.Fatalf("calls = %+v, want one memory.review.list", h.calls)
	}
	for _, want := range []string{
		"2026-09-04T12:00:00Z",               // the instant the claims are relative to
		"3 reference(s) across 2 session(s)", // the threshold, so a reader can check
		"PENDING", "project/below",
		"PROMOTED", "user/standing",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
	// The listing states a fact about the counts and says so in as many
	// words; it must not suggest, rank or score anything.
	if !strings.Contains(stdout, "not recommendations") {
		t.Errorf("the pending section does not say what it is:\n%s", stdout)
	}
	for _, forbidden := range []string{"suggest", "recommended", "score", "confidence"} {
		if strings.Contains(strings.ToLower(stdout), forbidden) {
			t.Errorf("the listing %sed something:\n%s", forbidden, stdout)
		}
	}
}

func TestMemoryReviewListReportsWhatTheTablesDoNotShow(t *testing.T) {
	result := sampleListResult()
	result.Snoozed = 2
	result.DueForAutoPromotion = []memory.CandidateSummary{{
		ID: "project/ready", Kind: memory.KindProject, RefCount: 3, Sessions: 2,
		Status: memory.CandidatePending,
	}}
	result.Unreadable = []review.Unreadable{{
		ID: "user/damaged", Reason: "reading /Users/a-person/.cascade/memory/x: bad",
	}}
	h := &memoryHarness{result: result}
	stdout, _, err := h.run(t, "review")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !strings.Contains(stdout, "2 pending candidate(s) hidden by a defer") {
		t.Errorf("the deferred candidates were not accounted for:\n%s", stdout)
	}
	if !strings.Contains(stdout, "project/ready") ||
		!strings.Contains(stdout, "mechanical lane") {
		t.Errorf("an above-threshold candidate vanished from the output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "user/damaged") {
		t.Errorf("an unreadable candidate was dropped:\n%s", stdout)
	}
	// The redaction canary: the row is shown, the machine path is not.
	if strings.Contains(stdout, "/Users/a-person") {
		t.Errorf("a machine path reached the rendered output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "[PATH-REDACTED]") {
		t.Errorf("the path was neither shown nor marked as redacted:\n%s", stdout)
	}
}

func TestMemoryReviewListJSONIsTheVersionedEnvelope(t *testing.T) {
	h := &memoryHarness{result: sampleListResult()}
	stdout, _, err := h.run(t, "review", "--json", "--section", "pending")
	if err != nil {
		t.Fatalf("review --json: %v", err)
	}
	var envelope struct {
		Version int  `json:"version"`
		OK      bool `json:"ok"`
		Data    struct {
			MinRefCount int `json:"min_ref_count"`
			Pending     []struct {
				ID       string `json:"id"`
				RefCount int    `json:"ref_count"`
			} `json:"pending"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("--json output is not an envelope: %v (%s)", err, stdout)
	}
	if envelope.Version != 1 || !envelope.OK {
		t.Errorf("envelope = %+v, want version 1 and ok", envelope)
	}
	if envelope.Data.MinRefCount != memory.PromotionMinRefCount {
		t.Errorf("the JSON payload dropped the threshold: %+v", envelope.Data)
	}
	if len(envelope.Data.Pending) != 1 || envelope.Data.Pending[0].ID != "project/below" {
		t.Errorf("payload pending = %+v", envelope.Data.Pending)
	}
	if params := h.calls[0].Params.(review.ListParams); params.Section != "pending" {
		t.Errorf("--section did not reach the RPC params: %+v", params)
	}
}

// TestMemoryReviewNonInteractiveActions covers the 08 §2 flag surface: each
// path chooses exactly one action and reaches the daemon without a TTY.
func TestMemoryReviewNonInteractiveActions(t *testing.T) {
	cases := map[string]struct {
		args   []string
		action string
		days   int
	}{
		"auto-approve": {args: []string{"--auto-approve"}, action: review.ActionApprove},
		"auto-skip":    {args: []string{"--auto-skip"}, action: review.ActionSkip},
		"revert":       {args: []string{"--revert"}, action: review.ActionRevert},
		"defer-days":   {args: []string{"--defer-days", "14"}, action: review.ActionDefer, days: 14},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := &memoryHarness{result: review.ActResult{
				Action: tc.action, Changed: tc.action != review.ActionSkip,
				Item: memory.CandidateSummary{ID: "project/below", Kind: memory.KindProject},
				At:   reviewNow,
			}}
			args := append([]string{"review", "project/below"}, tc.args...)
			stdout, _, err := h.run(t, args...)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(h.calls) != 1 || h.calls[0].Method != review.MethodReviewAct {
				t.Fatalf("calls = %+v, want one memory.review.act", h.calls)
			}
			params := h.calls[0].Params.(review.ActParams)
			if params.ID != "project/below" || params.Action != tc.action ||
				params.DeferDays != tc.days {
				t.Errorf("params = %+v, want action %q on the addressed candidate",
					params, tc.action)
			}
			if !strings.Contains(stdout, "project/below") {
				t.Errorf("the output did not name what was acted on:\n%s", stdout)
			}
		})
	}
}

func TestMemoryReviewEnvVarSelectsTheAction(t *testing.T) {
	h := &memoryHarness{
		result: review.ActResult{Action: review.ActionApprove, Changed: true,
			Item: memory.CandidateSummary{ID: "user/x", Status: memory.CandidatePromoted}},
		env: map[string]string{memoryReviewActionEnv: review.ActionApprove},
	}
	if _, _, err := h.run(t, "review", "user/x"); err != nil {
		t.Fatalf("env-driven approve: %v", err)
	}
	if params := h.calls[0].Params.(review.ActParams); params.Action != review.ActionApprove {
		t.Errorf("params = %+v, want the env var's action", params)
	}

	bad := &memoryHarness{env: map[string]string{memoryReviewActionEnv: "promote-everything"}}
	_, _, err := bad.run(t, "review", "user/x")
	if err == nil {
		t.Fatal("an unknown env action was accepted")
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v, want invalid_input", kind)
	}
	if len(bad.calls) != 0 {
		t.Errorf("an unknown env action still called the daemon: %+v", bad.calls)
	}
}

// TestMemoryReviewRefusesToActWithoutAnAddress is hard requirement 2 at
// the CLI boundary: there is no bulk mode, so a listing can never carry
// out the action a flag named.
func TestMemoryReviewRefusesToActWithoutAnAddress(t *testing.T) {
	cases := map[string][]string{
		"a flag with no address":  {"review", "--auto-approve"},
		"a defer with no address": {"review", "--defer-days", "3"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			h := &memoryHarness{}
			_, _, err := h.run(t, args...)
			if err == nil {
				t.Fatal("an action with no address was accepted")
			}
			if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
				t.Errorf("kind = %v, want invalid_input", kind)
			}
			if len(h.calls) != 0 {
				t.Errorf("it still called the daemon: %+v", h.calls)
			}
		})
	}

	env := &memoryHarness{env: map[string]string{memoryReviewActionEnv: review.ActionRevert}}
	if _, _, err := env.run(t, "review"); err == nil {
		t.Fatal("the env var acted on the whole queue")
	}
	if len(env.calls) != 0 {
		t.Errorf("the env var reached the daemon with no address: %+v", env.calls)
	}
}

func TestMemoryReviewRefusesTwoActionsAndNoAction(t *testing.T) {
	both := &memoryHarness{}
	_, _, err := both.run(t, "review", "project/x", "--auto-approve", "--revert")
	if err == nil {
		t.Fatal("two actions at once were accepted")
	}
	if len(both.calls) != 0 {
		t.Errorf("it still called the daemon: %+v", both.calls)
	}

	none := &memoryHarness{}
	_, _, err = none.run(t, "review", "project/x")
	if err == nil {
		t.Fatal("an address with no action was accepted")
	}
	if !strings.Contains(err.Error(), memoryReviewActionEnv) {
		t.Errorf("the refusal does not say how to name an action: %v", err)
	}
}

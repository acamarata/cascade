//go:build !windows

// Purpose: plugin.review.pr_diff's daemon side (P1-PLG-14, acceptance
// [3]-[5]): owner-only before any decode, a strict closed request, and the
// 8 MiB reply cap with no body in the refusal.
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/tools"
)

func prDiffClock() *testkit.FrozenClock {
	return testkit.NewFrozenClock(time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC))
}

func ownerCtx(t *testing.T) context.Context {
	return principal.WithCaller(t.Context(), principal.Caller{OwnerCLI: true})
}

func principalCtx(t *testing.T) context.Context {
	return principal.WithCaller(t.Context(), principal.Caller{Principal: &principal.Principal{ID: "fake-principal"}})
}

func TestReviewPRDiffRPC_OwnerOnly(t *testing.T) {
	isolatePRDiffTest(t)
	fetch, calls := countingPRDiff([]byte(fakePRDiff), nil)
	h := reviewPRDiffHandler(fetch, prDiffClock())
	// The declaration alone: a registry whose handler always answers still
	// refuses a principal caller and a caller-less call for this method.
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	stubRan := 0
	reg.Register(reviewPRDiffMethod, func(context.Context, json.RawMessage) (any, error) { stubRan++; return struct{}{}, nil })
	for name, ctx := range map[string]context.Context{"principal": principalCtx(t), "no caller": t.Context()} {
		_, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: reviewPRDiffMethod, Params: json.RawMessage(`{"bogus":1}`)})
		if eo == nil || stubRan != 0 {
			t.Fatalf("%s: dispatch admitted (stub ran %d); the verb must be declared AccessOwnerOnly", name, stubRan)
		}
	}
	if _, eo := reg.Dispatch(ownerCtx(t), &rpc.Request{JSONRPC: "2.0", Method: reviewPRDiffMethod, Params: json.RawMessage(`{}`)}); eo != nil || stubRan != 1 {
		t.Fatalf("owner dispatch: %v (stub ran %d), want admitted", eo, stubRan)
	}
	// The handler's own check, before any decode: malformed params from a
	// non-owner are permission-denied, not invalid-input.
	for name, ctx := range map[string]context.Context{"principal": principalCtx(t), "no caller": t.Context()} {
		_, err := h(ctx, json.RawMessage(`{"bogus":1}`))
		if k, _ := cascade.KindOf(err); k != cascade.KindPermissionDenied || len(*calls) != 0 {
			t.Fatalf("%s: err %v fetches %d, want permission-denied before decode or launch", name, err, len(*calls))
		}
	}
}

func TestReviewPRDiffRPC_StrictDecodeRefusals(t *testing.T) {
	isolatePRDiffTest(t)
	fetch, calls := countingPRDiff([]byte(fakePRDiff), nil)
	h := reviewPRDiffHandler(fetch, prDiffClock())
	valid := `{"owner":"octocat","repo":"Hello-World","number":1}`
	cases := map[string]string{
		"unknown field":     `{"owner":"octocat","repo":"Hello-World","number":1,"base_url":"http://x"}`,
		"number zero":       `{"owner":"octocat","repo":"Hello-World","number":0}`,
		"number negative":   `{"owner":"octocat","repo":"Hello-World","number":-3}`,
		"number too large":  `{"owner":"octocat","repo":"Hello-World","number":2147483648}`,
		"number float":      `{"owner":"octocat","repo":"Hello-World","number":1.5}`,
		"number string":     `{"owner":"octocat","repo":"Hello-World","number":"1"}`,
		"empty owner":       `{"owner":"","repo":"Hello-World","number":1}`,
		"missing owner":     `{"repo":"Hello-World","number":1}`,
		"owner slash":       `{"owner":"octo/cat","repo":"Hello-World","number":1}`,
		"owner traversal":   `{"owner":"..","repo":"Hello-World","number":1}`,
		"owner space":       `{"owner":"octo cat","repo":"Hello-World","number":1}`,
		"owner leading -":   `{"owner":"-octocat","repo":"Hello-World","number":1}`,
		"owner unicode":     `{"owner":"octocät","repo":"Hello-World","number":1}`,
		"empty repo":        `{"owner":"octocat","repo":"","number":1}`,
		"repo slash":        `{"owner":"octocat","repo":"Hello/World","number":1}`,
		"repo dotdot":       `{"owner":"octocat","repo":"..","number":1}`,
		"repo dot":          `{"owner":"octocat","repo":".","number":1}`,
		"trailing NUL":      valid + "\x00",
		"repo inner dotdot": `{"owner":"octocat","repo":"a..b","number":1}`,
		"repo newline":      `{"owner":"octocat","repo":"Hello\nWorld","number":1}`,
		"trailing object":   valid + `{}`,
		"trailing brace":    valid + `}`,
		"trailing garbage":  valid + ` x`,
		"not an object":     `["octocat","Hello-World",1]`,
		"null":              `null`,
		"oversize":          valid + strings.Repeat(" ", reviewPRDiffMaxRequest),
	}
	for name, raw := range cases {
		_, err := h(ownerCtx(t), json.RawMessage(raw))
		if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
			t.Errorf("%s: err %v, want invalid-input", name, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused request reached the fetcher %d time(s)", len(*calls))
	}
	if res, err := h(ownerCtx(t), json.RawMessage(valid)); err != nil || res.(tools.PRDiff).Diff != fakePRDiff || len(*calls) != 1 {
		t.Fatalf("valid request: %v %v (fetches %d), want the diff", res, err, len(*calls))
	}
}

func TestReviewPRDiffRPC_CapsResponse(t *testing.T) {
	isolatePRDiffTest(t)
	over := []byte(strings.Repeat("Z", tools.MaxPRDiffBytes+1))
	fetch, _ := countingPRDiff(over, nil)
	res, err := reviewPRDiffHandler(fetch, prDiffClock())(ownerCtx(t), json.RawMessage(`{"owner":"octocat","repo":"Hello-World","number":1}`))
	if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput || res != nil {
		t.Fatalf("over-cap diff: res %T err %v, want invalid-input and no body", res, err)
	}
	if strings.Contains(err.Error(), "ZZZZ") {
		t.Fatal("the refusal carries the diff body")
	}
	atCap, _ := countingPRDiff(over[:tools.MaxPRDiffBytes], nil)
	if res, err := reviewPRDiffHandler(atCap, prDiffClock())(ownerCtx(t), json.RawMessage(`{"owner":"octocat","repo":"Hello-World","number":1}`)); err != nil || res.(tools.PRDiff).Bytes != tools.MaxPRDiffBytes {
		t.Fatalf("a diff at the cap: err %v, want it returned", err)
	}
	// The CLI re-checks the daemon's reply: over the cap, or Bytes not len(Diff).
	for name, reply := range map[string]tools.PRDiff{
		"over cap":       {Diff: string(over), Bytes: len(over)},
		"bytes mismatch": {Diff: fakePRDiff, Bytes: len(fakePRDiff) + 1},
	} {
		call := func(_ context.Context, _ string, _, out any) error { *(out.(*tools.PRDiff)) = reply; return nil }
		diff, err := prDiffViaDaemon(t.Context(), call, reviewPRDiffParams{Owner: "octocat", Repo: "Hello-World", Number: 1})
		if err == nil || diff != nil || strings.Contains(err.Error(), "ZZZZ") {
			t.Fatalf("%s: diff %d bytes err %v, want a refusal with no body", name, len(diff), err)
		}
	}
}

func TestReviewPRDiffRPCRateBound(t *testing.T) {
	isolatePRDiffTest(t)
	fetch, calls := countingPRDiff([]byte(fakePRDiff), nil)
	h := reviewPRDiffHandler(fetch, prDiffClock())
	var err error
	for range gateAppendBurst + 1 {
		_, err = h(ownerCtx(t), json.RawMessage(`{"owner":"octocat","repo":"Hello-World","number":1}`))
	}
	if k, _ := cascade.KindOf(err); k != cascade.KindQuotaExhausted || len(*calls) != gateAppendBurst {
		t.Fatalf("burst+1: err %v fetches %d, want quota-exhausted after %d", err, len(*calls), gateAppendBurst)
	}
}

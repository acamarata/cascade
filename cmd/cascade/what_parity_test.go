// Purpose: TestWhatAliasParity — the acceptance-criterion proof that
// `cascade what <query>` and `cascade recall what <query>` produce
// byte-identical output for the same query and flags, over the SAME fake
// recallDeps, driven through the real cobra tree. Split out of
// what_test.go under the 300-line file cap; what_test.go carries the
// mount/Hidden/flag-set/delegation proofs this harness does not need.
//
// This file deliberately imports neither "net" nor "net/http" so it runs
// in the fast, no-network unit lane (Art.7.2): the injected recallCallFunc
// seam is the only thing that would otherwise reach a socket.
//
// SPORT: cmd.cascade.cmd.what (ADD, P1-E22-W5-S47-T5).
package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// whatParityDeps builds a fake recallDeps whose Call seam records every
// invocation and answers with a fixed result or error — the ONE deps
// value both trees below are built from, so TestWhatAliasParity's
// comparison is of two mounts of the same deps, not two separately
// constructed "equivalent" ones.
func whatParityDeps(t *testing.T, result retrieval.WhatResult, callErr error) recallDeps {
	t.Helper()
	dir := t.TempDir()
	return recallDeps{
		Paths:   fakeMemoryPaths{root: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		Getwd:   func() (string, error) { return dir, nil },
		Call: func(_ context.Context, _, method string, _, out any) error {
			if callErr != nil {
				return callErr
			}
			if method != retrieval.MethodWhat {
				t.Fatalf("unexpected RPC method %q, want %q", method, retrieval.MethodWhat)
			}
			return reencode(result, out)
		},
	}
}

// whatParityRoot mounts BOTH `recall what` and the hidden `what` alias on
// a fresh minimal root, over the SAME deps value, declaring the same
// persistent global flags root.go declares (recallHarness.run's own
// convention) so --json resolves identically at either depth.
func whatParityRoot(deps recallDeps) *cobra.Command {
	root := &cobra.Command{Use: "cascade"}
	flags := root.PersistentFlags()
	flags.Bool("json", false, "")
	flags.Bool("quiet", false, "")
	flags.Bool("verbose", false, "")
	flags.Bool("no-color", false, "")
	root.AddCommand(newRecallCmd(deps))
	root.AddCommand(newWhatCmd(deps))
	return root
}

// whatParityRun executes one command path against a freshly built root
// (never reused across calls — pflag values persist across Execute()
// calls on the same FlagSet, so reusing one root for two runs would leak
// the first run's --json/--verbose state into the second).
func whatParityRun(t *testing.T, deps recallDeps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := whatParityRoot(deps)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	root.SilenceUsage = true
	root.SilenceErrors = true
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	err = root.ExecuteContext(ctx)
	return out.String(), errBuf.String(), err
}

// whatAliasParityCase is one TestWhatAliasParity input/fixture pairing.
type whatAliasParityCase struct {
	name     string
	result   retrieval.WhatResult
	callErr  error
	queryArg []string
}

// whatAliasParityCases covers table rendering, --json, an empty match,
// the error/exit-code path, and argument-validation refusal — every shape
// recall_what_test.go's own cases already exercise for the primary
// command, replayed here against both surfaces.
var whatAliasParityCases = []whatAliasParityCase{
	{
		name: "json",
		result: retrieval.WhatResult{
			Query:   "q",
			Results: []retrieval.RecallWhatResult{{Rank: 1, Domain: "file", ID: "file:c1", Path: "a/b.md", Score: 0.5}},
			Legs:    []string{"file"},
		},
		queryArg: []string{"q", "--json"},
	},
	{
		name: "table",
		result: retrieval.WhatResult{
			Results:   []retrieval.RecallWhatResult{{Rank: 1, Domain: "memory", ID: "memory:a", Score: 0.9}},
			Withheld:  2,
			Truncated: 1,
			Errors:    map[string]string{"file": "files index unreadable"},
		},
		queryArg: []string{"q"},
	},
	{
		name:     "no results",
		result:   retrieval.WhatResult{},
		queryArg: []string{"kumquat marmalade"},
	},
	{
		name:     "call error",
		callErr:  cascade.New(cascade.KindUnavailable, "recall.what: daemon unreachable"),
		queryArg: []string{"q"},
	},
	{
		name:     "missing query argument",
		queryArg: nil,
	},
}

// TestWhatAliasParity is the acceptance-criterion proof: `cascade what
// <query>` and `cascade recall what <query>` produce byte-identical
// output for the same query and flags, over the SAME fake deps, driven
// through the real cobra tree.
func TestWhatAliasParity(t *testing.T) {
	for _, tc := range whatAliasParityCases {
		t.Run(tc.name, func(t *testing.T) { assertWhatAliasParity(t, tc) })
	}
}

// assertWhatAliasParity runs one case's query through both `recall what`
// and the `what` alias, over the SAME deps, and diffs stdout, stderr, the
// error text/Kind, and the process exit code.
func assertWhatAliasParity(t *testing.T, tc whatAliasParityCase) {
	t.Helper()
	deps := whatParityDeps(t, tc.result, tc.callErr)

	primaryArgs := append([]string{"recall", "what"}, tc.queryArg...)
	pStdout, pStderr, pErr := whatParityRun(t, deps, primaryArgs...)

	aliasArgs := append([]string{"what"}, tc.queryArg...)
	aStdout, aStderr, aErr := whatParityRun(t, deps, aliasArgs...)

	if pStdout != aStdout {
		t.Errorf("stdout differs:\nprimary: %q\nalias:   %q", pStdout, aStdout)
	}
	if pStderr != aStderr {
		t.Errorf("stderr differs:\nprimary: %q\nalias:   %q", pStderr, aStderr)
	}
	if (pErr == nil) != (aErr == nil) {
		t.Fatalf("error-ness differs: primary=%v alias=%v", pErr, aErr)
	}
	if pErr != nil {
		if pErr.Error() != aErr.Error() {
			t.Errorf("error text differs: primary=%q alias=%q", pErr.Error(), aErr.Error())
		}
		pKind, pOK := cascade.KindOf(pErr)
		aKind, aOK := cascade.KindOf(aErr)
		if pOK != aOK || pKind != aKind {
			t.Errorf("error Kind differs: primary=(%v,%v) alias=(%v,%v)", pKind, pOK, aKind, aOK)
		}
	}
	if got, want := cascade.ExitCode(aErr), cascade.ExitCode(pErr); got != want {
		t.Errorf("exit code differs: primary=%d alias=%d", want, got)
	}
}

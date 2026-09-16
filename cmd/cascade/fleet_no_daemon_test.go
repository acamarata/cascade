package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): one property asserted across EVERY `cascade fleet`
//   verb at once — with no daemon reachable, each refuses with a typed
//   error and prints nothing.
//
// WHY AS A WALK. These verbs are written to one shape (resolve, dial,
//   call, render) and were tested one at a time, which is how a verb added
//   later gets none. The walk discovers the tree from the SAME constructor
//   production mounts, so a new verb is covered the day it is added rather
//   than the day someone remembers it.
//
// What "refuses cleanly" means, and why each half matters: a typed
//   *cascade.Error is what the CLI's own error path renders and what an
//   exit code is derived from — a bare error would print as an opaque
//   string. Empty stdout matters because a verb that renders a zero-valued
//   result alongside its error tells an operator the queue is empty when
//   in truth it was never read.
//
// Constraints: no network. The socket path is under t.TempDir() and does
//   not exist, so every dial fails immediately; each invocation also
//   carries its own deadline so a verb that DID block fails the test
//   rather than hanging the package.
// SPORT: cmd/cascade/fleet tests (ADD) — coverage ratchet, cmd/cascade.

// fleetVerbInvocation is one leaf command and the arguments that get it
// past cobra's own Args validation to its RunE body.
type fleetVerbInvocation struct {
	path []string
	args []string
}

// fleetVerbInvocations is the argument table. A leaf that needs an
// argument is listed with one; everything else runs bare. A leaf missing
// from this table still runs — with no arguments — so forgetting to add a
// row cannot silently skip a verb.
var fleetVerbInvocations = []fleetVerbInvocation{
	{path: []string{"attention", "ack"}, args: []string{"01ATTENTIONITEMID000000000"}},
	{path: []string{"attention", "open"}, args: []string{"01ATTENTIONITEMID000000000"}},
	{path: []string{"jobs", "show"}, args: []string{"01JOBID0000000000000000000"}},
	{path: []string{"jobs", "cancel"}, args: []string{"01JOBID0000000000000000000"}},
	{path: []string{"jobs", "retry"}, args: []string{"01JOBID0000000000000000000"}},
	{path: []string{"leases", "release"}, args: []string{"01LEASEID00000000000000000"}},
	{path: []string{"mode", "set"}, args: []string{"manual"}},
}

// argsFor returns the invocation arguments for a leaf's path.
func argsFor(path []string) []string {
	for _, inv := range fleetVerbInvocations {
		if strings.Join(inv.path, " ") == strings.Join(path, " ") {
			return inv.args
		}
	}
	return nil
}

// collectFleetLeaves walks the fleet subtree and returns every runnable
// leaf's path relative to `fleet`.
func collectFleetLeaves(cmd *cobra.Command, prefix []string) [][]string {
	var out [][]string
	for _, child := range cmd.Commands() {
		path := append(append([]string{}, prefix...), child.Name())
		if len(child.Commands()) > 0 {
			out = append(out, collectFleetLeaves(child, path)...)
			continue
		}
		if child.RunE != nil || child.Run != nil {
			out = append(out, path)
		}
	}
	return out
}

// notDaemonOnly names the fleet leaves that deliberately DO work without a
// daemon, with the reason each is excluded from the refusal walk. It is a
// small allowlist rather than a silent skip so that a verb quietly gaining
// a daemonless path has to be added here, in writing.
//
//   - top: an interactive TUI. It reads the terminal and does not return on
//     its own, so it has no non-blocking refusal path to assert.
//   - sessions: detects locally-running agent processes by scanning the
//     HOST, which is both a real daemonless mode and the reason it cannot
//     join this walk — its result depends on what happens to be running on
//     the machine, which no unit test may depend on.
//   - usage: reads the local usage ledger and correctly reports an empty
//     one; there is nothing to refuse.
var notDaemonOnly = map[string]string{
	"top":      "interactive TUI; no non-blocking refusal path",
	"sessions": "scans host processes; a real daemonless mode, and non-hermetic here",
	"usage":    "reads the local ledger; an empty one is a valid answer, not a refusal",
}

// TestEveryFleetVerbRefusesWithNoDaemon is the walk.
func TestEveryFleetVerbRefusesWithNoDaemon(t *testing.T) {
	deps := fleetSessionsDeps{
		Paths:   fakeDaemonPaths{root: t.TempDir()},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	leaves := collectFleetLeaves(newFleetCmd(deps), nil)
	if len(leaves) < 8 {
		t.Fatalf("the walk found %d fleet leaves (%v); the tree has more than that, so it is not walking", len(leaves), leaves)
	}
	// An allowlist entry naming a verb that no longer exists would silently
	// excuse nothing while looking like it still excused something.
	present := map[string]bool{}
	for _, leaf := range leaves {
		present[leaf[0]] = true
	}
	for name := range notDaemonOnly {
		if !present[name] {
			t.Errorf("notDaemonOnly names %q, which is not in the fleet tree; remove the entry", name)
		}
	}

	for _, leaf := range leaves {
		name := strings.Join(leaf, " ")
		t.Run(name, func(t *testing.T) {
			if reason, ok := notDaemonOnly[leaf[0]]; ok {
				t.Skip(reason)
			}
			assertFleetVerbRefuses(t, deps, leaf)
		})
	}
}

// assertFleetVerbRefuses runs one leaf against a socket that does not exist
// and holds the three halves of a clean refusal: it returns, it returns a
// TYPED error, and it prints nothing.
func assertFleetVerbRefuses(t *testing.T, deps fleetSessionsDeps, leaf []string) {
	t.Helper()
	var stdout bytes.Buffer
	root := newFleetTestRoot(deps, &stdout)
	root.SetArgs(append(append([]string{"fleet"}, leaf...), argsFor(leaf)...))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)

	if err == nil {
		t.Fatalf("succeeded with no daemon reachable; stdout = %q", stdout.String())
	}
	if _, typed := cascade.KindOf(err); !typed {
		t.Errorf("err = %v (%T), want a typed *cascade.Error the CLI can render and derive an exit code from", err, err)
	}
	if ctx.Err() != nil {
		t.Error("the verb was still running at the deadline; it has no bounded refusal path")
	}
	if stdout.Len() != 0 {
		t.Errorf("printed a result despite failing: %q", stdout.String())
	}
}

// newFleetTestRoot builds a root carrying the same persistent global flags
// the real one does, with the fleet subtree mounted from the production
// constructor.
func newFleetTestRoot(deps fleetSessionsDeps, stdout *bytes.Buffer) *cobra.Command {
	root := &cobra.Command{Use: "cascade", SilenceUsage: true, SilenceErrors: true}
	for _, f := range []string{"json", "quiet", "verbose", "no-color"} {
		root.PersistentFlags().Bool(f, false, "")
	}
	root.AddCommand(newFleetCmd(deps))
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader(""))
	return root
}

// TestFleetAttentionListRejectsAnUnknownKindBeforeDialing holds the order
// the list command is written in: an invalid --kind is a caller mistake
// and must be reported as one, rather than surfacing as whatever the dial
// happened to fail with. The two errors are different KINDS, which is how
// the assertion tells them apart.
func TestFleetAttentionListRejectsAnUnknownKindBeforeDialing(t *testing.T) {
	deps := fleetSessionsDeps{
		Paths:   fakeDaemonPaths{root: t.TempDir()},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	root := newFleetTestRoot(deps, &bytes.Buffer{})
	root.SetArgs([]string{"fleet", "attention", "list", "--kind", "not-a-kind"})

	err := root.ExecuteContext(context.Background())
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput rather than the dial's own failure", err)
	}
	if !strings.Contains(err.Error(), "not-a-kind") {
		t.Errorf("err = %q, want it to name the rejected value", err)
	}
}

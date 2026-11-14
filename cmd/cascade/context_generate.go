// Purpose: `cascade context generate [--check]`, the operator verb that
// writes (or checks) a repository's generated harness files.
// Inputs: the working directory's git top level, the daemonless state the
// root command attached to the context, the workflows support probe.
// Outputs: the generation result or the drift report on stdout; in the
// embedded path one stderr line per refused target; a typed error on drift
// or on a failed leg.
// Constraints: daemon first through internal/client (context.generate), the
// in-process generate.Run/Check otherwise; never prompts, so
// CASCADE_NO_INPUT changes nothing.
// SPORT: cli/context-generate/ADD.

package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/context/generate"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// contextGenerateTimeout bounds the daemon round trip: generation registers
// and scans the repository, so it gets longer than a read verb's 5 s.
const contextGenerateTimeout = 2 * time.Minute

// contextGenerateRPC is the daemon method this verb dials; it equals the
// method the daemon registers.
const contextGenerateRPC = "context.generate"

// contextGenerateProbe builds the workflows probe of the in-process path;
// tests replace it with a recorded replay so no harness binary runs.
var contextGenerateProbe = func() generate.WorkflowsSupportProbe { return generate.NewExecProbe() }

// newContextGenerateCmd builds `context generate [--check]`.
func newContextGenerateCmd(deps contextScopeDeps) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Write or check this repository's generated harness files",
		Long: "Renders the rules, agents and skills, workflows and Codex/OpenCode files of the\n" +
			"repository holding the working directory, then writes the generation manifest.\n" +
			"A file whose bytes outside the managed blocks changed is left alone; the new\n" +
			"rendering goes to <file>.cascade-new and the refusal is reported.\n\n" +
			"--check writes nothing and exits non-zero naming every drifted path.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := contextGenerateRoot(cmd.Context())
			if err != nil {
				return err
			}
			if check {
				return runContextGenerateCheck(cmd, deps, root)
			}
			res, err := fetchContextGenerate(cmd.Context(), deps, root, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return contextScopeOutputWriter(cmd).Result(generateResultView{res})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "report drift without writing; exit non-zero on any drift")
	return cmd
}

// runContextGenerateCheck prints the drift report and fails when it is not clean.
func runContextGenerateCheck(cmd *cobra.Command, deps contextScopeDeps, root string) error {
	drift, err := fetchContextGenerateCheck(cmd.Context(), deps, root)
	if err != nil {
		return err
	}
	if err := contextScopeOutputWriter(cmd).Result(generateDriftView{drift}); err != nil {
		return err
	}
	if drift.Clean() {
		return nil
	}
	paths := make([]string, 0, len(drift.Items))
	for _, it := range drift.Items {
		paths = append(paths, it.Path+" ("+it.Condition+")")
	}
	return cascade.Newf(cascade.KindConflict, "cascade context generate --check: %d drifted: %s",
		len(paths), strings.Join(paths, ", "))
}

// contextGenerateRoot resolves the working directory's git top level.
func contextGenerateRoot(ctx context.Context) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "cascade context generate: resolve cwd")
	}
	dirs, err := repoGitCommonDir(ctx, wd)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "cascade context generate: the working directory is not in a git repository")
	}
	root, err := filepath.EvalSymlinks(dirs.Toplevel)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "cascade context generate: resolve the repository root")
	}
	return root, nil
}

// contextGenerateClient returns a client when a daemon is live, nil otherwise.
func contextGenerateClient(ctx context.Context, deps contextScopeDeps) (*client.Client, error) {
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || st.Embedded {
		return nil, nil
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextGenerateTimeout), nil
}

// fetchContextGenerate runs generation in the daemon, or in process.
func fetchContextGenerate(ctx context.Context, deps contextScopeDeps, root string, stderr io.Writer) (generate.Result, error) {
	c, err := contextGenerateClient(ctx, deps)
	if err != nil {
		return generate.Result{}, err
	}
	if c != nil {
		var res generate.Result
		err := c.Do(ctx, contextGenerateRPC, map[string]any{"repo": root, "check": false}, &res)
		return res, err
	}
	clock := runtime.NewSystemClock()
	return generate.Run(ctx, root, generate.RunDeps{
		Clock: clock, Attention: stderrGenerateSink(stderr), Probe: contextGenerateProbe(),
		Facts: generateLedgerFacts{paths: deps.Paths, clock: clock},
		Register: func(ctx context.Context, root string) (repo.Inventory, error) {
			return registerRepository(ctx, deps.Paths, clock, root)
		},
	})
}

// fetchContextGenerateCheck runs the read-only check in the daemon, or in process.
func fetchContextGenerateCheck(ctx context.Context, deps contextScopeDeps, root string) (generate.Drift, error) {
	c, err := contextGenerateClient(ctx, deps)
	if err != nil {
		return generate.Drift{}, err
	}
	if c == nil {
		return generate.Check(ctx, root)
	}
	var out struct {
		Drift generate.Drift `json:"drift"`
	}
	err = c.Do(ctx, contextGenerateRPC, map[string]any{"repo": root, "check": true}, &out)
	return out.Drift, err
}

// stderrGenerateSink reports each refusal as exactly one stderr line.
func stderrGenerateSink(stderr io.Writer) generate.AttentionSink {
	return func(_ context.Context, target, sidecar, reason string) error {
		line := "cascade: context generate refused " + target + ": " + reason
		if sidecar != "" {
			line += " (rendered to " + sidecar + ")"
		}
		if _, err := fmt.Fprintln(stderr, line); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "cascade context generate: report refusal")
		}
		return nil
	}
}

// generateLedgerFacts reads accepted facts from cascade.db for the in-process path.
type generateLedgerFacts struct {
	paths runtime.PathProvider
	clock runtime.Clock
}

// AcceptedFacts reads through the shared ledger service and its policy filter.
func (f generateLedgerFacts) AcceptedFacts(ctx context.Context, repoID string) ([]repo.AcceptedFact, []string, error) {
	var facts []repo.AcceptedFact
	var denied []string
	err := withRepoDB(ctx, f.paths, f.clock, func(db *sql.DB) error {
		svc, err := repo.NewLedgerService(repo.NewStore(db), repo.NewLedgerStore(db))
		if err != nil {
			return err
		}
		facts, denied, err = svc.AcceptedFacts(ctx, repoID)
		return err
	})
	return facts, denied, err
}

// generateResultView renders a generation result for humans; JSON keeps the fields.
type generateResultView struct{ generate.Result }

// String lists every written, adopted and refused path and any deferral.
func (v generateResultView) String() string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%d written, %d unchanged, %d refused", len(v.Written), len(v.Adopted), len(v.Refused))
	for _, p := range v.Written {
		_, _ = fmt.Fprintf(&b, "\nwritten  %s", p)
	}
	for _, o := range v.Refused {
		_, _ = fmt.Fprintf(&b, "\nrefused  %s (%s; new rendering in %s)", o.Path, o.Reason, o.Sidecar)
	}
	if d := v.Deferral; d != nil {
		_, _ = fmt.Fprintf(&b, "\ndeferred %s: %s (owner %s, expires %s)", d.Generator, d.Reason, d.Owner, d.Expiry)
	}
	return b.String()
}

// generateDriftView renders the --check report for humans.
type generateDriftView struct {
	Drift generate.Drift `json:"drift"`
}

// String names each drifted path and its condition, or says the tree is clean.
func (v generateDriftView) String() string {
	if v.Drift.Clean() {
		return "clean: every generated file matches the manifest"
	}
	var b strings.Builder
	for i, it := range v.Drift.Items {
		if i > 0 {
			b.WriteByte('\n')
		}
		_, _ = fmt.Fprintf(&b, "%s\t%s", it.Path, it.Condition)
	}
	return b.String()
}

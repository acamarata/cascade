package migration

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the bulk multi-project driver `cascade context sync
//
//	--project-list <file>` calls (P1-E26-W10-S53-T3). Per R-14.81 there is
//	no separate `migrate v1 regen-instructions` subcommand: this package
//	is the scanner + drift aggregator the existing `context sync` surface
//	mounts its --project-list flag onto (cmd/cascade/context_sync_cmd.go).
//	It never re-implements generation: every byte written or compared
//	comes from internal/context's own Discover/MergeTiers/HarnessGenerator
//	pipeline (S-08/S-09), called directly, per this ticket's Article-1
//	requirement.
//
// Inputs: a project-list file (one directory per line) merged with any
//
//	paths a caller already knows about; per-run CheckOnly/Yes/NoInput
//	flags and an optional Confirm callback for interactive TTY gating.
//
// Outputs: a Report (JSON-tagged for --json, String()'d for human mode in
//
//	report.go) describing, per project, which harness files would change
//	and — outside --check — which ones did. A typed cascade.Error when one
//	or more projects could not even be scanned; the report itself is still
//	returned alongside it (partial success), never discarded.
//
// Constraints: no bare time/rand (Art.7.3 — this package has no clock or
//
//	randomness dependency at all); pure file I/O, no OS-specific backend,
//	so the same code path runs on darwin/linux/windows without a
//	per-platform branch (Article-5). Never writes without one of a TTY
//	confirmation, --yes, or an explicit non---check invocation gated by
//	decideApply — see that function for the exact CASCADE_NO_INPUT/--yes
//	truth table this ticket's contract fixes.
//
// SPORT: internal/migration [ADD] (P1-E26-W10-S53-T3 sport_updates).

// DriftEntry describes one harness instruction file, for one project, whose
// on-disk content differs from what internal/context's real generators
// would produce right now.
type DriftEntry struct {
	// HarnessFile is the on-disk path the file would be written to.
	HarnessFile string `json:"harness_file"`
	// Added and Removed are line counts from diffLines (diff.go).
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	DiffBody string `json:"diff_body"`
}

// ProjectReport is one project's scan outcome: either a (possibly empty)
// drift list, or a scan Error that stopped this project's scan early
// without stopping the run (Run continues to the next project — see
// "unreadable project dir" in this ticket's acceptance criteria).
type ProjectReport struct {
	ProjectPath string       `json:"project_path"`
	Drift       []DriftEntry `json:"drift,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// AppliedFile records one write Run actually performed (apply mode only).
type AppliedFile struct {
	ProjectPath string `json:"project_path"`
	Path        string `json:"path"`
	Action      string `json:"action"`
}

// Report is the drift report emitted to stdout: human-readable via
// String() (report.go), machine-readable via json.Marshal under --json
// (internal/output.Writer.Result already does both from this one value).
type Report struct {
	Projects []ProjectReport `json:"projects"`
	Applied  []AppliedFile   `json:"applied,omitempty"`
	// Partial is true when at least one project's scan failed; the run
	// still completed for every other project.
	Partial bool `json:"partial"`
}

// RunOptions configures one Run.
type RunOptions struct {
	// ProjectPaths is the deduplicated set to scan, already merged by
	// MergeProjectPaths.
	ProjectPaths []string
	// HomeDir is passed straight through to Discover; nil defers to
	// os.UserHomeDir in production, and lets tests inject a temp HOME.
	HomeDir cascadecontext.HomeDirFunc
	// CheckOnly makes the run purely read-only regardless of Yes/NoInput.
	CheckOnly bool
	// Yes bypasses both the TTY prompt and the CASCADE_NO_INPUT refusal:
	// the operator has already said yes for every project in this run.
	Yes bool
	// NoInput mirrors CASCADE_NO_INPUT=1: no prompt is ever attempted.
	NoInput bool
	// Confirm asks whether to apply path's drift; called once per project
	// that has drift, only when CheckOnly, Yes and NoInput are all false.
	// A nil Confirm behaves as an unconditional "no" (never used outside
	// an interactive TTY caller).
	Confirm func(projectPath string) (bool, error)
}

// LoadProjectList reads path, one project directory per line; blank lines
// and lines starting with '#' are skipped. A relative line is resolved
// against base (the caller's cwd). An empty path returns (nil, nil): the
// --project-list flag is optional.
func LoadProjectList(path, base string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return nil, cascade.Newf(cascade.KindNotFound, "migration: project list %q does not exist", path)
	case err != nil:
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "migration: stat project list %q", path)
	case info.IsDir():
		return nil, cascade.Newf(cascade.KindInvalidInput, "migration: project list %q is a directory, not a file", path)
	}
	return readProjectListLines(path, base)
}

// readProjectListLines does the actual line-by-line read, split out so
// LoadProjectList's own validation stays a single, short function.
func readProjectListLines(path, base string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // path is operator-supplied CLI input (the --project-list flag), not derived from untrusted content.
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "migration: open project list %q", path)
	}
	defer func() { _ = f.Close() }()

	var paths []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !filepath.IsAbs(line) {
			line = filepath.Join(base, line)
		}
		paths = append(paths, filepath.Clean(line))
	}
	if err := scanner.Err(); err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "migration: read project list %q", path)
	}
	return paths, nil
}

// MergeProjectPaths dedupes list ++ registered, preserving first-seen
// order with list entries taking precedence: list is the operator's
// explicit intent for this run, registered rides along after it.
func MergeProjectPaths(list, registered []string) []string {
	seen := make(map[string]struct{}, len(list)+len(registered))
	out := make([]string, 0, len(list)+len(registered))
	for _, group := range [][]string{list, registered} {
		for _, p := range group {
			c := filepath.Clean(p)
			if _, ok := seen[c]; ok {
				continue
			}
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}

// decideApply is the whole of this ticket's "no silent overwrite" truth
// table for one project that has drift:
//
//	CheckOnly           -> never reached; Run skips apply entirely.
//	Yes                 -> true, no prompt (works under NoInput too).
//	NoInput && !Yes     -> false, no prompt, no error (report-then-exit-0).
//	interactive, no Yes -> Confirm's answer (nil Confirm means "no").
func decideApply(projectPath string, opts RunOptions) (bool, error) {
	if opts.Yes {
		return true, nil
	}
	if opts.NoInput {
		return false, nil
	}
	if opts.Confirm == nil {
		return false, nil
	}
	return opts.Confirm(projectPath)
}

// Run scans every path in opts.ProjectPaths, then — unless CheckOnly —
// applies confirmed projects' drift by calling internal/context.Sync's own
// regenerate path (the exact function E/S-09.T4 wired to `context sync`'s
// non---check mode), never a second write implementation.
func Run(ctx context.Context, opts RunOptions) (Report, error) {
	var report Report
	var firstErr error
	for _, p := range opts.ProjectPaths {
		pr, applyErr := runOneProject(ctx, p, opts)
		if pr.Error != "" {
			report.Partial = true
			if firstErr == nil {
				firstErr = applyErr
			}
		}
		report.Projects = append(report.Projects, pr.ProjectReport)
		report.Applied = append(report.Applied, pr.Applied...)
	}
	if firstErr != nil {
		kind, ok := cascade.KindOf(firstErr)
		if !ok {
			kind = cascade.KindInternal
		}
		return report, cascade.Wrapf(kind, firstErr, "migration: one or more of %d project(s) failed", len(opts.ProjectPaths))
	}
	return report, nil
}

// oneProjectResult carries runOneProject's outcome so Run's own loop body
// stays a handful of lines.
type oneProjectResult struct {
	ProjectReport
	Applied []AppliedFile
	err     error
}

// runOneProject scans p, decides whether to apply, and applies when
// confirmed — the entire per-project lifecycle Run's loop drives.
func runOneProject(ctx context.Context, p string, opts RunOptions) (oneProjectResult, error) {
	entries, err := scanOneProject(ctx, p, opts.HomeDir)
	if err != nil {
		return oneProjectResult{ProjectReport: ProjectReport{ProjectPath: p, Error: err.Error()}, err: err}, err
	}
	res := oneProjectResult{ProjectReport: ProjectReport{ProjectPath: p, Drift: entries}}
	if opts.CheckOnly || len(entries) == 0 {
		return res, nil
	}
	apply, err := decideApply(p, opts)
	if err != nil {
		res.Error = err.Error()
		res.err = err
		return res, err
	}
	if !apply {
		return res, nil
	}
	sr, err := cascadecontext.Sync(ctx, p, opts.HomeDir, false)
	for _, f := range sr.Files {
		if f.Action == cascadecontext.ActionUnchanged {
			continue
		}
		res.Applied = append(res.Applied, AppliedFile{ProjectPath: p, Path: f.Path, Action: actionName(f.Action)})
	}
	if err != nil {
		res.Error = err.Error()
		res.err = err
	}
	return res, res.err
}

// actionName renders a cascadecontext.WriteAction as the short lowercase
// word this ticket's report uses, mirroring internal/context's own
// harnessName convention for the same "small, stable, product-neutral
// string" reason.
func actionName(a cascadecontext.WriteAction) string {
	switch a {
	case cascadecontext.ActionCreated:
		return "created"
	case cascadecontext.ActionUpdated:
		return "updated"
	case cascadecontext.ActionAppended:
		return "appended"
	case cascadecontext.ActionBackedUp:
		return "backed-up"
	case cascadecontext.ActionUnchanged:
		return "unchanged"
	default:
		return "unchanged"
	}
}

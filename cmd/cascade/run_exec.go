// Purpose: `cascade run`'s execution half - split from run.go (which owns
//
//	the cobra command, flags and pure validation) to stay under Art.10.3's
//	300-line/file cap, the same split pattern this codebase already uses
//	(envelope.go/output.go, daemon_unix.go/daemon_unix_run.go). Holds the
//	blocking dispatch through internal/client.Client, non-interactive
//	JSON-envelope/TTY rendering, and the real --stream GET
//	/events?filter=job:<id> reader.
//
// Inputs: runFlags/runDeps from run.go; a *cobra.Command for its
//
//	Context/OutOrStdout/InOrStdin/flag access.
//
// Outputs: process output via internal/output.Writer; a taxonomy error on
//
//	failure.
//
// Constraints: dials the daemon exclusively through internal/client.Client
//
//	(hard requirement 1) - never internal/rpc. streamResult never blocks
//	past ctx or body's own EOF/error (bounded, no infinite read).
//
// SPORT: cmd/cascade/run (ADD, P1-E11-W3-S23-T1/T3).
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// runOutputWriter builds the Writer for this invocation, forcing JSON
// mode on in non-interactive automation (CASCADE_NO_INPUT=1 or a non-TTY
// stdout, §5.8) even when --json was not passed - the ticket's own
// non-interactive envelope requirement, layered onto the repo's existing
// versioned --json contract (see run.go's header note on this).
func runOutputWriter(cmd *cobra.Command, deps runDeps) *output.Writer {
	jsonFlag, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	nonInteractive := deps.Getenv("CASCADE_NO_INPUT") == "1" || !output.IsTerminal(cmd.OutOrStdout())
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonFlag || nonInteractive, quiet, verbose, noColor)
}

// resolveRunSocket mirrors status.go's resolveStatusSocket exactly (that
// function takes statusDeps, an unrelated type, so it cannot be called
// directly).
func resolveRunSocket(ctx context.Context, deps runDeps) (daemon.Settings, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: deps.Paths.ConfigPath(), Getenv: deps.Getenv, Environ: deps.Environ})
	if err != nil {
		return daemon.Settings{}, cascade.Wrap(cascade.KindInvalidInput, err, "cascade run: load config.toml")
	}
	return daemon.ResolveSettings(cfg, deps.Paths)
}

// runResultView is the --json/non-interactive envelope's data shape:
// output/job_id/cost per the ticket text, plus legs (R-21.214) - always a
// non-nil (possibly empty) array so the shape is uniform for machine
// consumers.
type runResultView struct {
	Output string         `json:"output"`
	JobID  string         `json:"job_id"`
	Cost   provider.Usage `json:"cost"`
	Legs   []runLegView   `json:"legs"`
}

type runLegView struct {
	Output string         `json:"output"`
	JobID  string         `json:"job_id"`
	Cost   provider.Usage `json:"cost"`
}

// toRunResultView projects a provider.ModelResponse onto the wire view,
// R-21.214: a fan-out parent's own Output stays empty and Legs carries
// each leg in index order; a single dispatch's Legs is [] (non-nil,
// empty), never omitted or null.
func toRunResultView(resp provider.ModelResponse) runResultView {
	legs := make([]runLegView, 0, len(resp.Legs))
	for _, leg := range resp.Legs {
		legs = append(legs, runLegView{Output: leg.Output, JobID: string(leg.JobID), Cost: leg.Usage})
	}
	return runResultView{Output: resp.Output, JobID: string(resp.JobID), Cost: resp.Usage, Legs: legs}
}

// renderRunResult renders resp per the R-21.214 TTY/non-interactive
// split: TTY prints each leg's output in index order (or resp.Output on a
// single dispatch) then a job_id/cost/leg_count info line to stderr;
// non-interactive emits exactly one runResultView through w.Result.
func renderRunResult(w *output.Writer, resp provider.ModelResponse) error {
	if w.Mode().JSON {
		return w.Result(toRunResultView(resp))
	}
	if len(resp.Legs) == 0 {
		w.Println(resp.Output)
	} else {
		for i, leg := range resp.Legs {
			if i > 0 {
				w.Println("")
			}
			w.Println(leg.Output)
		}
	}
	w.Warn("job_id=%s cost=%+v leg_count=%d", resp.JobID, resp.Usage, len(resp.Legs))
	return nil
}

// fetchRun dials the daemon and issues conductor.execute, decoding
// straight into pkg/provider.ModelResponse (R-21.214's own wire type) -
// the ONLY call site in this file that reaches the daemon (hard
// requirement 1).
func fetchRun(ctx context.Context, deps runDeps, params runRequestParams) (provider.ModelResponse, error) {
	settings, err := resolveRunSocket(ctx, deps)
	if err != nil {
		return provider.ModelResponse{}, err
	}
	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), runDialTimeout)
	var resp provider.ModelResponse
	if err := c.Do(ctx, "conductor.execute", params, &resp); err != nil {
		return provider.ModelResponse{}, err
	}
	return resp, nil
}

// runRunCmd is RunE's body: assemble params, dispatch, then either render
// the blocking result or, under --stream, open the real event stream for
// the returned job_id.
func runRunCmd(cmd *cobra.Command, deps runDeps, flags runFlags) error {
	params, err := buildRunParams(flags, cmd.InOrStdin())
	if err != nil {
		return err
	}
	w := runOutputWriter(cmd, deps)
	resp, err := fetchRun(cmd.Context(), deps, params)
	if err != nil {
		return err
	}
	if !flags.Stream {
		return renderRunResult(w, resp)
	}
	settings, err := resolveRunSocket(cmd.Context(), deps)
	if err != nil {
		return err
	}
	body, closeFn, err := openEventStream(cmd.Context(), deps, settings.SocketPath, string(resp.JobID))
	if err != nil {
		return err
	}
	defer closeFn()
	return streamResult(cmd.Context(), cmd.OutOrStdout(), body)
}

// openEventStream dials the daemon's real GET /events?filter=job:<id>
// endpoint through the same DialFunc internal/client.UnixDialer uses -
// production wiring, not a self-authored transport. Returns the still-open
// response body plus its closer. No client-level timeout: a subscription
// is long-lived by design, bounded by ctx only.
func openEventStream(ctx context.Context, deps runDeps, socketPath, jobID string) (io.ReadCloser, func(), error) {
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(c context.Context, _, _ string) (net.Conn, error) { return deps.DialContext(c, socketPath) },
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/events?filter=job:"+jobID, nil)
	if err != nil {
		return nil, func() {}, cascade.Wrap(cascade.KindInternal, err, "cascade run: build events request")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, func() {}, cascade.Wrap(cascade.KindUnavailable, err, "cascade run: dial GET /events")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, func() {}, cascade.Newf(cascade.KindUnavailable, "cascade run: GET /events returned status %d", resp.StatusCode)
	}
	return resp.Body, func() { _ = resp.Body.Close() }, nil
}

// streamResult reads SSE "data:" lines from body and writes each payload
// to out, one line at a time, until body closes/EOFs or ctx is cancelled -
// never a bare blocking read. It never panics on malformed input
// (FuzzStreamResult, run_fuzz_test.go).
func streamResult(ctx context.Context, out io.Writer, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if _, err := fmt.Fprintln(out, payload); err != nil {
			return cascade.Wrap(cascade.KindInternal, err, "cascade run: write stream payload")
		}
	}
	return nil
}

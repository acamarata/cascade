// Purpose: `cascade recall <query>` (07-CLI-COMMAND-TREE §recall; rpc:
//
//	recall.*) — the fused, cited query surface over the retrieval index.
//	This file is the CLI half: the cobra command, the injected call seam
//	that reaches the daemon through the Go IPC client SDK
//	(internal/client, never a hand-rolled JSON-RPC request — .golangci.yml's
//	cmd-rpc-server-boundary depguard rule), the flag surface, and the
//	rendering.
//
// Inputs: cobra args/flags; a recallDeps injected at construction so no
//
//	test touches the real environment or a real socket (Art.7.1).
//
// Outputs: process output via internal/output only; a typed taxonomy error
//
//	on failure, with its message scrubbed of machine paths and
//	secret-shaped values (memory_view.go's scrubDiagnostic, shared rather
//	than re-implemented).
//
// Constraints: one-shot and non-interactive — nothing here prompts, so
//
//	CASCADE_NO_INPUT has no effect (06 §5 rule 8). No platform-specific
//	imports (Art.5). A query that matched nothing prints that it matched
//	nothing and exits zero; every refusal carries a taxonomy Kind and its
//	mapped exit code. Nothing about a withheld result is ever printed
//	beyond the count the daemon already reduced it to.
//
// SPORT: cmd.cascade.cmd.recall (ADD, per T-3 sport_updates).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recallDialTimeout bounds the whole dial+request+response round trip
// against the daemon's local unix socket. A fixed duration literal, not a
// clock read (Art.7.3 governs bare time.Now/After/Tick/Sleep).
const recallDialTimeout = 15 * time.Second

// recallPathWidth caps the path column so one deep path cannot wrap a
// whole terminal into unreadability.
const recallPathWidth = 60

// recallCallFunc performs one JSON-RPC call against the daemon at
// socketPath, decoding the result into out. It is the injected seam this
// file's tests substitute, for the reason memoryCallFunc documents: a
// test that supplied a net.Conn would have to import "net", which the
// no-network unit lane (Art.7.2) forbids in an untagged _test.go file.
type recallCallFunc func(ctx context.Context, socketPath, method string, params, out any) error

// recallDeps carries every external input `cascade recall` needs.
type recallDeps struct {
	Paths   runtime.PathProvider
	Getenv  runtime.Getenv
	Environ func() []string
	// Call reaches the daemon. Production uses the SDK client; tests
	// substitute a recorder.
	Call recallCallFunc
}

// productionRecallDeps builds recallDeps against the real environment.
func productionRecallDeps() recallDeps {
	return recallDeps{
		Paths:   lazyPaths{},
		Getenv:  os.Getenv,
		Environ: os.Environ,
		Call:    clientRecallCall,
	}
}

// clientRecallCall is the production recallCallFunc: one SDK client per
// invocation, dialing the real unix socket. The whole JSON-RPC framing
// lives in internal/client; nothing is assembled here.
func clientRecallCall(ctx context.Context, socketPath, method string, params, out any) error {
	c := client.New(socketPath, client.UnixDialer, recallDialTimeout)
	return c.Do(ctx, method, params, out)
}

// mountRecallCmd attaches the top-level `recall` command, following
// mountMemoryCmd's exact pattern.
func mountRecallCmd(root *cobra.Command) {
	root.AddCommand(newRecallCmd(productionRecallDeps()))
}

// newRecallCmd builds `cascade recall`.
func newRecallCmd(deps recallDeps) *cobra.Command {
	var params recall.QueryParams
	cmd := &cobra.Command{
		Use:   "recall <query>",
		Short: "Search the retrieval index and print cited results",
		Long: "Run <query> against the retrieval index and print the fused, ranked\n" +
			"results with the corpus and trust tag behind each one.\n\n" +
			"Results are ranked by reciprocal rank fusion over every retrieval\n" +
			"leg this build has available; --k caps how many are returned and\n" +
			"--corpus narrows the search to named corpora. Nothing outside the\n" +
			"scope named by --scope is searched, ranked or cited.\n\n" +
			"A query that matches nothing prints that and exits 0. A malformed\n" +
			"query, an unknown corpus and an unreadable index each fail with a\n" +
			"typed error and its own exit code, so an empty answer is never\n" +
			"confused with a broken index.",
		Example: "  cascade recall \"reciprocal rank fusion\"\n" +
			"  cascade recall \"retry policy\" --corpus handbook --k 5 --cite\n" +
			"  cascade recall \"retry policy\" --json",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			params.Query = args[0]
			var result recall.QueryResult
			if err := recallCall(cmd, deps, recall.MethodQuery, params, &result); err != nil {
				return err
			}
			return recallWriter(cmd).Result(recallView{result: result, cite: params.Cite})
		},
	}
	flags := cmd.Flags()
	flags.StringSliceVar(&params.Corpus, "corpus", nil, "restrict the search to named corpora (repeatable)")
	flags.StringVar(&params.Scope, "scope", "", "the session scope to search within")
	flags.IntVar(&params.K, "k", 0, "maximum number of results to return (default 10)")
	flags.BoolVar(&params.Cite, "cite", false, "print the Markdown citation block under the results")
	return cmd
}

// recallCall resolves the daemon socket and performs one RPC, scrubbing
// every error on the way out. Scrubbing happens HERE, at the boundary, so
// no call site can forget it.
func recallCall(cmd *cobra.Command, deps recallDeps, method string, params, out any) error {
	socketPath, err := recallResolveSocket(cmd.Context(), deps)
	if err != nil {
		return scrubDiagnostic(err)
	}
	return scrubDiagnostic(deps.Call(cmd.Context(), socketPath, method, params, out))
}

// recallResolveSocket loads config.toml — the single resolution model every
// CLI command uses (08 §2) — and resolves the daemon's socket path from it.
func recallResolveSocket(ctx context.Context, deps recallDeps) (string, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    deps.Paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "cascade recall: load config.toml")
	}
	settings, err := daemon.ResolveSettings(cfg, deps.Paths)
	if err != nil {
		return "", err
	}
	return settings.SocketPath, nil
}

// recallWriter builds this command's output.Writer from the resolved global
// flags, mirroring memoryWriter's established local convention.
func recallWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// recallView renders recall.query's result. The embedded result is the
// --json payload's own shape, so the human table and the JSON envelope
// cannot describe different answers.
type recallView struct {
	result recall.QueryResult
	cite   bool
}

// MarshalJSON emits the RPC result verbatim, so --json is the wire shape
// and not a second, drifting rendering of it.
func (v recallView) MarshalJSON() ([]byte, error) { return json.Marshal(v.result) }

// String renders the ranked results as a table, then the withheld count,
// then — only when asked — the citation block.
//
// "no results" is printed in full words rather than left as an empty
// table: a command that printed nothing would be indistinguishable from
// one that failed to run.
func (v recallView) String() string {
	var buf bytes.Buffer
	if len(v.result.Results) == 0 {
		buf.WriteString("no results")
	} else {
		writeRecallTable(&buf, v.result.Results)
	}
	if v.result.Withheld > 0 {
		_, _ = fmt.Fprintf(&buf, "\n%d result(s) withheld: outside this scope", v.result.Withheld)
	}
	if v.cite && strings.TrimSpace(v.result.Rendered) != "" {
		_, _ = fmt.Fprintf(&buf, "\n\n%s", strings.TrimRight(v.result.Rendered, "\n"))
	}
	return strings.TrimRight(buf.String(), "\n")
}

// writeRecallTable renders the ranked rows.
func writeRecallTable(buf *bytes.Buffer, results []recall.Result) {
	tw := tabwriter.NewWriter(buf, 0, 4, 2, ' ', 0)
	// bytes.Buffer never fails a write, so tabwriter's writes through it
	// never do either; errcheck still wants the result handled.
	_, _ = fmt.Fprintf(tw, "RANK\tSCORE\tCORPUS\tTRUST\tSOURCE\n")
	for _, r := range results {
		_, _ = fmt.Fprintf(tw, "%d\t%.3f\t%s\t%s\t%s\n",
			r.Rank, r.Score, r.CorpusID, r.Trust, recallSource(r))
	}
	_ = tw.Flush()
}

// recallSource is the printable identity of one result: its path when it
// has one, otherwise its content-addressed chunk id. Control characters
// are dropped, because a path read out of an index is arbitrary text and
// an escape sequence in it must not become an escape sequence on
// someone's terminal.
func recallSource(r recall.Result) string {
	text := r.Path
	if strings.TrimSpace(text) == "" {
		text = r.ChunkID
	}
	text = stripControl(text)
	if len([]rune(text)) > recallPathWidth {
		return "..." + string([]rune(text)[len([]rune(text))-recallPathWidth:])
	}
	return text
}

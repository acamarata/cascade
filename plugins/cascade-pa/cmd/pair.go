// Purpose: `cascade pa pair`'s cobra surface — the issuance half of the
//
//	bridge pairing flow (W/S-48.T1's contract, R-14.71's §D-21 COMMANDS
//	mount, RPC pa.pair_code). The bot-side half (verifying "/pair
//	<code>" inside Telegram) is plugins/cascade-pa/telegram/pairing.go;
//	this file only prints a fresh code for the operator to send there.
//
// Inputs: an optional positional subject (which bridge instance to pair).
//
//	OMITTING IT IS THE NORMAL CALL: the empty string asks the daemon for the
//	bridge it actually runs. No literal default is ever substituted here —
//	see the note below the imports. Plus the PairClient seam a composition
//	root injects.
//
// Outputs: the plaintext code, its TTL, and (with --json) a structured
//
//	{code, subject, expires_at} object on stdout.
//
// Constraints: R-14.71 — "nothing about issuance is interactive, so no
//
//	prompt is ever raised under CASCADE_NO_INPUT": this command takes no
//	positional prompt to await and no TTY branch to refuse, so it is
//	non-interactive by construction; CASCADE_NO_INPUT=1 changes nothing
//	about its behavior (pair_test.go asserts the two runs are
//	byte-identical). The real PairClient is injected at the binary's
//	composition root (internal/plugins/cascadepa_bridge_client.go's
//	SetPairClient call, mounted on `cascade pa` by
//	cmd/cascade/builtin_plugins.go) and dials the daemon's pa.pair_code
//	RPC: the daemon runs the bridge's poll loop and verifies the code, and
//	it is the only process holding the key the stored digest is computed
//	under. The package default remains unconfiguredPairClient, whose typed
//	cascade.KindUnavailable error is the correct answer for a binary that
//	did not link that root — never a fabricated code.
//
// SPORT: plugins/cascade-pa:cmd:pair (ADD) — P1-E23-W5-S48-T1.

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// An omitted positional subject is passed to the client as the EMPTY
// string, not as a made-up name like "default": the client knows which
// bridge instance is configured (a Telegram subject is a digest of the bot
// token) and resolves the default itself. A literal here would have issued
// codes under a subject no bridge module ever verifies, which looks exactly
// like a working command.

// PairCodeResult is one successful issuance.
type PairCodeResult struct {
	Code      string
	Subject   string
	ExpiresAt time.Time
}

// PairClient is the seam over the daemon's pa.pair_code RPC.
type PairClient interface {
	// IssueCode requests a fresh one-use code for subject.
	IssueCode(ctx context.Context, subject string) (PairCodeResult, error)
}

// errPairClientUnconfigured is unconfiguredPairClient's shared error —
// see this file's doc comment on the composition-root deviation.
var errPairClientUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade pa pair: no daemon client wired into this binary")

type unconfiguredPairClient struct{}

func (unconfiguredPairClient) IssueCode(context.Context, string) (PairCodeResult, error) {
	return PairCodeResult{}, errPairClientUnconfigured
}

var pairClientState struct {
	mu sync.RWMutex
	c  PairClient
}

// SetPairClient injects the real PairClient. Tests call it directly to
// inject a stub; a composition root calls it once at daemon-command
// startup, matching cmd/chat.go's SetClient.
func SetPairClient(c PairClient) {
	pairClientState.mu.Lock()
	pairClientState.c = c
	pairClientState.mu.Unlock()
}

func activePairClient() PairClient {
	pairClientState.mu.RLock()
	defer pairClientState.mu.RUnlock()
	if pairClientState.c == nil {
		return unconfiguredPairClient{}
	}
	return pairClientState.c
}

// pairOptions is NewPairCommand's parsed flag/arg state.
type pairOptions struct {
	subject string
	json    bool
}

// NewPairCommand builds the `pair` cobra command. It is mounted on
// `cascade pa` by cmd/cascade/builtin_plugins.go, and is directly
// Execute()-able in isolation for pair_test.go's sake.
func NewPairCommand() *cobra.Command {
	var opts pairOptions
	c := &cobra.Command{
		Use:   "pair [subject]",
		Short: "Issue a one-use pairing code for a bridge module (Telegram, WhatsApp)",
		Long: "Prints a fresh 8-character pairing code with a 10-minute TTL. Send " +
			"\"/pair <code>\" to the bridge's bot before it expires to bind your " +
			"Q-identity. Fully non-interactive: CASCADE_NO_INPUT has no effect on " +
			"this command, because it never prompts.",
		Args: cobra.MaximumNArgs(1),
		// Matches cmd/cascade's own root-level convention (e.g.
		// daemon_unix_cmd_test.go): the caller renders the returned
		// error, cobra never double-prints "Error: ..." plus a Usage
		// block on top of it. Set here (not only inherited from a
		// mounting root) so this command is directly Execute()-able in
		// isolation, exactly as pair_test.go does.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cc *cobra.Command, args []string) error {
			opts.subject = ""
			if len(args) > 0 {
				opts.subject = args[0]
			}
			return runPair(cc, opts)
		},
	}
	c.Flags().BoolVar(&opts.json, "json", false, "emit a structured JSON result: {code, subject, expires_at}")
	return c
}

// runPair is NewPairCommand's real logic, factored out so tests drive it
// directly with a fake PairClient and a cobra.Command whose output
// stream is a bytes.Buffer.
func runPair(cc *cobra.Command, opts pairOptions) error {
	ctx := cc.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := activePairClient().IssueCode(ctx, opts.subject)
	if err != nil {
		return err
	}
	return writePairResult(cc, res, opts)
}

// writePairResult renders a successful issuance to cc's own output
// stream (never a bare os.Stdout reference, matching cmd/chat.go's
// identical boundary).
func writePairResult(cc *cobra.Command, res PairCodeResult, opts pairOptions) error {
	out := cc.OutOrStdout()
	if opts.json {
		enc := json.NewEncoder(out)
		return enc.Encode(struct {
			Code      string `json:"code"`
			Subject   string `json:"subject"`
			ExpiresAt string `json:"expires_at"`
		}{Code: res.Code, Subject: res.Subject, ExpiresAt: res.ExpiresAt.UTC().Format(time.RFC3339)})
	}
	_, err := fmt.Fprintf(out, "pairing code: %s (subject %s)\nexpires: %s\nsend \"/pair %s\" to the bot before it expires\n",
		res.Code, res.Subject, res.ExpiresAt.UTC().Format(time.RFC3339), res.Code)
	return err
}

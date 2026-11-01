// Purpose: `cascade context facts list|propose [--repo <path>]` and
// `cascade context facts accept <id>`, the operator surface of the
// inferred-fact ledger. list and propose call the daemon's
// context.facts.list and context.facts.propose; accept sends the always
// elevated context.facts.accept through callElevated only, so the daemon
// records an operator accept only after a fresh local attestation.
// Inputs: cobra flags and arguments; contextScopeDeps for the socket.
// Outputs: the daemon's reply through internal/output.Writer.
// Constraints: untagged, so the command tree and the elevation row are the
// same on every OS (accept refuses before dialing on Windows); the verbs need
// a running daemon (the conductor door and the ledger writer live there);
// --repo defaults to the working directory and is sent absolute.
// SPORT: cli/context facts (ADD) -- P1-GEN-02.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The three context.facts verbs.
const (
	contextFactsListMethod    = "context.facts.list"
	contextFactsProposeMethod = "context.facts.propose"
	contextFactsAcceptMethod  = "context.facts.accept"
)

// factsAcceptCallDeps builds the elevated-call collaborators accept signs
// with; tests inject a test custody here.
var factsAcceptCallDeps = productionElevatedCallDeps

// newContextFactsCmd builds the `context facts` group.
func newContextFactsCmd(deps contextScopeDeps) *cobra.Command {
	cmd := &cobra.Command{Use: "facts", Short: "Inferred repository facts: list, propose and accept"}
	cmd.AddCommand(newContextFactsRepoCmd(deps, "list", "List every stored fact version for a repository", factsListCall))
	cmd.AddCommand(newContextFactsRepoCmd(deps, "propose", "Ask the extract model for facts the detectors do not cover", factsProposeCall))
	cmd.AddCommand(newContextFactsAcceptCmd(deps))
	return cmd
}

// factsRepoCall dials one {repo}-only verb on c; each verb's method is a
// named const at its own c.Do call site.
type factsRepoCall func(ctx context.Context, c *client.Client, params map[string]string, out *json.RawMessage) error

// factsListCall dials context.facts.list.
func factsListCall(ctx context.Context, c *client.Client, params map[string]string, out *json.RawMessage) error {
	return c.Do(ctx, contextFactsListMethod, params, out)
}

// factsProposeCall dials context.facts.propose.
func factsProposeCall(ctx context.Context, c *client.Client, params map[string]string, out *json.RawMessage) error {
	return c.Do(ctx, contextFactsProposeMethod, params, out)
}

// newContextFactsRepoCmd builds list or propose, both {repo}-only verbs.
func newContextFactsRepoCmd(deps contextScopeDeps, use, short string, call factsRepoCall) *cobra.Command {
	var repoPath string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			abs, err := factsRepoPath(repoPath)
			if err != nil {
				return err
			}
			c, err := factsClient(cmd.Context(), deps, "context facts "+use)
			if err != nil {
				return err
			}
			var out json.RawMessage
			if err := call(cmd.Context(), c, map[string]string{"repo": abs}, &out); err != nil {
				return err
			}
			return contextScopeOutputWriter(cmd).Result(factsView{Raw: out})
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "repository path (default: the working directory)")
	return cmd
}

// newContextFactsAcceptCmd builds `context facts accept <id>`.
func newContextFactsAcceptCmd(deps contextScopeDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "accept <id>",
		Short:   "Accept the newest proposed version of a fact (elevated)",
		Example: "  cascade context facts accept fact:<repository-id>:layout",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseElevatedOnUnsupportedPlatform("context facts accept"); err != nil {
				return err
			}
			c, err := factsClient(cmd.Context(), deps, "context facts accept")
			if err != nil {
				return err
			}
			var out json.RawMessage
			params := map[string]string{"id": args[0]}
			if err := callElevated(cmd.Context(), c, factsAcceptCallDeps(cmd.ErrOrStderr()), contextFactsAcceptMethod, params, &out); err != nil {
				return err
			}
			return contextScopeOutputWriter(cmd).Result(factsView{Raw: out})
		},
	}
}

// factsRepoPath resolves --repo (or the working directory) to an absolute path.
func factsRepoPath(p string) (string, error) {
	if p == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", cascade.Wrap(cascade.KindUnavailable, err, "cascade context facts: resolve the working directory")
		}
		return wd, nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindInvalidInput, err, "cascade context facts: resolve %q", p)
	}
	return abs, nil
}

// factsClient dials the daemon, refusing when the CLI runs embedded.
func factsClient(ctx context.Context, deps contextScopeDeps, verb string) (*client.Client, error) {
	if st, ok := runtime.DaemonlessStateFrom(ctx); ok && st.Embedded {
		return nil, cascade.Newf(cascade.KindUnavailable,
			"cascade %s needs a running daemon: the fact ledger and the model door live in the daemon process. "+
				"Start it with `cascade daemon start`.", verb)
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextScopeDialTimeout), nil
}

// factsView renders a daemon reply: JSON under --json, indented JSON as text.
type factsView struct {
	Raw json.RawMessage
}

// MarshalJSON emits the daemon's reply unchanged.
func (v factsView) MarshalJSON() ([]byte, error) {
	if len(v.Raw) == 0 {
		return []byte("null"), nil
	}
	return v.Raw, nil
}

// String renders the reply as indented JSON.
func (v factsView) String() string {
	var m any
	if err := json.Unmarshal(v.Raw, &m); err != nil {
		return strings.TrimSpace(string(v.Raw))
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b)
}

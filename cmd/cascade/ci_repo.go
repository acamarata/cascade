// Purpose: repository registration CLI transport and presentation.
// Registration is dispatched through the daemon's elevated RPC surface.
package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/spf13/cobra"
)

type ciRepoRegisterParams struct {
	Path     string   `json:"path"`
	Baseline []string `json:"baseline"`
}
type ciRepoTip struct{ Ref, Commit string }
type ciRepoExcludedTip struct{ Ref, Commit, Reason string }
type ciRepoRegisterReply struct {
	RepoID       string
	RegisteredAt time.Time
	Baseline     []ciRepoTip
	Excluded     []ciRepoExcludedTip
	Detached     bool
}

func (r ciRepoRegisterReply) String() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("%s %s", r.RepoID, r.RegisteredAt.Format(time.RFC3339)))
	for _, t := range r.Baseline {
		lines = append(lines, fmt.Sprintf("baseline %s %s", t.Ref, t.Commit))
	}
	for _, t := range r.Excluded {
		lines = append(lines, fmt.Sprintf("excluded %s %s %s", t.Ref, t.Commit, t.Reason))
	}
	if r.Detached && len(r.Baseline) == 0 {
		lines = append(lines, "baseline empty: HEAD is detached; only --baseline adds branches")
	}
	return strings.Join(lines, "\n")
}

func ciRepoRegister(deps statusDeps, elevated elevatedCallDeps) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		path, _ := cmd.Flags().GetString("path")
		if path == "" {
			path = "."
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return cascade.Wrap(cascade.KindInvalidInput, err, "ci repo register: resolve repository path")
		}
		baseline, _ := cmd.Flags().GetStringSlice("baseline")
		settings, err := resolveStatusSocket(cmd.Context(), deps)
		if err != nil {
			return err
		}
		c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), statusDialTimeout)
		var reply ciRepoRegisterReply
		if err := callElevated(cmd.Context(), c, elevated, "ci.repo_register", ciRepoRegisterParams{abs, baseline}, &reply); err != nil {
			if cascade.HasKind(err, cascade.KindConflict) {
				if raw, ok := client.ErrorData(err); ok {
					var data struct {
						Registration ciRepoRegisterReply `json:"registration"`
					}
					if json.Unmarshal(raw, &data) == nil && data.Registration.RepoID != "" {
						_ = outputWriter(cmd).Result(data.Registration)
					}
				}
			}
			return err
		}
		return outputWriter(cmd).Result(reply)
	}
}

// ciRepoListReply is the registered repository ids, one per line.
type ciRepoListReply []string

func (r ciRepoListReply) String() string { return strings.Join(r, "\n") }

func ciRepoList(cmd *cobra.Command, _ []string) error {
	return outputWriter(cmd).Result(ciRepoListReply{})
}

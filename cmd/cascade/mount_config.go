// Purpose: registers the `config` root mount with its production Deps,
// including AcceptLoosening, which dials the daemon's elevated
// config.accept_loosening through callElevated (P1-SEC-30).
// Inputs: the root command; the process environment at run time.
// Outputs: the mounted `config` tree.
// Constraints: the tree never depends on the environment at mount time;
// the daemon is resolved and dialled only when a loosening is accepted.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/cmd/cascade/config"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
)

var _ = registerRootMount(rootMount{Name: "config", Order: 40, Mount: mountConfigTree})

// configAcceptDialTimeout bounds the daemon dial; the elevation prompt
// itself runs inside callElevated's attest step.
const configAcceptDialTimeout = 5 * time.Second

// mountConfigTree attaches the `config` tree with production Deps.
func mountConfigTree(root *cobra.Command) {
	cmd := config.NewConfigCmd(config.Deps{
		Paths:   lazyPaths{},
		Getenv:  os.Getenv,
		Clock:   runtime.SystemClock{},
		Environ: os.Environ,
		AcceptLoosening: func(ctx context.Context, sha256hex string) error {
			return acceptConfigLoosening(ctx, configAcceptClient, productionElevatedCallDeps(root.ErrOrStderr()), sha256hex)
		},
	})
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// configAcceptClient dials the daemon socket the paths resolve to.
func configAcceptClient() (*client.Client, error) {
	settings, err := daemon.ResolveSettings(nil, lazyPaths{})
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(client.UnixDialer), configAcceptDialTimeout), nil
}

// acceptConfigLoosening submits config.accept_loosening for the config file
// whose sha256 is sha256hex. CASCADE_NO_INPUT and the platform refusals are
// callElevated's; an unreachable daemon is the client's KindUnavailable.
func acceptConfigLoosening(ctx context.Context, dial func() (*client.Client, error), deps elevatedCallDeps, sha256hex string) error {
	c, err := dial()
	if err != nil {
		return err
	}
	params := struct {
		ConfigSHA256 string `json:"config_sha256"`
	}{sha256hex}
	var out json.RawMessage
	return callElevated(ctx, c, deps, runtime.AcceptLooseningMethod, params, &out)
}

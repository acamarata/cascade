// Purpose: `cascade self-update [--check]` — composes internal/update's
//
//	separate steps (Check, Apply) over the production inputs: the
//	install-channel stamp, the receipt under the cascade root, the release
//	source from CASCADE_UPDATE_BASE_URL / CASCADE_UPDATE_DIST, the trust
//	root (embedded key or CASCADE_MINISIGN_PUBKEY), the process egress
//	firewall (opened lazily on the first release request), and the daemon
//	pidfile, hand-off signal and status.get version probe.
//
// Inputs: the --check flag and the global --json flag.
//
// Outputs: a CheckResult (--check) or Result through internal/output;
//
//	exit 0 for none, the channel no-ops, defer and success.
//
// Constraints: a local command only; no RPC method is registered. No
//
//	--channel flag. Every refusal is a typed cascade error.
//
// SPORT: cmd/cascade self-update/ADDED.
package main

import (
	"context"
	"fmt"
	"os"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/update"
)

// selfUpdateDeps builds the update inputs; tests replace it.
var selfUpdateDeps = productionSelfUpdateDeps

// productionUpdateEnv is the channel input of this process.
func productionUpdateEnv() update.Env {
	return update.Env{Stamp: stampedChannel(), Executable: os.Executable, Getenv: os.Getenv}
}

// stampedChannel is the build stamp when buildinfo accepts it as a channel,
// else "" (an unstamped or unknown stamp never decides the channel).
func stampedChannel() string {
	if buildinfo.InstallChannel == "" || buildinfo.ResolvedInstallChannel() != buildinfo.InstallChannel {
		return ""
	}
	return buildinfo.InstallChannel
}

func newSelfUpdateCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update cascade to the latest signed release",
		Long: "Update cascade to the latest signed release: verify the signed checksums with the release key, " +
			"swap the executables (keeping the previous set), hand the running daemon to the new binary, " +
			"and restore the previous set on any failure. brew and OCI installs are managed by their tool; " +
			"node-managed installs defer to the controller. --check reports and changes nothing.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := selfUpdateDeps()
			if err != nil {
				return err
			}
			w := outputWriter(cmd)
			if check {
				res, err := update.Check(cmd.Context(), d)
				if err != nil {
					return err
				}
				return w.Result(checkView{res})
			}
			res, err := update.Apply(cmd.Context(), d)
			if err != nil {
				return err
			}
			return w.Result(resultView{res})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "report what self-update would do; write nothing and signal nothing")
	return cmd
}

// productionSelfUpdateDeps wires the real process inputs.
func productionSelfUpdateDeps() (update.Deps, error) {
	paths, err := runtime.NewDefaultPathProvider()
	if err != nil {
		return update.Deps{}, err
	}
	src, err := update.SourceFromEnv(os.Getenv)
	if err != nil {
		return update.Deps{}, err
	}
	var fetch update.Fetcher = update.HTTPFetcher{Interceptor: update.ProcessInterceptor(), BaseURL: src.BaseURL}
	if src.Dist != "" {
		fetch = update.DirFetcher{Dir: src.Dist}
	}
	clock := runtime.NewSystemClock()
	return update.Deps{
		Env:       productionUpdateEnv(),
		Version:   buildinfo.Version,
		Source:    src,
		Fetcher:   fetch,
		Platform:  update.Platform{OS: goruntime.GOOS, Arch: goruntime.GOARCH},
		TrustRoot: func() (minisign.PublicKey, error) { return update.TrustRoot(os.Getenv) },
		DataDir:   paths.DataDir(),
		Clock:     clock,
		Daemon: update.DrainDeps{
			Status:  update.DaemonStatus(daemon.PIDFilePath(paths), clock),
			Signal:  update.ProductionSignal(),
			Version: daemonVersion(paths),
			Clock:   clock,
		},
	}, nil
}

// daemonVersion asks the running daemon's status.get for its version.
func daemonVersion(paths runtime.PathProvider) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		deps := statusDeps{Paths: paths, Getenv: os.Getenv, Environ: os.Environ, DialContext: client.UnixDialer}
		settings, err := resolveStatusSocket(ctx, deps)
		if err != nil {
			return "", err
		}
		resp, err := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), statusDialTimeout).Status(ctx)
		return resp.Version, err
	}
}

// checkView renders a CheckResult; its JSON is the CheckResult's.
type checkView struct{ update.CheckResult }

func (v checkView) String() string {
	if v.Action == update.ActionUpdate {
		return fmt.Sprintf("update available: %s -> %s (%s); run `cascade self-update`", v.Current.Version, v.Latest.Version, v.Latest.ReleaseTag)
	}
	return describeNoChange(v.CheckResult)
}

// resultView renders a Result; its JSON is the Result's.
type resultView struct{ update.Result }

func (v resultView) String() string {
	if !v.Updated {
		return describeNoChange(v.CheckResult)
	}
	s := fmt.Sprintf("updated cascade %s -> %s (%s); the previous version is kept in %s",
		v.Current.Version, v.Latest.Version, v.Latest.ReleaseTag, v.PreviousDir)
	if v.DaemonSignalled {
		s += "; the daemon relaunched on " + v.Latest.Version
	}
	return s
}

// describeNoChange is the line for every outcome that changes nothing.
func describeNoChange(r update.CheckResult) string {
	switch r.Channel {
	case update.ChannelNodeManaged:
		return "managed by the controller (node upgrade)"
	case update.ChannelBrew:
		return "managed by brew; run brew upgrade cascade"
	case update.ChannelOCI:
		return "managed by oci; pull the image"
	case update.ChannelManual:
		return "installed manually (no install receipt); reinstall with install.sh to use self-update"
	case update.ChannelScript:
	}
	return fmt.Sprintf("cascade %s is up to date (latest %s)", r.Current.Version, r.Latest.ReleaseTag)
}

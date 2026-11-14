// Purpose: the CLI half of billing-tier promotion for one provider:
//
//	`provider test <name> --promote paid|--demote` and the evidence
//	forget that `provider remove` sends. The local reachability test runs
//	first and is advisory; the daemon repeats its own authenticated probe.
//
// Inputs: providerDeps (data directory), the daemon socket seam and the
//
//	elevated-call seams below.
//
// Outputs: the domain, its new tier and each probed provider.
// Constraints: the promote verb is dialed only through callElevated; a
//
//	failed local test sends nothing; no credential is ever read or printed
//	here; a forget that cannot reach the daemon never fails the remove.
//
// SPORT: cmd/cascade provider promote.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The daemon verbs this file dials; the daemon registers them.
const (
	domainPromoteMethod = "fleet.domains.promote"
	domainDemoteMethod  = "fleet.domains.demote"
	domainForgetMethod  = "fleet.domains.forget"

	tierPaid     = "paid"
	tierDiscover = "discover"

	providerDaemonTimeout = 5 * time.Second

	// promoteProbeBudget is the daemon's bound on one provider probe; the
	// daemon runs the probes of a domain one after another.
	promoteProbeBudget = 30 * time.Second
	// promoteTimeoutMargin covers the attestation round trip, the key
	// derivation and the promotion transaction around the probes.
	promoteTimeoutMargin = 30 * time.Second
)

// promoteRoundTripTimeout bounds one elevated promote: a full probe budget
// for each provider the daemon may probe in turn, plus the margin. At least
// one probe is assumed, so the bound never falls below one budget.
func promoteRoundTripTimeout(probes int) time.Duration {
	return time.Duration(max(probes, 1))*promoteProbeBudget + promoteTimeoutMargin
}

// registryProbeCount is the number of providers in reg: an upper bound on
// the sequential network probes of one promote (agent lanes probe locally).
func registryProbeCount(ctx context.Context, reg *registry.Registry) (int, error) {
	recs, err := reg.ListProviders(ctx)
	return len(recs), err
}

// providerDaemonDial dials the daemon socket; tests substitute it.
var providerDaemonDial = client.UnixDialer

// providerPromoteCallDeps builds the elevated-call collaborators the
// promote verb signs with; tests inject a test custody here.
var providerPromoteCallDeps = productionElevatedCallDeps

// domainChangeParams is the only request shape of the three verbs: a
// target (domain or provider) and a tier.
type domainChangeParams struct {
	Domain   string `json:"domain,omitempty"`
	Provider string `json:"provider,omitempty"`
	Tier     string `json:"tier,omitempty"`
}

// domainChangeReply is what the daemon answers to promote and demote.
type domainChangeReply struct {
	Domain     string   `json:"domain"`
	Tier       string   `json:"tier"`
	Providers  []string `json:"providers,omitempty"`
	Evidence   int      `json:"evidence,omitempty"`
	Pruned     int      `json:"pruned,omitempty"`
	PruneError string   `json:"prune_error,omitempty"`
}

// String names the domain, its new tier and each probed provider.
func (r domainChangeReply) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "domain %s: tier=%s", r.Domain, r.Tier)
	for _, p := range r.Providers {
		fmt.Fprintf(&b, "\n  provider %s: probe passed", p)
	}
	if r.PruneError != "" {
		fmt.Fprintf(&b, "\n  prune failed: %s", r.PruneError)
	}
	return b.String()
}

// forgetReply is the daemon's answer to fleet.domains.forget.
type forgetReply struct {
	Provider  string `json:"provider"`
	Forgotten int    `json:"forgotten"`
}

// providerDaemonClient builds a client for the daemon socket under deps
// whose round trips are bounded by timeout.
func providerDaemonClient(deps providerDeps, timeout time.Duration) (*client.Client, error) {
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	dial := func(ctx context.Context, socket string) (net.Conn, error) { return providerDaemonDial(ctx, socket) }
	return client.New(settings.SocketPath, client.DialFunc(dial), timeout), nil
}

// providerTierFlags are the promotion flags of `provider test`.
type providerTierFlags struct {
	promote string
	demote  bool
}

// validate refuses a tier other than paid and the two flags together.
func (f providerTierFlags) validate() error {
	switch {
	case f.promote != "" && f.demote:
		return cascade.New(cascade.KindInvalidInput, "provider test: use --promote or --demote, not both")
	case f.promote != "" && f.promote != tierPaid:
		return cascade.Newf(cascade.KindInvalidInput, "provider test: --promote takes %q, got %q", tierPaid, f.promote)
	}
	return nil
}

// active reports whether a tier change was asked for.
func (f providerTierFlags) active() bool { return f.promote != "" || f.demote }

// changeProviderTier sends the promote (elevated) or demote verb for the
// provider's quota domain and prints the daemon's answer. probes bounds the
// sequential provider probes the daemon may run for the promote.
func changeProviderTier(cmd *cobra.Command, deps providerDeps, name string, f providerTierFlags, probes int) error {
	timeout := providerDaemonTimeout
	if !f.demote {
		timeout = promoteRoundTripTimeout(probes)
	}
	c, err := providerDaemonClient(deps, timeout)
	if err != nil {
		return err
	}
	var reply domainChangeReply
	if f.demote {
		err = c.Do(cmd.Context(), domainDemoteMethod, domainChangeParams{Provider: name, Tier: tierDiscover}, &reply)
	} else {
		err = callElevated(cmd.Context(), c, providerPromoteCallDeps(stderrOf(cmd)), domainPromoteMethod,
			domainChangeParams{Provider: name, Tier: tierPaid}, &reply)
	}
	if err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(reply)
}

// stderrOf is the command's error stream.
func stderrOf(cmd *cobra.Command) io.Writer { return cmd.ErrOrStderr() }

// forgetProviderEvidence asks the daemon to delete the provider's billing
// evidence. It returns a note for the user when that could not be done; the
// remove itself never fails because of it.
func forgetProviderEvidence(ctx context.Context, deps providerDeps, name string) string {
	c, err := providerDaemonClient(deps, providerDaemonTimeout)
	if err == nil {
		var reply forgetReply
		if err = c.Do(ctx, domainForgetMethod, domainChangeParams{Provider: name}, &reply); err == nil {
			return ""
		}
	}
	if cascade.HasKind(err, cascade.KindUnavailable) {
		return fmt.Sprintf("the daemon is not running: the billing evidence of %q is invalid and is pruned at the next reconcile", name)
	}
	return fmt.Sprintf("billing evidence of %q was not forgotten (%v): it is invalid and is pruned at the next reconcile", name, err)
}

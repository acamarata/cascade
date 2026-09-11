// Purpose: `cascade provider test|remove|usage` -- the second half of
//   P1-E10-W3-S21-T2's five subcommands, split out of
//   provider_health_cmd.go (list/health/storage wiring) to stay under the
//   repo's 300-line-per-file gate. Shares openProviderStorage,
//   providerVaultBroker, and the providerListRow/providerListResult
//   rendering types declared there.
// Inputs/Outputs/Constraints: identical to provider_health_cmd.go's own
//   header -- see that file for the full contradiction note on why these
//   commands operate on local storage rather than daemon RPC.
// SPORT: provider · J · S-21 · T-2 · provider CLI commands and doctor
//   check (P1-E10-W3-S21-T2).

package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/providers/health"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func newProviderTestCmd(deps providerDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "test <name>",
		Short:       "Probe one provider's reachability and report its health",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderTest(cmd, deps, args[0])
		},
	}
}

// providerTestResult is `cascade provider test`'s --json/table payload.
type providerTestResult struct {
	Name     string `json:"name"`
	Success  bool   `json:"success"`
	Endpoint string `json:"endpoint,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

func (r providerTestResult) String() string {
	if r.Success {
		return fmt.Sprintf("%s: healthy (status=%d)", r.Name, r.Status)
	}
	return fmt.Sprintf("%s: unhealthy %s", r.Name, r.Detail)
}

func runProviderTest(cmd *cobra.Command, deps providerDeps, name string) error {
	store, err := openProviderStorage(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	ok, err := store.Health.RecoverProbe(cmd.Context(), name)
	if err != nil {
		return err
	}
	result, _ := store.Health.LastProbeResult(name)
	res := providerTestResult{Name: name, Success: ok, Endpoint: result.Endpoint, Status: result.Status, Detail: result.TransportErr}
	if err := vaultOutputWriter(cmd).Result(res); err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindUnavailable, "provider test: %q is not reachable", name)
	}
	return nil
}

func newProviderRemoveCmd(deps providerDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "remove <name>",
		Short:       "Remove a provider and revoke its vault entry",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderRemove(cmd, deps, args[0])
		},
	}
}

// providerRemoveResult reports whether removal did anything (R-14.95:
// idempotent -- a second removal of an already-absent provider is a
// no-op, never an error).
type providerRemoveResult struct {
	Name  string `json:"name"`
	Delta string `json:"delta"` // "removed" or "none"
}

func (r providerRemoveResult) String() string { return fmt.Sprintf("%s: delta=%s", r.Name, r.Delta) }

func runProviderRemove(cmd *cobra.Command, deps providerDeps, name string) error {
	store, err := openProviderStorage(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	rec, err := store.Registry.GetProvider(cmd.Context(), name)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return vaultOutputWriter(cmd).Result(providerRemoveResult{Name: name, Delta: "none"})
	}
	if err != nil {
		return err
	}
	if err := revokeProviderVaultEntry(cmd.Context(), deps, rec.AuthRef.String()); err != nil {
		return err
	}
	if err := store.Registry.DeleteProvider(cmd.Context(), name); err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(providerRemoveResult{Name: name, Delta: "removed"})
}

// revokeProviderVaultEntry deletes authRef from the vault, tolerating an
// already-absent entry (NotFound) so remove stays idempotent end to end.
func revokeProviderVaultEntry(ctx context.Context, deps providerDeps, authRef string) error {
	if authRef == "" {
		return nil
	}
	broker, err := providerVaultBroker(deps)
	if err != nil {
		return err
	}
	if err := broker.Delete(ctx, authRef); err != nil && !cascade.HasKind(err, cascade.KindNotFound) {
		return err
	}
	return nil
}

func newProviderUsageCmd(deps providerDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "usage",
		Short:       "Show aggregate per-provider usage",
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProviderUsage(cmd, deps)
		},
	}
}

// providerUsageResult is `cascade provider usage`'s --json/table payload:
// aggregate rows only (06 §5.16 -- personal tables are never exposed).
type providerUsageResult struct {
	Rows []provider.UsageSummary `json:"rows"`
}

func (r providerUsageResult) String() string {
	if len(r.Rows) == 0 {
		return "no usage recorded"
	}
	lines := make([]string, len(r.Rows))
	for i, row := range r.Rows {
		lines[i] = fmt.Sprintf("%-20s lane=%-10s tokens_in=%d tokens_out=%d cost_micro_usd=%d",
			row.ProviderName, row.LaneName, row.TokensIn, row.TokensOut, row.CostMicroUSD)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func runProviderUsage(cmd *cobra.Command, deps providerDeps) error {
	store, err := openProviderStorage(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	reader := usage.NewReader(store.Usage)
	rows, err := reader.QueryUsage(cmd.Context(), provider.UsageFilter{})
	if err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(providerUsageResult{Rows: rows})
}

func newProviderHealthCmd(deps providerDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "health",
		Short:       "Show aggregate health of every registered provider",
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProviderHealth(cmd, deps)
		},
	}
}

func runProviderHealth(cmd *cobra.Command, deps providerDeps) error {
	store, err := openProviderStorage(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	recs, err := store.Registry.ListProviders(cmd.Context())
	if err != nil {
		return err
	}
	rows := make([]providerListRow, len(recs))
	unhealthy := 0
	for i, rec := range recs {
		rows[i] = providerListRow{Name: rec.Name, Driver: string(rec.Driver), Tier: string(rec.Tier),
			Health: string(rec.HealthStatus), Capabilities: rec.Capabilities}
		if rec.HealthStatus != registry.HealthHealthy {
			unhealthy++
		}
	}
	if err := vaultOutputWriter(cmd).Result(providerListResult{Providers: rows}); err != nil {
		return err
	}
	if unhealthy > 0 {
		return cascade.Newf(cascade.KindUnavailable, "provider health: %d provider(s) not healthy", unhealthy)
	}
	return nil
}

// providerDoctorTimeout bounds every doctor-driven reachability probe.
const providerDoctorTimeout = 10 * time.Second

// httpDoer is httpReachabilityProber's transport seam, deliberately typed
// without net/http in its signature: providerDeps.HealthHTTPDoer lets a
// test substitute a fake with no net/http import at all, so
// TestProviderTest_* stays outside the no-network-unit-lane gate (a
// _test.go file importing net/http is flagged even when, as here, it
// never opens a real socket -- internal/build's gate is import-based).
// realHTTPDoer (below) is the only implementation that imports net/http;
// it is never referenced from a _test.go file.
type httpDoer interface {
	// Get performs a GET against url and returns its status code, or an
	// error if the request could not be sent/completed at all.
	Get(ctx context.Context, url string) (status int, err error)
}

// realHTTPDoer is the production httpDoer: a real outbound GET through
// *http.Client. This is the one place provider_health_cmd.go's
// reachability probe touches net/http.
type realHTTPDoer struct{ client *http.Client }

func (d realHTTPDoer) Get(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

type httpReachabilityProber struct {
	doer httpDoer
}

// Probe implements health.Prober. This is a REAL, network-capable probe
// scoped to reachability rather than a full per-vendor authenticated
// 1-token verify: it issues a GET to the provider's BaseURL and treats
// any response with status < 500 as reachable. A true per-vendor verify
// call (matching each driver's own auth header shape) needs a live
// provider.ModelProvider instance per driver kind, which is out of this
// ticket's files_scope (providers/* driver packages) -- see this
// ticket's journal for the full scope note.
func (p httpReachabilityProber) Probe(ctx context.Context, rec registry.ProviderRecord) (health.ProbeResult, error) {
	if rec.BaseURL == "" {
		return health.ProbeResult{Success: false, TransportErr: "no base_url configured"}, nil
	}
	status, err := p.doer.Get(ctx, rec.BaseURL)
	if err != nil {
		return health.ProbeResult{Endpoint: rec.BaseURL, Success: false, TransportErr: err.Error()}, nil
	}
	return health.ProbeResult{Endpoint: rec.BaseURL, Status: status, Success: status < 500}, nil
}

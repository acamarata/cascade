// Purpose: the provider_health doctor check (P1-E10-W3-S21-T2,
//   R-14.35): verifies every registered provider is reachable and
//   surfaces a distinct, visible outcome when reachability cannot be
//   determined at all -- StatusWarn, never StatusOK, so an unverifiable
//   subject never reads as a healthy one (Art.1). --fix RE-PROBES
//   unreachable/unknown providers and reports the result; it never
//   evicts or deletes a provider record (eviction stays manual via
//   `cascade provider remove`).
// Inputs: a ProviderHealthSource, the narrow interface this package
//   declares so internal/doctor never imports cmd/cascade (the real
//   source, a registry+health.Manager pair, is adapted in
//   cmd/cascade/doctor_mounts.go, this check's sole mount point).
// Outputs: CheckResult/FixResult per the Check interface.
// Constraints: a nil ProviderHealthSource, or a source that errors
//   listing providers, is StatusError ("cannot determine"), never a
//   silent StatusOK. No provider name or credential is ever echoed
//   beyond the bare name each row already carries in the registry.
// SPORT: provider · J · S-21 · T-2 · provider CLI commands and doctor
//   check (P1-E10-W3-S21-T2).

package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ProviderHealthRow is one provider's name and last-known health status
// ("healthy", "degraded", "dead", or "" for not-yet-probed/unknown --
// mirroring internal/providers/registry.HealthStatus's closed vocabulary
// without importing that package here).
type ProviderHealthRow struct {
	Name   string
	Status string
}

// ProviderHealthSource is the narrow surface checks_provider.go needs:
// list every provider's current health, and re-probe one by name for
// --fix. The production implementation (cmd/cascade/doctor_mounts.go)
// opens the S-20.T2 registry and the S-20.T3 health.Manager this ticket
// also wires into `cascade provider test/health`.
type ProviderHealthSource interface {
	// ListProviderHealth returns every registered provider's current
	// health row, or an error if the source could not be reached at all
	// (fail closed: the check reports "cannot determine", not "healthy").
	ListProviderHealth(ctx context.Context) ([]ProviderHealthRow, error)
	// RecoverProviderHealth re-probes name and reports whether it is now
	// reachable. Never mutates the registry beyond recording the probe
	// outcome -- it does not evict or delete.
	RecoverProviderHealth(ctx context.Context, name string) (bool, error)
}

// providerHealthyStatus is registry.HealthHealthy's string value,
// duplicated here (rather than imported) so this package stays free of
// internal/providers/registry per its own no-cmd/cascade-import
// constraint's spirit -- ProviderHealthRow.Status is a plain string by
// design.
const providerHealthyStatus = "healthy"

// providerHealthCheck implements Check for the provider_health check.
type providerHealthCheck struct {
	source ProviderHealthSource
}

// NewProviderHealthCheck returns the provider_health Check over source.
// A nil source is accepted (Run/Fix report StatusError/an error rather
// than panicking), matching internal/doctor's other checks' nil-safety
// convention for an unwired composition-root dependency.
func NewProviderHealthCheck(source ProviderHealthSource) Check {
	return &providerHealthCheck{source: source}
}

func (c *providerHealthCheck) Name() string { return "provider_health" }

func (c *providerHealthCheck) Describe() string {
	return "Verifies every registered model provider is reachable"
}

func (c *providerHealthCheck) Metadata() CheckMeta {
	return CheckMeta{FirstRun: true, Fixable: true}
}

// Run lists every provider's current health and classifies the worst
// outcome found. A provider whose Status is "" (never probed) is
// reported distinctly from a healthy one -- StatusWarn, not StatusOK --
// because "cannot determine" and "healthy" are never the same outcome.
func (c *providerHealthCheck) Run(ctx context.Context) (CheckResult, error) {
	if c.source == nil {
		return CheckResult{Status: StatusError,
			Message: "provider_health: no provider health source configured; cannot determine health"}, nil
	}
	rows, err := c.source.ListProviderHealth(ctx)
	if err != nil {
		return CheckResult{Status: StatusError,
			Message: "provider_health: could not list registered providers", Detail: err.Error()}, nil
	}
	if len(rows) == 0 {
		return CheckResult{Status: StatusOK, Message: "provider_health: no providers registered"}, nil
	}
	return classifyProviderHealth(rows), nil
}

// classifyProviderHealth turns rows into one CheckResult: StatusOK only
// when every row is "healthy"; StatusWarn when the worst row is unknown
// ("", never probed -- an unverifiable subject, distinct from healthy);
// StatusError when at least one provider is degraded or dead.
func classifyProviderHealth(rows []ProviderHealthRow) CheckResult {
	var unhealthy, unknown []string
	for _, r := range rows {
		switch r.Status {
		case providerHealthyStatus:
		case "":
			unknown = append(unknown, r.Name)
		default:
			unhealthy = append(unhealthy, r.Name)
		}
	}
	sort.Strings(unhealthy)
	sort.Strings(unknown)

	if len(unhealthy) > 0 {
		return CheckResult{Status: StatusError,
			Message:     fmt.Sprintf("provider_health: %d provider(s) unreachable: %s", len(unhealthy), strings.Join(unhealthy, ", ")),
			Remediation: "run `cascade provider test <name>` for detail, or `cascade doctor --fix` to re-probe"}
	}
	if len(unknown) > 0 {
		return CheckResult{Status: StatusWarn,
			Message:     fmt.Sprintf("provider_health: %d provider(s) never probed (health cannot be determined)", len(unknown)),
			Detail:      strings.Join(unknown, ", "),
			Remediation: "run `cascade doctor --fix` to probe them now"}
	}
	return CheckResult{Status: StatusOK, Message: fmt.Sprintf("provider_health: %d provider(s) healthy", len(rows))}
}

// Fix re-probes every provider Run found unhealthy or unverifiable
// (R-14.35: --fix re-probes and reports; it never evicts or deletes a
// provider record -- eviction stays manual via `cascade provider
// remove`). A second --fix on an already-healthy system applies nothing
// and reports Applied=false, matching the idempotency contract every
// Check's Fix follows.
func (c *providerHealthCheck) Fix(ctx context.Context) (FixResult, error) {
	if c.source == nil {
		return FixResult{}, ErrCheckNotFixable
	}
	rows, err := c.source.ListProviderHealth(ctx)
	if err != nil {
		return FixResult{}, err
	}
	var reprobed, stillDown []string
	for _, r := range rows {
		if r.Status == providerHealthyStatus {
			continue
		}
		ok, perr := c.source.RecoverProviderHealth(ctx, r.Name)
		if perr != nil {
			stillDown = append(stillDown, r.Name)
			continue
		}
		reprobed = append(reprobed, r.Name)
		if !ok {
			stillDown = append(stillDown, r.Name)
		}
	}
	if len(reprobed) == 0 {
		return FixResult{Applied: false}, nil
	}
	sort.Strings(reprobed)
	sort.Strings(stillDown)
	delta := fmt.Sprintf("re-probed %d provider(s): %s", len(reprobed), strings.Join(reprobed, ", "))
	if len(stillDown) > 0 {
		delta += fmt.Sprintf(" (still unreachable: %s)", strings.Join(stillDown, ", "))
	}
	return FixResult{Applied: true, Delta: delta}, nil
}

package topologycheck

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// topologyCheck is the shared shell of the four checks: name, text and a
// scan returning one line per violation.
type topologyCheck struct {
	name        string
	describe    string
	remediation string
	scan        func(ctx context.Context) ([]string, error)
}

func (c *topologyCheck) Name() string               { return c.name }
func (c *topologyCheck) Describe() string           { return c.describe }
func (c *topologyCheck) Group() string              { return doctor.GroupProviders }
func (c *topologyCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }

// Fix never changes a topology row: the checks are read-only.
func (c *topologyCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run scans the rows. A read error or an unwired reader is StatusError,
// never StatusOK.
func (c *topologyCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	found, err := c.scan(ctx)
	if err != nil {
		return doctor.CheckResult{
			Status:  doctor.StatusError,
			Message: fmt.Sprintf("%s: cannot read topology rows", c.name),
			Detail:  err.Error(),
		}, nil
	}
	if len(found) == 0 {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: c.describe + ": none"}, nil
	}
	sort.Strings(found)
	return doctor.CheckResult{
		Status:      doctor.StatusWarn,
		Message:     fmt.Sprintf("%s: %d violation(s)", c.name, len(found)),
		Detail:      strings.Join(found, "\n"),
		Remediation: c.remediation,
	}, nil
}

// errNilReader is the scan error for an unwired reader.
var errNilReader = cascade.New(cascade.KindUnavailable, "doctor: topology check reader is not wired")

// NewFreeDomainAbovePublicCheck reports reservations, live or terminal,
// whose sensitivity is above public on a free-tier quota domain.
func NewFreeDomainAbovePublicCheck(topo TopologyReader, res ReservationReader) doctor.Check {
	return &topologyCheck{
		name:        "free_domain_above_public",
		describe:    "Reservations above public sensitivity on a free-tier quota domain",
		remediation: "route non-public work to a paid quota domain",
		scan:        tierAbovePublicScan(topo, res, topology.BillingTierFree),
	}
}

// NewDiscoverDomainAbovePublicCheck reports reservations, live or
// terminal, whose sensitivity is above public on a quota domain whose
// billing tier is still discover.
func NewDiscoverDomainAbovePublicCheck(topo TopologyReader, res ReservationReader) doctor.Check {
	return &topologyCheck{
		name:        "discover_domain_above_public",
		describe:    "Reservations above public sensitivity on an undiscovered-tier quota domain",
		remediation: "set the quota domain's billing tier before routing non-public work to it",
		scan:        tierAbovePublicScan(topo, res, topology.BillingTierDiscover),
	}
}

// NewCredentialWithoutDomainCheck reports credentials whose quota-domain
// reference is empty or names no quota-domain row.
func NewCredentialWithoutDomainCheck(topo TopologyReader) doctor.Check {
	return &topologyCheck{
		name:        "credential_without_domain",
		describe:    "Credentials with an empty or missing quota domain",
		remediation: "re-run provider reconcile or remove the orphaned credential",
		scan:        func(ctx context.Context) ([]string, error) { return credentialWithoutDomain(ctx, topo) },
	}
}

// NewDomainWithoutCredentialCheck reports api_project and shared_pool
// quota domains no credential references. A subscription_window domain is
// never reported.
func NewDomainWithoutCredentialCheck(topo TopologyReader) doctor.Check {
	return &topologyCheck{
		name:        "domain_without_credential",
		describe:    "Quota domains with no credential",
		remediation: "add a credential for the domain or remove it",
		scan:        func(ctx context.Context) ([]string, error) { return domainWithoutCredential(ctx, topo) },
	}
}

// credentialWithoutDomain lists every credential whose quota_domain_ref is
// empty or dangling, naming the credential id and secret_ref only.
func credentialWithoutDomain(ctx context.Context, topo TopologyReader) ([]string, error) {
	if topo == nil {
		return nil, errNilReader
	}
	domains, err := domainSet(ctx, topo)
	if err != nil {
		return nil, err
	}
	creds, err := topo.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range creds {
		if c.QuotaDomainRef == "" {
			out = append(out, fmt.Sprintf("credential %s (secret_ref %s): empty quota_domain_ref",
				c.ID, c.SecretRef.String()))
			continue
		}
		if _, ok := domains[c.QuotaDomainRef]; !ok {
			out = append(out, fmt.Sprintf("credential %s (secret_ref %s): quota domain %s not found",
				c.ID, c.SecretRef.String(), c.QuotaDomainRef))
		}
	}
	return out, nil
}

// domainWithoutCredential lists every api_project or shared_pool domain no
// credential references.
func domainWithoutCredential(ctx context.Context, topo TopologyReader) ([]string, error) {
	if topo == nil {
		return nil, errNilReader
	}
	domains, err := topo.ListQuotaDomains(ctx)
	if err != nil {
		return nil, err
	}
	creds, err := topo.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	used := make(map[topology.DomainID]bool, len(creds))
	for _, c := range creds {
		used[c.QuotaDomainRef] = true
	}
	var out []string
	for _, d := range domains {
		if used[d.ID] || !needsCredential(d.Kind) {
			continue
		}
		out = append(out, fmt.Sprintf("quota domain %s (%s): no credential", d.ID, d.Kind))
	}
	return out, nil
}

// needsCredential reports whether a domain of kind k must be referenced by
// a credential. A subscription window is never reported; an unknown kind
// is (fail closed).
func needsCredential(k topology.QuotaDomainKind) bool {
	switch k {
	case topology.QuotaDomainSubscriptionWindow:
		return false
	case topology.QuotaDomainAPIProject, topology.QuotaDomainSharedPool:
		return true
	}
	return true
}

// domainSet indexes every quota domain by id.
func domainSet(ctx context.Context, topo TopologyReader) (map[topology.DomainID]topology.QuotaDomain, error) {
	domains, err := topo.ListQuotaDomains(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[topology.DomainID]topology.QuotaDomain, len(domains))
	for _, d := range domains {
		out[d.ID] = d
	}
	return out, nil
}

// abovePublic reports whether a recorded sensitivity is above public. An
// empty or unparsable value counts as above public (fail closed).
func abovePublic(s string) bool {
	t, err := provider.ParseSensitivityTier(s)
	if err != nil {
		return true
	}
	return t.MoreRestrictiveThan(provider.SensitivityPublic)
}

// tierAbovePublicScan returns a scan reporting every reservation row, live
// or terminal, whose sensitivity is above public on a domain of tier.
func tierAbovePublicScan(topo TopologyReader, res ReservationReader, tier topology.BillingTier) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		if topo == nil || res == nil {
			return nil, errNilReader
		}
		domains, err := domainSet(ctx, topo)
		if err != nil {
			return nil, err
		}
		rows, err := res.ListReservations(ctx)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, r := range rows {
			d, ok := domains[topology.DomainID(r.DomainID)]
			if !ok || d.BillingTier != tier || !abovePublic(r.Sensitivity) {
				continue
			}
			out = append(out, fmt.Sprintf("reservation %s (%s): sensitivity %q on %s quota domain %s",
				r.ID, r.State, r.Sensitivity, tier, d.ID))
		}
		return out, nil
	}
}

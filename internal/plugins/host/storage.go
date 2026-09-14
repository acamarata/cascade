// Purpose: CheckStorage, the storage-domain check every host_storage_*
//
//	call transits before the host reads or writes the plugin's namespaced
//	storage area.
//
// Inputs: the storage domain a plugin asked to access.
// Outputs: nil for a declared same-domain access, or a domain outside the
//
//	plugin's own set when it holds the explicit cross-domain capability;
//	a denied, audited error otherwise.
//
// Constraints: fail closed — an empty domain, a nil declared-domain set,
//
//	and a domain outside that set with CrossDomain unset all deny.
//
// SPORT: internal/plugins/host storage-domain-check (ADD) — P1-E15-W4-S31-T4.

package host

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// storageCallType names this check in audit entries and denial messages.
const storageCallType = "storage"

// CheckStorage reports whether domain is reachable under e's grants:
// allowed when domain is in the plugin's own declared StorageDomains
// (same-domain), or when it is not but the plugin holds the explicit
// CrossDomain capability. An empty domain and a domain outside the
// declared set without CrossDomain both deny (fail closed).
func (e *HostBoundaryEnforcer) CheckStorage(ctx context.Context, domain string) error {
	if domain == "" {
		return e.Deny(ctx, storageCallType, cascade.New(cascade.KindInvalidInput, "host_storage: empty domain"))
	}
	if e.grants.ownsDomain(domain) {
		return nil
	}
	if e.grants.CrossDomain {
		return nil
	}
	return e.Deny(ctx, storageCallType,
		cascade.Newf(cascade.KindCapabilityDenied, "host_storage: domain %q is outside the plugin's declared domains and it holds no cross-domain capability", domain))
}

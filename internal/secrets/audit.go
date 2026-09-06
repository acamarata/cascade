// Purpose: the read-only secrets audit report behind `cascade vault
//
//	audit`. It answers "what does this vault hold, and what needs
//	attention" without answering "what is in it": no path through this
//	file can reach a stored value.
//
// Inputs: an AuditDeps the CLI fills with the broker, the quarantine
//
//	ledger and a clock. Every source is injected; nothing here opens a
//	store of its own.
//
// Outputs: an AuditReport. Names, a backend label, counts and expiry
//
//	timestamps. The OAuth records it decodes hold vault key NAMES only.
//
// Constraints: read-only and idempotent - two runs over an unchanged
//
//	vault return the same report. An unreadable source is an error, never
//	an empty section: an empty entry list is the assertion "this vault
//	holds nothing", and this surface must not make that claim untruthfully.
//
// SPORT: SECRETS_AUDIT_REPORT: ADD (internal/secrets.AuditReport, Build).

package secrets

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// AuditDeps carries the sources the report reads.
type AuditDeps struct {
	// Broker is the vault. Required.
	Broker *Broker
	// Quarantine is the detector's ledger. Required.
	Quarantine *QuarantineStore
	// Now supplies the report timestamp and the expiry comparison.
	// Required: this file never reaches for the system clock.
	Now func() time.Time
}

// AuditEntry is one stored entry, described without its value.
type AuditEntry struct {
	// Name is the vault entry name.
	Name string `json:"name"`
	// Kind classifies the entry from its name alone: "oauth-record",
	// "oauth-token" or "secret". It is derived, never stored.
	Kind string `json:"kind"`
}

// AuditGrant is one stored OAuth grant's expiry state.
type AuditGrant struct {
	// Provider and Account identify the grant. Neither is a secret.
	Provider string `json:"provider"`
	Account  string `json:"account"`
	// ExpiresAt is the declared expiry; the zero value means the
	// authorization server declared none.
	ExpiresAt time.Time `json:"expires_at"`
	// Expired and ExpiringSoon are the two states worth acting on.
	Expired      bool `json:"expired"`
	ExpiringSoon bool `json:"expiring_soon"`
}

// AuditReport is the full read-only view.
type AuditReport struct {
	// Backend is the custody backend that answered.
	Backend string `json:"backend"`
	// Entries are the stored names, sorted.
	Entries []AuditEntry `json:"entries"`
	// Grants are the stored OAuth grants, sorted by provider/account.
	Grants []AuditGrant `json:"grants"`
	// QuarantineDepth is the number of detections awaiting a decision.
	QuarantineDepth int `json:"quarantine_depth"`
	// GeneratedAt is when the report was built.
	GeneratedAt time.Time `json:"generated_at"`
	// PerEntryMetadataAvailable reports whether provider, last-used and
	// never-resolved metadata could be sourced. It is false in this
	// build: no metadata sidecar exists, and reporting a fabricated
	// last-used time would be worse than reporting none.
	PerEntryMetadataAvailable bool `json:"per_entry_metadata_available"`
}

// BuildAuditReport reads every source and assembles the report.
func BuildAuditReport(ctx context.Context, deps AuditDeps) (AuditReport, error) {
	switch {
	case deps.Broker == nil:
		return AuditReport{}, cascade.New(cascade.KindInvalidInput, "secrets: the audit report needs a vault broker")
	case deps.Quarantine == nil:
		return AuditReport{}, cascade.New(cascade.KindInvalidInput, "secrets: the audit report needs a quarantine ledger")
	case deps.Now == nil:
		return AuditReport{}, cascade.New(cascade.KindInvalidInput, "secrets: the audit report needs a clock")
	}
	names, err := deps.Broker.List(ctx)
	if err != nil {
		return AuditReport{}, err
	}
	now := deps.Now()
	grants, err := auditGrants(ctx, deps.Broker, names, now)
	if err != nil {
		return AuditReport{}, err
	}
	depth, err := deps.Quarantine.PendingCount()
	if err != nil {
		return AuditReport{}, err
	}
	return AuditReport{
		Backend:         deps.Broker.Backend(),
		Entries:         auditEntries(names),
		Grants:          grants,
		QuarantineDepth: depth,
		GeneratedAt:     now.UTC(),
	}, nil
}

// auditEntries classifies each name. Sorted, so two runs over the same
// vault produce byte-identical output.
func auditEntries(names []string) []AuditEntry {
	out := make([]AuditEntry, 0, len(names))
	for _, name := range names {
		out = append(out, AuditEntry{Name: name, Kind: auditEntryKind(name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// auditEntryKind derives an entry's kind from its name alone.
func auditEntryKind(name string) string {
	switch {
	case !strings.HasPrefix(name, oauthKeyPrefix):
		return "secret"
	case strings.HasSuffix(name, oauthRecordSuffix):
		return "oauth-record"
	default:
		return "oauth-token"
	}
}

// auditGrants decodes every stored OAuth record and grades its expiry.
// A record it cannot decode fails the whole report rather than being
// dropped: an audit that silently omitted a grant would understate what
// the vault holds, which is the one thing this surface must not do.
func auditGrants(ctx context.Context, broker *Broker, names []string, now time.Time) ([]AuditGrant, error) {
	var out []AuditGrant
	for _, name := range names {
		if !strings.HasPrefix(name, oauthKeyPrefix) || !strings.HasSuffix(name, oauthRecordSuffix) {
			continue
		}
		rec, err := readOAuthRecord(ctx, broker, name)
		if err != nil {
			return nil, err
		}
		out = append(out, AuditGrant{
			Provider:     rec.Provider,
			Account:      rec.Account,
			ExpiresAt:    rec.ExpiresAt.UTC(),
			Expired:      !rec.ExpiresAt.IsZero() && !rec.ExpiresAt.After(now),
			ExpiringSoon: !rec.ExpiresAt.IsZero() && rec.ExpiresAt.After(now) && rec.ExpiresAt.Before(now.Add(oauthExpiryWindow)),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Account < out[j].Account
	})
	return out, nil
}

// String renders the human table. It is deterministic and carries no
// value, and it says so in its own footer so an operator reading a pasted
// report knows the surface withholds values by construction.
func (r AuditReport) String() string {
	var b strings.Builder
	b.WriteString("backend: " + r.Backend + "\n")
	b.WriteString("entries: " + strconv.Itoa(len(r.Entries)) + "\n")
	for _, e := range r.Entries {
		b.WriteString("  " + e.Name + "  (" + e.Kind + ")\n")
	}
	if len(r.Grants) > 0 {
		b.WriteString("oauth grants:\n")
		for _, g := range r.Grants {
			b.WriteString("  " + g.Provider + "/" + g.Account + "  " + grantState(g) + "\n")
		}
	}
	b.WriteString("quarantined detections: " + strconv.Itoa(r.QuarantineDepth) + "\n")
	b.WriteString("per-entry metadata: unavailable in this build\n")
	b.WriteString("values are never shown here; read one with `cascade vault get NAME`\n")
	return strings.TrimRight(b.String(), "\n")
}

// grantState renders one grant's expiry state.
func grantState(g AuditGrant) string {
	switch {
	case g.Expired:
		return "EXPIRED " + g.ExpiresAt.Format(time.RFC3339)
	case g.ExpiringSoon:
		return "expires soon " + g.ExpiresAt.Format(time.RFC3339)
	case g.ExpiresAt.IsZero():
		return "no declared expiry"
	default:
		return "valid until " + g.ExpiresAt.Format(time.RFC3339)
	}
}

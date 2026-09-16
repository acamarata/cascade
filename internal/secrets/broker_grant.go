// Purpose: the standing-grant read path — the headless daemon's only way
//
//	to a vault value, and the audit trail beside it (R-14.243).
//
// Split from broker.go for Art.10.3's 300-line cap, and by subject: this
//
//	file is one authority model, broker.go is the store's own verbs.
//
// SPORT: internal/secrets grant-read/ADD — P1-E10-W4-S87-T1.

package secrets

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// GetGranted returns a secret's value under a STANDING GRANT rather than a
// live elevation (R-14.243).
//
// This is the headless daemon's only read path, and it is deliberately a
// SEPARATE method rather than a branch inside Get. A caller that holds a
// *Broker must name which authority it is using; a Get that silently
// consulted grants would mean every existing elevated call site quietly
// gained a second way to succeed, which is how an exemption gets built by
// accident.
//
// The grant must be live for THIS name and for vault.get specifically. A
// grant for another key, an expired one, a revoked one, or none at all all
// produce the same typed refusal naming the key — never an empty value,
// never a prompt from a process with nobody to answer it.
func (b *Broker) GetGranted(ctx context.Context, name string, grants *Grants) ([]byte, error) {
	if err := validateSecretName(name); err != nil {
		return nil, err
	}
	if grants == nil {
		return nil, cascade.Newf(cascade.KindUnavailable,
			"secrets: reading %s needs a standing grant and no grant register is configured", name)
	}
	grant, ok, err := grants.Lookup(ctx, name, VerbGet)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, cascade.Newf(cascade.KindUnavailable,
			"secrets: no live grant authorises reading %s; issue one with `cascade vault grant %s`", name, name)
	}
	value, err := b.custody.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	b.recordGrantUse(ctx, grant)
	return value, nil
}

// GrantAudit receives a record of every granted read. It is an interface
// of one method so the broker never imports the audit package, keeping
// internal/secrets' import set to pkg/cascade plus stdlib — the property
// arch_secrets_test.go guards.
type GrantAudit interface {
	// GrantUsed records one read authorised by grantID against keyRef. It
	// is never handed a value.
	GrantUsed(ctx context.Context, grantID, keyRef string)
}

// WithGrantAudit returns a broker that records every granted read through
// sink. Audit is opt-in at the composition root rather than required here,
// because a broker built without one still refuses everything a broker
// with one refuses — the audit adds a trail, not a permission.
func (b *Broker) WithGrantAudit(sink GrantAudit) *Broker {
	out := *b
	out.grantAudit = sink
	return &out
}

// recordGrantUse writes the trail entry for one granted read. It carries
// the grant id and the key NAME only: the value never enters an audit
// record, which is this package's standing rule.
func (b *Broker) recordGrantUse(ctx context.Context, grant Grant) {
	if b.grantAudit == nil {
		return
	}
	b.grantAudit.GrantUsed(ctx, grant.ID, grant.KeyRef)
}

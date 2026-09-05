package egress

import (
	"context"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Engine is the firewall a subsystem writes through. It binds a registry
// to the vault, detector and rewriter one process uses. Build one with
// NewEngine; the zero value refuses everything, which is the correct
// behaviour for a firewall nobody configured.
type Engine struct {
	registry *Registry
	vault    Vault
	detector *secrets.Detector
	rewriter *secrets.Rewriter
}

// NewEngine binds registry to the value source and detector the process
// runs. Every argument is required: an engine missing any of them could
// only fail closed on every call, and a firewall that refuses everything
// silently reads to an operator like a firewall that is working.
func NewEngine(registry *Registry, vault Vault, detector *secrets.Detector) (*Engine, error) {
	switch {
	case registry == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "egress: an engine needs a registry")
	case vault == nil:
		return nil, cascade.Wrap(cascade.KindInvalidInput, ErrNoVault, "egress: an engine needs a vault")
	case detector == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "egress: an engine needs a detector")
	}
	return &Engine{registry: registry, vault: vault, detector: detector, rewriter: secrets.NewRewriter()}, nil
}

// Registry reports the registry this engine enforces. A nil engine
// reports no registry rather than panicking: a diagnostic accessor that
// crashes the caller it was meant to inform is not a diagnostic.
func (e *Engine) Registry() *Registry {
	if e == nil {
		return nil
	}
	return e.registry
}

// Capability issues the capability for class from this engine's registry.
func (e *Engine) Capability(class EgressClass) (Capability, error) {
	if e == nil || e.registry == nil {
		return Capability{}, cascade.New(cascade.KindUnavailable, "egress: no engine configured")
	}
	return e.registry.Capability(class)
}

// Intercept is the choke point. It returns bytes that are safe to write,
// or an error and NOTHING to write.
//
// token is the unforgeable capability the registry issued for the
// destination class; tier is the classification the CALLER declares for
// content. The tier is an explicit argument on purpose: it is never
// derived from ctx and never inferred from the bytes.
//
// Order of refusal, and why it is this order: the capability is checked
// first, then the class, then the tier, and only then is any byte of
// content examined. A caller with no business on this path never gets its
// content read by the detector, and an unknown class is reported without
// the firewall having touched what it was asked to send.
func (e *Engine) Intercept(ctx context.Context, token Capability, tier SensitivityTier, content []byte) ([]byte, error) {
	if err := e.admit(token, tier); err != nil {
		return nil, err
	}
	out, err := SubstitutionPass(ctx, e.vault, e.detector, e.rewriter, content)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// admit runs every check that precedes reading content: the capability,
// the class registration, the enabled bit and the sensitivity matrix.
func (e *Engine) admit(token Capability, tier SensitivityTier) error {
	if e == nil || e.registry == nil {
		return cascade.New(cascade.KindUnavailable, "egress: no engine configured")
	}
	if token.class == "" {
		return cascade.Wrap(cascade.KindCapabilityDenied, ErrCapabilityRequired,
			"egress: a zero capability names no class; obtain one from the registry")
	}
	cfg, ok := e.registry.Lookup(token.class)
	if !ok {
		return unknownClass(token.class)
	}
	if !cfg.Enabled {
		return disabledClass(token.class, cfg.Owner)
	}
	if serr := SensitivityPass(token.class, cfg, tier); serr != nil {
		return serr
	}
	return nil
}

// InterceptClass is the convenience entry point for a caller that holds a
// class rather than a capability: it acquires the capability and
// intercepts in one step. It is exactly as strict as Intercept, because
// acquiring the capability is itself refused for an unknown or disabled
// class.
func (e *Engine) InterceptClass(ctx context.Context, class EgressClass, tier SensitivityTier, content []byte) ([]byte, error) {
	if e == nil || e.registry == nil {
		return nil, cascade.New(cascade.KindUnavailable, "egress: no engine configured")
	}
	token, err := e.Capability(class)
	if err != nil {
		return nil, err
	}
	return e.Intercept(ctx, token, tier, content)
}

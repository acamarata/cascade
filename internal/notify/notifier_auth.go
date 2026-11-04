package notify

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SendAuthorizer checks whether a notification may be sent.
type SendAuthorizer func(ctx context.Context, n Notification) error

// SetSendAuthorizer installs the send authorizer once.
func (nf *Notifier) SetSendAuthorizer(a SendAuthorizer) error {
	nf.authMu.Lock()
	defer nf.authMu.Unlock()
	if nf.authSet {
		return cascade.New(cascade.KindConflict, "notify: send authorizer already set")
	}
	nf.authorizer, nf.authSet = a, true
	return nil
}

func (nf *Notifier) authorize(ctx context.Context, n Notification) error {
	switch n.Class.Resolve() {
	case ClassGlobalCritical:
		for _, source := range GlobalCriticalSources() {
			if n.Source == source {
				return nil
			}
		}
		return cascade.New(cascade.KindInvalidInput, "notify: invalid global-critical source")
	case ClassAddressed, ClassScoped:
		nf.authMu.RLock()
		a := nf.authorizer
		nf.authMu.RUnlock()
		if a != nil {
			return a(ctx, n)
		}
		if n.TargetScope != "" && n.TargetScope != n.OriginScope {
			return cascade.New(cascade.KindUnavailable, "no send authorizer")
		}
	case classUnresolvable:
		// Resolve never returns it; refuse rather than send unchecked.
		return cascade.New(cascade.KindInvalidInput, "notify: unresolvable class")
	}
	return nil
}

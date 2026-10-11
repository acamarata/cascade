package rpc

// Purpose: fail-closed access policy before any RPC middleware or SSE handler.
// Inputs: declared class and transport caller. Outputs: permission decision and event predicate.
// Constraints: undeclared/unknown classes never dispatch; a missing Caller is never owner.
// SPORT: rpc/Access.

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Access describes the authority required for a registered method.
type Access uint8

// Access classes are closed; values beyond accessMax always refuse.
const (
	AccessUnmarked Access = iota
	AccessOpen
	AccessOwnerOnly
	AccessOwnerView
	AccessScoped
	AccessIngest
	accessMax = AccessIngest
)

// DefaultStreamTopic marks the daemon-wide stream.
const DefaultStreamTopic = ""

// EventVisibleConsumer promises that the handler checks EventVisibleFrom for every event.
type EventVisibleConsumer interface{ ConsumesEventVisible() }

var accessMu sync.RWMutex
var methodAccess = make(map[string]Access)
var topicAccess = make(map[string]Access)
var errUnmarked = cascade.New(cascade.KindInternal, "verb has no access marking")
var errUnknownAccess = cascade.New(cascade.KindInternal, "unknown access class")
var errAccessDenied = cascade.New(cascade.KindPermissionDenied, "caller not permitted")

func accessErrorObject(err error) *ErrorObject {
	eo := errorObjectFrom(err)
	if e, ok := err.(*cascade.Error); ok {
		eo.Message = e.Msg
		eo.Data = map[string]string{"kind": e.Kind.String()}
	}
	return eo
}

// DeclareAccess records one declaration; duplicates are a startup error.
func DeclareAccess(method string, a Access) struct{} {
	accessMu.Lock()
	defer accessMu.Unlock()
	if _, exists := methodAccess[method]; exists {
		panic("duplicate access declaration: " + method)
	}
	methodAccess[method] = a
	return struct{}{}
}

// DeclareTopicAccess restricts stream declarations to classes with a read policy.
func DeclareTopicAccess(topic string, a Access) struct{} {
	if a != AccessOwnerOnly && a != AccessOwnerView {
		panic("invalid topic access class")
	}
	accessMu.Lock()
	defer accessMu.Unlock()
	if _, exists := topicAccess[topic]; exists {
		panic("duplicate topic access declaration: " + topic)
	}
	topicAccess[topic] = a
	return struct{}{}
}

// SetAccessPolicy enables admission checks even when no resolver is supplied.
func (r *Registry) SetAccessPolicy(resolve func(string) (principal.Principal, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accessEnabled, r.resolvePrincipal = true, resolve
}

// SetEventVisibleFactory installs the session ancestry predicate producer.
func (r *Registry) SetEventVisibleFactory(f func(principal.Principal) func(string, string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventVisibleFactory = f
}

func declaredAccess(method string) Access {
	accessMu.RLock()
	defer accessMu.RUnlock()
	return methodAccess[method]
}

func checkAccess(ctx context.Context, a Access) error {
	if a > accessMax {
		return errUnknownAccess
	}
	c, ok := principal.CallerFrom(ctx)
	switch a {
	case AccessUnmarked:
		return errUnmarked
	case AccessOpen, AccessIngest:
		return nil
	case AccessOwnerOnly:
		if ok && c.Principal == nil {
			return nil
		}
	case AccessOwnerView:
		if ok {
			return nil
		}
	case AccessScoped:
		if ok && c.Principal != nil {
			return nil
		}
	}
	return errAccessDenied
}

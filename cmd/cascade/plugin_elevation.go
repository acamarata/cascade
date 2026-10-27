// Purpose: hold every plugin-store write of an elevated plugin verb
//
//	(plugin.add, plugin.update, perms.grant, perms.revoke) behind the signed
//	local elevation gate.
//
// Inputs: the plugin deps (their Gate) and the verb the write serves.
//
// Outputs: withElevatedPluginStore, which runs fn over a store whose first
//
//	write (Put, Delete or Tx) waits for the gate; reads pass through.
//
// Constraints: the proof is asked for only when the command is about to
//
//	write, so an already-installed add or a daemon-routed elevated add asks
//	once or not at all; a refused or failed proof is remembered and every
//	later write of the same call refuses with it. A nil gate refuses. The
//	production gate's audit rows go to the store the command already holds
//	(storeAuditedGate): the runtime store admits one opener.
//
// SPORT: cli/plugin-lifecycle elevated writes (P1-SEC-08).

package main

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// elevatedWriteStore admits writes only after one granted Authorize.
type elevatedWriteStore struct {
	provider.Store
	gate secrets.ElevationGate
	verb string

	mu      sync.Mutex
	decided bool
	err     error
}

// admit asks the gate once per call and remembers the answer.
func (s *elevatedWriteStore) admit(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.decided {
		s.decided = true
		if s.gate == nil {
			s.err = cascade.Newf(cascade.KindElevationRequired,
				"plugin: %s needs a signed presence proof and no elevation gate is configured", s.verb)
		} else {
			s.err = s.gate.Authorize(ctx, s.verb)
		}
	}
	return s.err
}

// Put implements provider.Store behind the gate.
func (s *elevatedWriteStore) Put(ctx context.Context, namespace, key string, value []byte) error {
	if err := s.admit(ctx); err != nil {
		return err
	}
	return s.Store.Put(ctx, namespace, key, value)
}

// Delete implements provider.Store behind the gate.
func (s *elevatedWriteStore) Delete(ctx context.Context, namespace, key string) error {
	if err := s.admit(ctx); err != nil {
		return err
	}
	return s.Store.Delete(ctx, namespace, key)
}

// Tx implements provider.Store behind the gate: a transaction may write.
func (s *elevatedWriteStore) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	if err := s.admit(ctx); err != nil {
		return err
	}
	return s.Store.Tx(ctx, fn)
}

// withElevatedPluginStore is withPluginStore whose writes need verb's proof.
func withElevatedPluginStore[T any](ctx context.Context, deps pluginDeps, verb string,
	fn func(context.Context, provider.Store) (T, error)) (T, error) {
	return withPluginStore(ctx, deps, func(ctx context.Context, store provider.Store) (T, error) {
		return fn(ctx, &elevatedWriteStore{Store: store, gate: storeAuditedGate(deps, store), verb: verb})
	})
}

// storeAuditedGate points the production gate's rows at the runtime store
// this command already holds open (the store admits one opener, so the
// gate's own sink could not open it). The rows go through the plain gate
// log: no vault is read.
func storeAuditedGate(deps pluginDeps, store provider.Store) secrets.ElevationGate {
	g, ok := deps.Gate.(*elevationGate)
	if !ok {
		return deps.Gate
	}
	if _, auto := g.sink.(*autoGateSink); !auto {
		return deps.Gate
	}
	audited := *g
	audited.sink = openGateAuditLog(store, deps.Clock, nil)
	return &audited
}

// gateRefusalRecorder is the production gate's record of a refusal decided
// by a command before any store write.
type gateRefusalRecorder interface {
	RecordRefusal(ctx context.Context, verb, reason string, refusal error) error
}

// recordPluginRefusal records refusal as attempt then deny with reason
// through deps' gate and returns it (with any audit failure attached).
func recordPluginRefusal(ctx context.Context, deps pluginDeps, verb, reason string, refusal error) error {
	if r, ok := deps.Gate.(gateRefusalRecorder); ok {
		return r.RecordRefusal(ctx, verb, reason, refusal)
	}
	return refusal
}

// isDaemonRequired reports the plugins package's daemon-required refusal.
func isDaemonRequired(err error, label string) bool {
	return err != nil && err.Error() == plugins.ErrDaemonRequiredForElevatedPluginOp(label).Error()
}

//go:build !windows

// Purpose: the two seams between the hooks Dispatcher's rehydration step
// and the daemon: the vault bindings an operator admitted, and the secrets
// rehydrator that resolves only the names those bindings give.
// Inputs: the daemon's HotReloader (the admitted config) and a rehydrator
// over the process broker.
// Outputs: a binding source for hooks.BoundRehydrate and the RehydrateBytes
// function it calls.
// Constraints: bindings come from the admitted config the reloader holds,
// never from config.toml on disk. A pii tag is refused before the binding
// predicate is asked. Error text is fixed: a malformed tag or section can
// quote a configured value, so no cause text is carried.
// SPORT: hook rehydration in the cmd/cascade composition (P1-SUP-02).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// admittedHookBindings reads the vault bindings of the [hooks] section the
// reloader currently holds. It is called only when a tag is asked about.
func admittedHookBindings(hr *runtime.HotReloader) func() ([]hooks.VaultBinding, error) {
	return func() ([]hooks.VaultBinding, error) {
		if hr == nil || hr.Current() == nil {
			return nil, cascade.New(cascade.KindUnavailable, "hooks: no admitted configuration to read vault bindings from")
		}
		list, err := hooks.ParseHooksSection(hr.Current().Extra[hooks.SectionName], hookRunnable)
		if err != nil {
			return nil, cascade.New(cascade.KindInvalidInput, "hooks: the admitted [hooks] section does not parse")
		}
		bindings, err := hooks.VaultBindingsOf(list, hookTagNames)
		if err != nil {
			return nil, cascade.New(cascade.KindInvalidInput, "hooks: the admitted vault bindings are invalid")
		}
		return bindings, nil
	}
}

// rehydrateOver adapts r to the seam's RehydrateBytes. A pii tag is refused
// before allowName is consulted, so no binding can ever admit one.
func rehydrateOver(r *secrets.Rehydrator) hooks.RehydrateBytes {
	return func(ctx context.Context, tagged []byte, allowName func(string) bool) ([]byte, func(), error) {
		if r == nil || allowName == nil {
			return nil, func() {}, cascade.New(cascade.KindUnavailable, "hooks: no rehydrator configured")
		}
		rc, err := r.RehydrateOnly(ctx, tagged, func(tag secrets.Tag) bool {
			return tag.Type != secrets.TagPII && allowName(tag.Name)
		})
		if err != nil {
			return nil, func() {}, err
		}
		return rc.Data, rc.Zero, nil
	}
}

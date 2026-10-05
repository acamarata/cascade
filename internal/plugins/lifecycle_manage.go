// Package plugins (lifecycle_manage.go): Purpose: `cascade plugin enable/disable/remove` business logic. Split
// from lifecycle.go (not itself in files_scope) under the same 300-line
// cap that forced approval_standing.go off approval.go — the ticket's own
// worked precedent for splitting one command family's non-elevated verbs
// into a same-package sibling file rather than crowding the file that owns
// the record type.
//
// Teardown runs under TeardownDeadline: a teardown that never returns (a
// plugin that ignores the drain) fails the remove with KindUnavailable and
// the metadata record is kept, rather than blocking `remove` forever.
//
// SPORT: internal/plugins lifecycle-manage/ADD — P1-E15-W4-S32-T4; teardown deadline (CHANGE) — P1-PLG-09.
package plugins

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ProcessTeardown is the local seam for O/S-31.T3's clean-shutdown step
// `remove` must run before a metadata record is deleted: drain in-flight
// calls and run the manifest's declared uninstall hook, if any. A nil
// ProcessTeardown is valid (e.g. a plugin that was never actually
// launched, or a builtin-tier plugin with nothing to tear down) and
// RemovePlugin treats it as "nothing to drain," never as an error.
type ProcessTeardown interface {
	// Teardown drains name's in-flight calls and runs its uninstall hook.
	Teardown(ctx context.Context, name string) error
}

// TeardownDeadline bounds how long RemovePlugin waits for a teardown. A
// variable only so a test can shorten it; production code never assigns
// it.
var TeardownDeadline = 10 * time.Second

// SetEnabled toggles name's Enabled flag and reports the record's state
// AFTER the change, so a caller can print "before -> after" without a
// second read. Setting to the state a record is already in is a no-write
// idempotent no-op (still returns the current record, no error) — neither
// direction needs elevation (§5.14 lists neither).
func SetEnabled(ctx context.Context, store provider.Store, name string, enabled bool) (PluginMetadata, error) {
	if err := validateName(name); err != nil {
		return PluginMetadata{}, err
	}
	rec, ok, err := LoadMetadata(ctx, store, name)
	if err != nil {
		return PluginMetadata{}, err
	}
	if !ok {
		return PluginMetadata{}, cascade.Newf(cascade.KindNotFound, "plugin: %q is not installed", name)
	}
	if rec.Enabled == enabled {
		return rec, nil
	}
	rec.Enabled = enabled
	if err := SaveMetadata(ctx, store, rec); err != nil {
		return PluginMetadata{}, err
	}
	return rec, nil
}

// RemovePlugin tears down (when teardown is non-nil) and deletes name's
// metadata record. Removing always reduces grants to nothing, so §5.14
// never elevates this verb — RemovePlugin takes no daemonAvailable
// parameter, unlike AddPlugin/UpdatePlugin/ChangePerms, and that omission
// is itself the enforcement: there is no elevation branch to gate.
func RemovePlugin(ctx context.Context, store provider.Store, teardown ProcessTeardown, name string) (PluginMetadata, error) {
	if err := validateName(name); err != nil {
		return PluginMetadata{}, err
	}
	rec, ok, err := LoadMetadata(ctx, store, name)
	if err != nil {
		return PluginMetadata{}, err
	}
	if !ok {
		return PluginMetadata{}, cascade.Newf(cascade.KindNotFound, "plugin: %q is not installed", name)
	}
	if teardown != nil {
		if err := runTeardown(ctx, teardown, name); err != nil {
			return PluginMetadata{}, err
		}
	}
	if err := DeleteMetadata(ctx, store, name); err != nil {
		return PluginMetadata{}, err
	}
	return rec, nil
}

// runTeardown runs teardown under TeardownDeadline. The teardown gets the
// bounded context, and RemovePlugin returns at the deadline even if the
// teardown ignores that context.
func runTeardown(ctx context.Context, teardown ProcessTeardown, name string) error {
	deadline := TeardownDeadline
	tctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- teardown.Teardown(tctx, name) }()
	select {
	case err := <-done:
		return err
	case <-tctx.Done():
		return cascade.Wrapf(cascade.KindUnavailable, tctx.Err(),
			"plugin: %q teardown did not finish within %s; the record is kept", name, deadline)
	}
}

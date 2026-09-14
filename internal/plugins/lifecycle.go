// Package plugins (lifecycle.go): Purpose: the installed-plugin metadata
// record `cascade plugin` manages, and its CRUD against the reserved host
// storage slot (R-14.100's "plugin.__host__.metadata/<name>").
//
// Inputs: a pkg/provider.Store bound by the caller (cmd/cascade's
//
//	composition root — see Contract-vs-tree note below) and a plugin name.
//
// Outputs: PluginMetadata records; a not-found sentinel on a missing name.
//
// Constraints: this file imports only pkg/** (never internal/**):
//
//	.golangci.yml's plugins-providers-boundary depguard rule denies ANY
//	non-test file matching "**/plugins/**/*.go" from importing
//	internal/**, which includes this file itself (confirmed by
//	P1-E15-W4-S31-T3/T4's journals for the identical pattern one
//	directory over). internal/storage.PluginStorage — the natural-looking
//	fit — is therefore never imported here: it also isolates ONE plugin's
//	own data from every OTHER plugin's (NewPluginStorage refuses the
//	reserved "__host__" id outright, by design — R-14.100's own
//	ReservedPluginHostNamespace doc comment). What this package needs is
//	the HOST's own bookkeeping across every installed plugin, which is a
//	plain pkg/provider.Store namespace, not a PluginStorage instance.
//	cmd/cascade/plugin.go (outside plugins/**, so unrestricted) is the
//	real composition root that supplies a *providers/sqlite.Driver
//	(itself a provider.Store) bound to this namespace.
//
// SPORT: internal/plugins lifecycle-metadata/ADD — P1-E15-W4-S32-T4.
package plugins

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// HostNamespace is the raw pkg/provider.Store namespace every installed
// plugin's metadata record lives under: "plugin.__host__", matching
// internal/storage.ReservedPluginHostNamespace's literal value exactly
// (redeclared here, not imported, per this file's package doc — the two
// packages cannot share a symbol across the depguard boundary, so both
// sides fix the same literal; lifecycle_test.go asserts this constant
// against the string internal/storage exports as defense against drift).
const HostNamespace = "plugin.__host__"

// metadataKeyPrefix is the key prefix every metadata record lives under
// inside HostNamespace, per the contract's literal
// "plugin.__host__.metadata/<name>" (the ".metadata/" segment is the KEY,
// not a second namespace level — pkg/provider.Store has no nested
// namespaces).
const metadataKeyPrefix = "metadata/"

// PluginMetadata is one installed plugin's host-owned bookkeeping record:
// everything `cascade plugin` needs to answer list/info/enable/disable
// without re-parsing the plugin's manifest on every call.
type PluginMetadata struct {
	// Name is the plugin's manifest ID (pkg/plugin.Manifest.ID) and this
	// record's key.
	Name string `json:"name"`
	// InstalledVersion is the manifest Version last successfully
	// installed or updated to.
	InstalledVersion string `json:"installed_version"`
	// Enabled reports whether the plugin is currently active.
	Enabled bool `json:"enabled"`
	// RuntimeMode mirrors the installed manifest's Runtime field.
	RuntimeMode plugin.RuntimeMode `json:"runtime_mode"`
	// PinnedChecksum is the hex checksum `add`/`update --checksum`
	// verified the installed artifact against, empty when none was
	// supplied.
	PinnedChecksum string `json:"pinned_checksum,omitempty"`
	// Grants is the capability grant set currently in effect, mirroring
	// the installed manifest's Requires at the time it was granted.
	Grants []string `json:"grants"`
}

// metadataKey builds the storage key for name's record.
func metadataKey(name string) string { return metadataKeyPrefix + name }

// validateName refuses an empty name: every entry point in this file and
// its siblings (lifecycle_add.go, lifecycle_update.go) funnels through
// this first, fail-closed (no operation silently no-ops on "").
func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return cascade.New(cascade.KindInvalidInput, "plugin: name is empty")
	}
	return nil
}

// LoadMetadata reads name's record from store. ok is false (with a nil
// error) when no record exists — callers distinguish "not installed" from
// a real storage failure by checking ok, not by testing err against
// KindNotFound, so a future store implementation's exact not-found
// signaling never leaks into every caller.
func LoadMetadata(ctx context.Context, store provider.Store, name string) (rec PluginMetadata, ok bool, err error) {
	if err := validateName(name); err != nil {
		return PluginMetadata{}, false, err
	}
	raw, err := store.Get(ctx, HostNamespace, metadataKey(name))
	if err != nil {
		if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindNotFound {
			return PluginMetadata{}, false, nil
		}
		return PluginMetadata{}, false, err
	}
	var rec2 PluginMetadata
	if err := json.Unmarshal(raw, &rec2); err != nil {
		return PluginMetadata{}, false, cascade.Wrapf(cascade.KindIntegrity, err,
			"plugin: %s's stored metadata record is corrupt", name)
	}
	return rec2, true, nil
}

// SaveMetadata writes rec under its own Name, unconditionally overwriting
// any prior record — callers that need idempotent-vs-conflict semantics
// (add's "already installed at vN") check LoadMetadata first.
func SaveMetadata(ctx context.Context, store provider.Store, rec PluginMetadata) error {
	if err := validateName(rec.Name); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "plugin: encode %s's metadata record", rec.Name)
	}
	return store.Put(ctx, HostNamespace, metadataKey(rec.Name), raw)
}

// DeleteMetadata removes name's record. Idempotent: deleting an
// already-absent record is not an error (mirrors provider.Store.Delete's
// own documented idempotency).
func DeleteMetadata(ctx context.Context, store provider.Store, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return store.Delete(ctx, HostNamespace, metadataKey(name))
}

// ListMetadata returns every installed plugin's record, sorted by Name for
// deterministic output (`plugin list` never reorders between runs with no
// intervening change).
func ListMetadata(ctx context.Context, store provider.Store) ([]PluginMetadata, error) {
	it, err := store.Scan(ctx, HostNamespace, metadataKeyPrefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()

	var recs []PluginMetadata
	for it.Next(ctx) {
		var rec PluginMetadata
		if err := json.Unmarshal(it.Value(), &rec); err != nil {
			return nil, cascade.Wrapf(cascade.KindIntegrity, err,
				"plugin: stored metadata record %q is corrupt", it.Key())
		}
		recs = append(recs, rec)
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	return recs, nil
}

// ErrNoInputHardError is the §5.8 CASCADE_NO_INPUT=1 refusal: a caller
// asked for an interactive prompt (process-tier consent, an elevation
// dialog) while automation mode forbids ever showing one. Never a silent
// default — the operation stops here, unconditionally.
func ErrNoInputHardError(verb string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"cascade plugin %s: CASCADE_NO_INPUT=1 hard-errors on any interactive prompt; "+
			"pass the required confirmation flag non-interactively instead", verb)
}

// ChangePerms applies a §5.14 perms grant|revoke: both directions are
// unconditionally elevated, so daemonAvailable is checked first (D/S-07.T4)
// and noInput second (§5.8) — an elevated call that cannot prompt refuses
// before it can be told "yes" on its own behalf. grant=true adds cap to
// name's Grants (idempotent: already-granted is a no-op, not an error);
// grant=false removes it (idempotent: already-absent is a no-op).
func ChangePerms(ctx context.Context, store provider.Store, name, capability string, grant, daemonAvailable, noInput bool) (PluginMetadata, error) {
	if !daemonAvailable {
		verb := "perms grant"
		if !grant {
			verb = "perms revoke"
		}
		return PluginMetadata{}, ErrDaemonRequiredForElevatedPluginOp(verb)
	}
	if noInput {
		verb := "perms grant"
		if !grant {
			verb = "perms revoke"
		}
		return PluginMetadata{}, ErrNoInputHardError(verb)
	}
	if err := validateName(name); err != nil {
		return PluginMetadata{}, err
	}
	if strings.TrimSpace(capability) == "" {
		return PluginMetadata{}, cascade.New(cascade.KindInvalidInput, "plugin: perms capability is empty")
	}

	rec, ok, err := LoadMetadata(ctx, store, name)
	if err != nil {
		return PluginMetadata{}, err
	}
	if !ok {
		return PluginMetadata{}, cascade.Newf(cascade.KindNotFound, "plugin: %q is not installed", name)
	}

	rec.Grants = applyGrantChange(rec.Grants, capability, grant)
	if err := SaveMetadata(ctx, store, rec); err != nil {
		return PluginMetadata{}, err
	}
	return rec, nil
}

// applyGrantChange returns grants with cap added (grant=true) or removed
// (grant=false), idempotently, preserving order for the entries that
// remain.
func applyGrantChange(grants []string, capability string, grant bool) []string {
	idx := -1
	for i, g := range grants {
		if g == capability {
			idx = i
			break
		}
	}
	if grant {
		if idx >= 0 {
			return grants
		}
		return append(append([]string(nil), grants...), capability)
	}
	if idx < 0 {
		return grants
	}
	out := make([]string, 0, len(grants)-1)
	out = append(out, grants[:idx]...)
	out = append(out, grants[idx+1:]...)
	return out
}

// grantsExpand reports whether want contains any capability not already
// present in have — the §5.14 "grant-expansion" trigger both add and
// update consult. Removing a grant, or requesting an identical set, is
// never an expansion.
func grantsExpand(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, g := range have {
		set[g] = struct{}{}
	}
	for _, g := range want {
		if _, ok := set[g]; !ok {
			return true
		}
	}
	return false
}

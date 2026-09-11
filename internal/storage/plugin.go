// Package storage (plugin.go): Purpose: PluginStorage — the namespaced,
//
//	isolation-enforcing storage surface a plugin host binds to a plugin
//	at load time (02-TARGET-STRUCTURE.md "Storage scoping").
//
// Tree correction (full contradiction quoted in this ticket's journal):
// domains.go's DomainID set is CLOSED (R-14.5/R-16.51, exhaustive-linted);
// PluginStorage is NOT a twelfth domain, it wraps provider.Store under the
// plain namespace string "plugin.<id>" per ReservedPluginHostNamespace's
// own doc comment. "Domain-ownership registry" is PluginDomainRegistry
// below, a string-keyed single-writer tracker mirroring providers/sqlite/
// executor.go's own string-keyed DomainRegistry, declared at the
// provider.Store abstraction level so it stays SQLite/Postgres portable.
//
// Import-cycle correction: internal/policy/grant_store.go imports
// internal/storage, and internal/secrets/clipboard.go imports
// internal/policy — so this package cannot import either directly (both
// complete a cycle back here). GrantChecker/SecretScanner below are local,
// primitives-only seams (domains.go's Clock/GrantRegistry pattern). A
// composition root outside this ticket's files_scope adapts real
// *policy.StoreGrants/*secrets.Detector values in; plugin_test.go's fakes
// prove the seam shape (test files may import both without completing the
// cycle — Go permits a package's tests to depend on its own dependents).
//
// SPORT: internal.storage.PluginStorage/ADDED,
//
//	internal.storage.PluginDomainRegistry/ADDED (P1-E15-W4-S32-T3).
package storage

import (
	"context"
	"regexp"
	"sort"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// pluginIDPattern mirrors pkg/plugin's idPatternRe (validate.go) exactly,
// redeclared for defense in depth (never trust an upstream-validated id).
var pluginIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// maxPluginIDLen bounds a plugin id so pluginTableName (plugin_migrate.go)
// always fits Postgres's 63-byte identifier limit alongside a suffix.
const maxPluginIDLen = 40

// validatePluginID refuses an empty, malformed, over-length, or
// reserved-namespace id — every entry point funnels through this first.
func validatePluginID(id string) error {
	if id == "" {
		return cascade.New(cascade.KindInvalidInput, "storage: plugin id is empty")
	}
	if len(id) > maxPluginIDLen {
		return cascade.Newf(cascade.KindInvalidInput,
			"storage: plugin id %q is %d bytes, over the %d-byte limit", id, len(id), maxPluginIDLen)
	}
	if !pluginIDPattern.MatchString(id) {
		return cascade.Newf(cascade.KindInvalidInput, "storage: %q is not a well-formed plugin id", id)
	}
	if id == ReservedPluginHostNamespace || pluginNamespace(id) == ReservedPluginHostNamespace {
		return cascade.Newf(cascade.KindInvalidInput,
			"storage: plugin id %q collides with the reserved host namespace %q", id, ReservedPluginHostNamespace)
	}
	return nil
}

// crossDomainCapability names the capability a grant must cover.
func crossDomainCapability(targetID string) string {
	return "plugin.cross_domain." + targetID
}

// GrantChecker is the cross-domain gate seam (local, see package doc).
// Non-nil error always means denied; nil means granted.
type GrantChecker interface {
	CheckGrant(ctx context.Context, subjectID, capability string) error
}

// SecretScanner is the sensitive-payload guard seam (local, same reason).
type SecretScanner interface {
	HasSecret(content []byte) bool // a high-confidence credential-shaped span
}

// PluginStoragePermissionDeniedError is the fail-closed cross-domain
// refusal (wraps cascade.KindPermissionDenied). Reason is diagnostic only.
type PluginStoragePermissionDeniedError struct {
	PluginID string
	Target   string
	Reason   string
	inner    *cascade.Error
}

func newPermissionDeniedError(pluginID, target, reason string, cause error) *PluginStoragePermissionDeniedError {
	return &PluginStoragePermissionDeniedError{
		PluginID: pluginID,
		Target:   target,
		Reason:   reason,
		inner: cascade.Wrapf(cascade.KindPermissionDenied, cause,
			"%s (plugin=%s target=%s reason=%s)", plugin.MsgCrossDomainDenied, pluginID, target, reason),
	}
}

func (e *PluginStoragePermissionDeniedError) Error() string { return e.inner.Error() }
func (e *PluginStoragePermissionDeniedError) Unwrap() error { return e.inner }

// PluginStorageSensitivePayloadError is Set's refusal for a credential-
// shaped value (wraps cascade.KindPolicyDenied; §5.21).
type PluginStorageSensitivePayloadError struct {
	PluginID string
	Key      string
	inner    *cascade.Error
}

func newSensitivePayloadError(pluginID, key string) *PluginStorageSensitivePayloadError {
	return &PluginStorageSensitivePayloadError{
		PluginID: pluginID,
		Key:      key,
		inner: cascade.Newf(cascade.KindPolicyDenied,
			"%s (plugin=%s key=%s)", plugin.MsgSensitivePayload, pluginID, key),
	}
}

func (e *PluginStorageSensitivePayloadError) Error() string { return e.inner.Error() }
func (e *PluginStorageSensitivePayloadError) Unwrap() error { return e.inner }

// ErrPluginDomainOwned is the §D-3 single-writer refusal, mirrored (same
// Kind) from providers/sqlite/executor.go's ErrDomainOwned (not imported
// directly — would tie PluginStorage to the SQLite driver).
var ErrPluginDomainOwned = cascade.New(cascade.KindConflict, "storage: plugin domain already owned by another writer")

// pluginDomainSlot is one registered domain's bookkeeping.
type pluginDomainSlot struct {
	owner   string
	version int
}

// PluginDomainRegistry tracks single-writer ownership + installed version
// per "plugin.<id>" domain. Zero value not usable; use NewPluginDomainRegistry.
type PluginDomainRegistry struct {
	mu    sync.Mutex
	slots map[string]pluginDomainSlot
}

// NewPluginDomainRegistry returns an empty registry.
func NewPluginDomainRegistry() *PluginDomainRegistry {
	return &PluginDomainRegistry{slots: make(map[string]pluginDomainSlot)}
}

// Register claims "plugin.<pluginID>" for owner at version. Same owner +
// same version is idempotent (created=false, nil, no writes); same owner
// + new version updates the record; a different owner gets
// ErrPluginDomainOwned — never silently reassigned.
func (r *PluginDomainRegistry) Register(pluginID, owner string, version int) (created bool, err error) {
	if err := validatePluginID(pluginID); err != nil {
		return false, err
	}
	if owner == "" {
		return false, cascade.New(cascade.KindInvalidInput, "storage: plugin domain registration requires a non-empty owner")
	}
	if version < 1 {
		return false, cascade.Newf(cascade.KindInvalidInput, "storage: plugin domain version must be >= 1, got %d", version)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	domain := pluginNamespace(pluginID)
	existing, ok := r.slots[domain]
	if !ok {
		r.slots[domain] = pluginDomainSlot{owner: owner, version: version}
		return true, nil
	}
	if existing.owner != owner {
		return false, ErrPluginDomainOwned
	}
	if existing.version == version {
		return false, nil // already current: idempotent no-op.
	}
	r.slots[domain] = pluginDomainSlot{owner: owner, version: version}
	return true, nil
}

// Version reports pluginID's registered version, or (0, false).
func (r *PluginDomainRegistry) Version(pluginID string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot, ok := r.slots[pluginNamespace(pluginID)]
	if !ok {
		return 0, false
	}
	return slot.version, true
}

// pluginNamespace is a short local alias for PluginNamespace (domains.go).
func pluginNamespace(pluginID string) string { return PluginNamespace(pluginID) }

// PluginStorage is the namespaced, isolation-enforcing storage surface
// bound to one plugin; implements pkg/plugin.Storage (var _ below).
type PluginStorage struct {
	pluginID string
	store    provider.Store
	grants   GrantChecker
	scanner  SecretScanner
	migrator Migrator
}

var _ plugin.Storage = (*PluginStorage)(nil)

// Migrator is the migration seam Migrate delegates to; real impl is
// PluginMigrator (plugin_migrate.go).
type Migrator interface {
	Apply(ctx context.Context, pluginID string, migrations []plugin.Migration) (plugin.MigrationReport, error)
}

// NewPluginStorage builds a PluginStorage bound to pluginID. All five
// arguments are required (fail-closed: no nil-safe defaults).
func NewPluginStorage(pluginID string, store provider.Store, grants GrantChecker, scanner SecretScanner, migrator Migrator) (*PluginStorage, error) {
	if err := validatePluginID(pluginID); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginStorage requires a non-nil Store")
	}
	if grants == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginStorage requires a non-nil GrantChecker")
	}
	if scanner == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginStorage requires a non-nil SecretScanner")
	}
	if migrator == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: PluginStorage requires a non-nil Migrator")
	}
	return &PluginStorage{pluginID: pluginID, store: store, grants: grants, scanner: scanner, migrator: migrator}, nil
}

// Migrate implements pkg/plugin.Storage, delegating to Migrator.
func (p *PluginStorage) Migrate(ctx context.Context, migrations []plugin.Migration) (plugin.MigrationReport, error) {
	return p.migrator.Apply(ctx, p.pluginID, migrations)
}

// Get implements pkg/plugin.Storage.
func (p *PluginStorage) Get(ctx context.Context, key string) ([]byte, error) {
	return p.store.Get(ctx, pluginNamespace(p.pluginID), key)
}

// Set refuses (never writes) a credential-shaped value.
func (p *PluginStorage) Set(ctx context.Context, key string, value []byte) error {
	if p.scanner.HasSecret(value) {
		return newSensitivePayloadError(p.pluginID, key)
	}
	return p.store.Put(ctx, pluginNamespace(p.pluginID), key, value)
}

// List implements pkg/plugin.Storage.
func (p *PluginStorage) List(ctx context.Context, prefix string) ([]string, error) {
	it, err := p.store.Scan(ctx, pluginNamespace(p.pluginID), prefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()

	var keys []string
	for it.Next(ctx) {
		keys = append(keys, it.Key())
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

// Delete implements pkg/plugin.Storage.
func (p *PluginStorage) Delete(ctx context.Context, key string) error {
	return p.store.Delete(ctx, pluginNamespace(p.pluginID), key)
}

// CrossDomainGet reads key from targetPluginID's domain (host capability
// broker only). Fail-closed: absent/expired grant and malformed target id
// all refuse via *PluginStoragePermissionDeniedError, never silently.
func (p *PluginStorage) CrossDomainGet(ctx context.Context, targetPluginID, key string) ([]byte, error) {
	if err := p.checkCrossDomain(ctx, targetPluginID); err != nil {
		return nil, err
	}
	return p.store.Get(ctx, pluginNamespace(targetPluginID), key)
}

func (p *PluginStorage) checkCrossDomain(ctx context.Context, targetPluginID string) error {
	if err := validatePluginID(targetPluginID); err != nil {
		return newPermissionDeniedError(p.pluginID, targetPluginID, "malformed-scope", err)
	}
	if targetPluginID == p.pluginID {
		return nil // same-domain access never requires a grant.
	}
	if err := p.grants.CheckGrant(ctx, p.pluginID, crossDomainCapability(targetPluginID)); err != nil {
		return newPermissionDeniedError(p.pluginID, targetPluginID, "denied", err)
	}
	return nil
}

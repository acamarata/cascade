package plugins

// Purpose (this file): the real install.Installer the composition root
//   injects -- O/S-32.T4's plugin lifecycle engine, called directly
//   in-process (this file already lives in package internal/plugins, the
//   SAME package lifecycle_add.go/dispatch.go are in) rather than by
//   dialing the daemon's own "plugin.add" RPC back into itself over its
//   own unix socket. DISCLOSED DEVIATION from the ticket's literal "call
//   the daemon RPC plugin.add" wording -- see dispatch.go's own package
//   comment for the precedent this follows (a file directly at
//   internal/plugins/<x>.go is exempt from the plugins-providers-boundary
//   depguard rule).
//
// SECURITY FIX (round-1 adversarial CR, B1): a prior draft called AddPlugin
//   /ProvisionElevated with entry.Checksum but never actually verified the
//   fetched bytes -- AddPlugin only runs pkg/plugin.VerifyArtifact when its
//   checksum argument is non-empty, and the elevated retry RE-FETCHED,
//   opening a TOCTOU window (tampered bytes on the second fetch installed
//   under the first fetch's honest checksum). This file now: (1) refuses
//   any registry entry with an empty Checksum or Signature outright; (2)
//   verifies the fetched buffer with the real pkg/plugin.Ed25519Verifier
//   (checksum + signature) BEFORE it is ever parsed or installed; (3)
//   caches the verified buffer, keyed by {PluginID, Version}, so the
//   elevated retry installs the SAME bytes it verified rather than
//   fetching (and trusting) a second time.
//
// Inputs: an install.AddRequest naming a resolved plugin.Candidate, and
//   (for a registry-sourced candidate) an ElevationWitness proving a real
//   attestation for the elevated retry.
// Outputs: an install.AddResult, or the underlying failure unmodified
//   (checksum/signature mismatch, elevation refusal, registry unreachable).
//
// Constraints:
//   - CandidateSourceInstalled never calls AddPlugin/ProvisionElevated: the
//     resolver's own installed-first step only ever returns an ENABLED
//     installed candidate, so "add" is already satisfied by construction.
//     A candidate the resolver vouches for but this host has no metadata
//     record of is trusted ONLY for a RuntimeBuiltin manifest (a compiled-
//     in builtin that never ran through AddPlugin's own SaveMetadata is a
//     real, benign case); any other runtime with no record is refused
//     rather than reported AlreadyInstalled from nothing (round-1 CR F1c).
//   - CandidateSourceRegistry is the genuine install case. registryURL and
//     registryPubKey are both REFUSING defaults (KindUnavailable) until a
//     composition root injects the real config readers -- no committed
//     config surface names either at this ticket's HEAD (X/S-50.T2 is not
//     this ticket's dependency).
//   - Every write goes through plugins.AddPlugin (non-elevated) or
//     plugins.ProvisionElevated (elevation-witnessed retry) -- never
//     internal/plugins/process's lower-level Install primitive directly.
// SPORT: internal/plugins:cascadepa-install-wiring (ADD) -- P1-E24-W5-S50-T4.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"sync"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// installerAdapter implements install.Installer over the real lifecycle
// engine. Every collaborator is lazily constructed exactly once (sync.Once)
// against the SAME cascade.db shared (a *sharedCascadeStore) points at --
// production always leaves the override fields nil; a same-package test
// sets them. shared is REQUIRED, not optional: cascadepa_install_confirm.go
// and cascadepa_install_dbevents.go point at the SAME cascade.db path, and
// providers/sqlite.Open takes an exclusive per-path flock, so a second,
// independent Open against that path fails (see
// cascadepa_install_shared_store.go's header for the round-1 rework
// finding this fixes).
type installerAdapter struct {
	resolvePaths pathResolver
	clock        runtime.Clock
	shared       *sharedCascadeStore

	fetcher func(baseURL string) plugin.RegistryFetcher // nil -> registryfetch.HTTPFetcher
	// registryURL resolves the configured registry base URL. nil ->
	// errNoRegistryConfigured.
	registryURL func() (string, error)
	// registryPubKey resolves the Ed25519 public key artifact bytes are
	// verified against. nil -> errNoRegistryPubKeyConfigured.
	registryPubKey func() (ed25519.PublicKey, error)

	once    sync.Once
	onceErr error
	store   provider.Store
	db      *sql.DB
	domains *storage.PluginDomainRegistry
	dataDir string
	// closers holds only handles installerAdapter itself uniquely opened
	// (the raw *sql.DB) -- the shared store's lifetime belongs to whoever
	// constructed sharedCascadeStore, never to this adapter.
	closers []io.Closer

	// verifyMu guards verified, the {pluginID@version -> verified bytes}
	// cache closing the TOCTOU window between a fresh-install decide call
	// and its elevated retry (see this file's header).
	verifyMu sync.Mutex
	verified map[string][]byte
}

// newInstallerAdapter builds an installerAdapter over its collaborators.
func newInstallerAdapter(resolvePaths pathResolver, clock runtime.Clock, shared *sharedCascadeStore) *installerAdapter {
	return &installerAdapter{resolvePaths: resolvePaths, clock: clock, shared: shared}
}

// Add implements install.Installer.
func (a *installerAdapter) Add(ctx context.Context, req install.AddRequest) (install.AddResult, error) {
	switch req.Candidate.Source {
	case plugin.CandidateSourceInstalled:
		return a.addInstalled(ctx, req.Candidate)
	case plugin.CandidateSourceRegistry:
		return a.addRegistry(ctx, req)
	default:
		return install.AddResult{}, cascade.Newf(cascade.KindInvalidInput,
			"cascade-pa install: candidate %q has an unrecognized source %q", req.Candidate.PluginID, req.Candidate.Source)
	}
}

// addInstalled reports the real, already-installed metadata record for an
// installed-first candidate. A missing host record is trusted ONLY for a
// RuntimeBuiltin manifest (round-1 CR F1c: any other runtime with no
// record is a fact worth refusing on, not reporting as satisfied).
func (a *installerAdapter) addInstalled(ctx context.Context, c plugin.Candidate) (install.AddResult, error) {
	if err := a.init(ctx); err != nil {
		return install.AddResult{}, err
	}
	_, ok, err := LoadMetadata(ctx, a.store, c.PluginID)
	if err != nil {
		return install.AddResult{}, err
	}
	if ok {
		return install.AddResult{Outcome: install.AddOutcomeAlreadyInstalled}, nil
	}
	if c.Manifest.Runtime == plugin.RuntimeBuiltin {
		return install.AddResult{Outcome: install.AddOutcomeAlreadyInstalled}, nil
	}
	return install.AddResult{}, cascade.Newf(cascade.KindIntegrity,
		"cascade-pa install: resolver named %q already installed but this host has no metadata "+
			"record for it (runtime %q) -- refusing rather than reporting a fabricated already-installed",
		c.PluginID, c.Manifest.Runtime)
}

// addRegistry fetches, verifies and installs a registry-sourced candidate.
func (a *installerAdapter) addRegistry(ctx context.Context, req install.AddRequest) (install.AddResult, error) {
	if err := a.init(ctx); err != nil {
		return install.AddResult{}, err
	}
	entry, err := matchingVersion(req.Candidate)
	if err != nil {
		return install.AddResult{}, err
	}
	artifact, err := a.verifiedArtifactBytes(ctx, req.Candidate.PluginID, entry)
	if err != nil {
		return install.AddResult{}, err
	}

	if !req.Witness.Valid() {
		res, err := AddPlugin(ctx, a.store, artifact, entry.Checksum, artifact, true)
		if err != nil {
			return install.AddResult{}, err
		}
		outcome, err := mapAddOutcome(res.Outcome)
		if err != nil {
			return install.AddResult{}, err
		}
		return install.AddResult{Outcome: outcome}, nil
	}

	// Elevated retry: install the SAME verified buffer (see
	// verifiedArtifactBytes), honouring Sec5.9 on this branch too --
	// ProvisionElevated always writes, so this adapter performs the same
	// already-installed guard AddPlugin performs on the non-elevated
	// branch (round-1 CR fix item 8).
	m, err := plugin.ParseManifest(bytes.NewReader(artifact))
	if err != nil {
		return install.AddResult{}, err
	}
	existing, ok, err := LoadMetadata(ctx, a.store, m.ID)
	if err != nil {
		return install.AddResult{}, err
	}
	if ok && existing.InstalledVersion == m.Version {
		return install.AddResult{Outcome: install.AddOutcomeAlreadyInstalled}, nil
	}
	if _, err := ProvisionElevated(ctx, a.db, migrate.SQLiteEmitter{}, a.clock, "", "", a.store, a.domains,
		m, entry.Checksum, false, nil); err != nil {
		return install.AddResult{}, err
	}
	return install.AddResult{Outcome: install.AddOutcomeInstalled}, nil
}

// matchingVersion finds c.RegistryEntry's published entry for its own
// LatestVersion -- the version Resolve's registry-lookup step ranked
// candidates by (RegistryIndexEntry.LatestVersion's own doc comment).
func matchingVersion(c plugin.Candidate) (plugin.RegistryVersionEntry, error) {
	for _, v := range c.RegistryEntry.Versions {
		if v.Version == c.RegistryEntry.LatestVersion {
			return v, nil
		}
	}
	return plugin.RegistryVersionEntry{}, cascade.Newf(cascade.KindIntegrity,
		"cascade-pa install: registry entry %q names latest_version %q with no matching versions[] entry",
		c.PluginID, c.RegistryEntry.LatestVersion)
}

// mapAddOutcome translates lifecycle_add.go's AddOutcome onto install's own
// distinct type (this package cannot re-export it: install cannot import
// internal/plugins -- R-16.62, R-14.69). An outcome this switch does not
// recognize is refused rather than silently reported as a successful
// install (round-1 CR fix item 7: the prior default was fail-OPEN).
func mapAddOutcome(o AddOutcome) (install.AddOutcome, error) {
	switch o {
	case AddOutcomeInstalled:
		return install.AddOutcomeInstalled, nil
	case AddOutcomeAlreadyInstalled:
		return install.AddOutcomeAlreadyInstalled, nil
	case AddOutcomeElevationRequired:
		return install.AddOutcomeElevationRequired, nil
	default:
		return install.AddOutcome(0), cascade.Newf(cascade.KindInternal,
			"cascade-pa install: lifecycle add returned an unrecognized outcome %d", o)
	}
}

// init lazily opens the real store/db/domains this adapter needs, exactly
// once -- mirroring wirePluginAddHandler's own disclosed "own second
// sqlite connection, left open for the process lifetime" tradeoff. Every
// handle opened here is recorded in closers so Close (below) can release
// it -- required for a test's TempDir cleanup on Windows (round-1 CR fix
// item 11).
func (a *installerAdapter) init(ctx context.Context) error {
	a.once.Do(func() {
		paths, err := a.resolvePaths()
		if err != nil {
			a.onceErr = cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa install: resolve daemon paths")
			return
		}
		a.dataDir = paths.DataDir()
		dbPath := filepath.Join(a.dataDir, "cascade.db")

		store, err := a.shared.open(ctx)
		if err != nil {
			a.onceErr = err
			return
		}
		a.store = store

		db, err := sql.Open("sqlite", "file:"+dbPath)
		if err != nil {
			a.onceErr = cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa install: open cascade.db (raw)")
			return
		}
		db.SetMaxOpenConns(1)
		a.db = db
		a.closers = append(a.closers, db)
		a.domains = storage.NewPluginDomainRegistry()
	})
	return a.onceErr
}

// Close releases every store/db handle init opened. Production leaves this
// unreached (the daemon process owns the adapter's lifetime); every test
// that opens real handles calls it via t.Cleanup (round-1 CR fix item 11).
func (a *installerAdapter) Close() error {
	var errs []error
	for _, c := range a.closers {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// compile-time proof installerAdapter really is the Installer install.Flow
// reads, so a signature drift on either side fails here rather than at the
// wiring site.
var _ install.Installer = (*installerAdapter)(nil)

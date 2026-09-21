package install_test

// Purpose (this file): a test-local install.Installer for the Epic X
//   acceptance story (acceptance_x_test.go) that drives cascade-github's
//   registry-sourced add through the SAME exported, real production
//   lifecycle functions internal/plugins's own installerAdapter calls --
//   plugins.AddPlugin, plugins.ProvisionElevated, plugins.LoadMetadata --
//   never a second, simulated install branch. installerAdapter itself
//   (internal/plugins/cascadepa_install_installer.go) is unexported, so a
//   _test.go file outside that package cannot reach it directly: the
//   plugins-providers-boundary depguard rule exempts _test.go files from
//   the internal/-import restriction (see .golangci.yml), but that is an
//   import-boundary exemption, not a Go-visibility one -- unexported
//   identifiers stay unreachable across packages regardless. This wrapper
//   therefore composes the identical exported calls in the identical
//   order addRegistry/addInstalled already use, so every write this test
//   performs is the real lifecycle engine, not a reimplementation of it.
//   (This file is "package install_test", not "package install" like
//   this directory's other _test.go files -- see acceptance_x_test.go's
//   package doc for why: internal/plugins imports plugins/cascade-pa/install,
//   so an internal test file importing internal/plugins would close an
//   import cycle.)
// Inputs: a real *sql.DB + pkg/provider.Store rooted at t.TempDir(), the
//   real plugins/github/manifest.toml bytes the caller supplies as
//   "artifact" (acceptRealArtifact in acceptance_x_test.go), and the
//   fixture's verified plugin.RegistryIndexEntry the resolver returned.
// Outputs: install.AddResult mapped from the real internal/plugins.
//   AddOutcome the production functions return; calls records every
//   install.AddRequest received, in order, so a test can assert
//   Witness.Valid() without a separate spy (R-14.72: elevation must
//   precede the retried, witness-carrying call).
// Constraints: verifies checksum+signature via the real
//   plugin.Ed25519Verifier BEFORE ever calling plugins.AddPlugin, matching
//   cascadepa_install_installer.go's own ordering exactly; zero network
//   I/O (Art.7 §2); CandidateSourceInstalled is deliberately unimplemented
//   -- no required test exercises it, and every fixture candidate this
//   suite resolves is CandidateSourceRegistry.
// SPORT: plugins/cascade-pa/install:acceptance (ADD) -- P1-E24-W5-S50-T7.

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
	sqlitestore "github.com/acamarata/cascade/providers/sqlite"
)

// acceptRegistryInstaller is this file's install.Installer. See the file
// header for why it exists instead of reusing internal/plugins's own
// unexported installerAdapter.
type acceptRegistryInstaller struct {
	store    provider.Store
	db       *sql.DB
	domains  *storage.PluginDomainRegistry
	clock    migrate.Clock
	verifier plugin.Ed25519Verifier
	artifact []byte
	calls    []install.AddRequest
}

// newAcceptRegistryInstaller opens a real, disposable cascade.db under
// t.TempDir() (Art.7.1: never the operator's real home) and returns an
// Installer that verifies+installs artifact for whatever registry
// candidate a real plugin.Resolver hands it.
func newAcceptRegistryInstaller(t *testing.T, verifier plugin.Ed25519Verifier, artifact []byte) *acceptRegistryInstaller {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	store, err := sqlitestore.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("acceptRegistryInstaller: sqlitestore.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("acceptRegistryInstaller: sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &acceptRegistryInstaller{
		store: store, db: db, domains: storage.NewPluginDomainRegistry(),
		clock: testkit.NewFrozenClock(fixedAcceptTestTime), verifier: verifier, artifact: artifact,
	}
}

// Add implements install.Installer. Every call is recorded before
// dispatch, including one that errors, so a test asserting call counts
// never has to guess at partial-call bookkeeping.
func (a *acceptRegistryInstaller) Add(ctx context.Context, req install.AddRequest) (install.AddResult, error) {
	a.calls = append(a.calls, req)
	if req.Candidate.Source != plugin.CandidateSourceRegistry {
		return install.AddResult{}, cascade.Newf(cascade.KindUnsupported,
			"acceptance installer: candidate source %q not exercised by this suite", req.Candidate.Source)
	}
	return a.addRegistry(ctx, req)
}

// addRegistry mirrors cascadepa_install_installer.go's addRegistry
// method field-for-field: verify (real Ed25519Verifier) before any write,
// AddPlugin for the first (non-elevated) attempt, ParseManifest +
// LoadMetadata + ProvisionElevated for the witnessed retry.
func (a *acceptRegistryInstaller) addRegistry(ctx context.Context, req install.AddRequest) (install.AddResult, error) {
	entry, err := acceptMatchingVersion(req.Candidate)
	if err != nil {
		return install.AddResult{}, err
	}
	if err := a.verifier.VerifyArtifact(ctx, a.artifact, entry); err != nil {
		return install.AddResult{}, err
	}

	if !req.Witness.Valid() {
		res, err := plugins.AddPlugin(ctx, a.store, a.artifact, entry.Checksum, a.artifact, true)
		if err != nil {
			return install.AddResult{}, err
		}
		outcome, err := acceptMapAddOutcome(res.Outcome)
		if err != nil {
			return install.AddResult{}, err
		}
		return install.AddResult{Outcome: outcome}, nil
	}

	m, err := plugin.ParseManifest(bytes.NewReader(a.artifact))
	if err != nil {
		return install.AddResult{}, err
	}
	existing, ok, err := plugins.LoadMetadata(ctx, a.store, m.ID)
	if err != nil {
		return install.AddResult{}, err
	}
	if ok && existing.InstalledVersion == m.Version {
		return install.AddResult{Outcome: install.AddOutcomeAlreadyInstalled}, nil
	}
	if _, err := plugins.ProvisionElevated(ctx, a.db, migrate.SQLiteEmitter{}, a.clock, "", "",
		a.store, a.domains, m, entry.Checksum, false, nil); err != nil {
		return install.AddResult{}, err
	}
	return install.AddResult{Outcome: install.AddOutcomeInstalled}, nil
}

// acceptMatchingVersion mirrors installer.go's matchingVersion.
func acceptMatchingVersion(c plugin.Candidate) (plugin.RegistryVersionEntry, error) {
	for _, v := range c.RegistryEntry.Versions {
		if v.Version == c.RegistryEntry.LatestVersion {
			return v, nil
		}
	}
	return plugin.RegistryVersionEntry{}, cascade.Newf(cascade.KindIntegrity,
		"acceptance installer: registry entry %q names latest_version %q with no matching versions[] entry",
		c.PluginID, c.RegistryEntry.LatestVersion)
}

// acceptMapAddOutcome mirrors installer.go's mapAddOutcome: an outcome
// this switch does not recognize refuses rather than reporting a
// fabricated success.
func acceptMapAddOutcome(o plugins.AddOutcome) (install.AddOutcome, error) {
	switch o {
	case plugins.AddOutcomeInstalled:
		return install.AddOutcomeInstalled, nil
	case plugins.AddOutcomeAlreadyInstalled:
		return install.AddOutcomeAlreadyInstalled, nil
	case plugins.AddOutcomeElevationRequired:
		return install.AddOutcomeElevationRequired, nil
	default:
		return install.AddOutcome(0), cascade.Newf(cascade.KindInternal,
			"acceptance installer: lifecycle add returned an unrecognized outcome %d", o)
	}
}

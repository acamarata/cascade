package conversation

// Purpose (this file): shared, cross-platform collaborators for the five
//   S-44.T5 acceptance test files in this package (acceptance_livemirror,
//   acceptance_scrub, acceptance_privacy, acceptance_pagination,
//   acceptance_journal): a real two-lane providers registry for the
//   privacy acceptance test, built over a real SQLite database via
//   internal/providers/registry (never a fake registry -- that package's
//   own privacy_test.go uses an in-package unexported fakeRegistry this
//   external-to-conductor test cannot reach), and a minimal real
//   conductor.QuotaSpiller call counter (FILTER 4's ordering seam; a
//   trivial deterministic implementation is legitimate here because the
//   thing under acceptance is FILTER 0 -- filterPrivacy -- refusing
//   before FILTER 4 is ever consulted, not the spill order FILTER 4
//   itself decides).
// Inputs: none beyond *testing.T.
// Outputs: a *registry.Reader (a real pkg/provider.ProviderRegistryReader)
//   naming one controller-local and one external-api lane, and an
//   acceptanceQuotaCounter proving "no external lane call is made" by its
//   own call count rather than by a mock's recorded arguments.
// Constraints: no "net"/"net/http" import here -- those stay isolated to
//   acceptance_livemirror_test.go's own "!windows" build tag, so this
//   file (and every other acceptance_*_test.go that imports it) compiles
//   and its tests run on GOOS=windows too.
// SPORT: internal.conversation/acceptance (ADDED, tests-only) (P1-E20-W5-S44-T5).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
)

// acceptanceLocalLaneName / acceptanceExternalLaneName name the two lanes
// newAcceptancePrivacyRegistry seeds. Package-level constants so every
// acceptance test asserting against them names the same literal.
const (
	acceptanceLocalLaneName    = "accept-lane-local"
	acceptanceExternalLaneName = "accept-lane-external"
)

// openAcceptanceRegistryDB opens a real, file-backed modernc-sqlite
// database under t.TempDir() and applies the real providers-registry
// migration -- matching internal/providers/registry's own
// newTestRegistry (migration_test.go), duplicated here because that
// helper is unexported to its own package and this test lives in
// internal/conversation.
func openAcceptanceRegistryDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "acceptance-registry.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open acceptance registry db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() }) // WINDOWS: closed before TempDir cleanup runs (t.Cleanup LIFO).
	if err := registry.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return db
}

// newAcceptancePrivacyRegistry returns a real *registry.Reader (the exact
// adapter internal/conductor's production NewDaemonRouter wraps a real
// *registry.Registry in) naming exactly two lanes: one whose provider
// BaseURL is loopback (conductor.ClassifyLane reads this as
// LaneControllerLocal) and one whose BaseURL names an external host
// (LaneExternalAPI). This is the real registry-and-reader pair a daemon
// composition root would build, not a double of PrivacyAllows/ClassifyLane
// or of Router.Select itself.
func newAcceptancePrivacyRegistry(t *testing.T) *registry.Reader {
	t.Helper()
	db := openAcceptanceRegistryDB(t)
	reg := registry.NewRegistry(db, newTestClock())
	ctx := context.Background()

	// "127.0.0.1" is a literal dotted-decimal loopback address --
	// conductor.computedLocalityIsLocal's real predicate (fixed by the
	// S-44.T2 CR-B finding Q1a to reject a registrable "127.evil.com"
	// domain), never a name that merely starts with "127.".
	local := acceptanceProviderRecord("accept-provider-local", "http://127.0.0.1:9999")
	external := acceptanceProviderRecord("accept-provider-external", "https://api.example.com")

	if err := reg.AddProvider(ctx, local); err != nil {
		t.Fatalf("AddProvider(local): %v", err)
	}
	if err := reg.AddProvider(ctx, external); err != nil {
		t.Fatalf("AddProvider(external): %v", err)
	}
	if err := reg.UpsertLane(ctx, registry.LaneRecord{
		LaneName: acceptanceLocalLaneName, ProviderName: local.Name, Weight: 1,
		Capacity: registry.CapacityInteractiveUsage, State: registry.LaneStateAvailable,
	}); err != nil {
		t.Fatalf("UpsertLane(local): %v", err)
	}
	if err := reg.UpsertLane(ctx, registry.LaneRecord{
		LaneName: acceptanceExternalLaneName, ProviderName: external.Name, Weight: 1,
		Capacity: registry.CapacityInteractiveUsage, State: registry.LaneStateAvailable,
	}); err != nil {
		t.Fatalf("UpsertLane(external): %v", err)
	}
	return registry.NewReader(reg)
}

// newAcceptanceExternalOnlyRegistry returns a real *registry.Reader naming
// ONLY the external-api lane -- no controller-local lane exists at all.
// filterPrivacy only returns ErrSensitivityViolation when EVERY candidate
// is refused (privacy.go: "len(out) == 0 && len(cands) > 0"); a registry
// that also offers a controller-local lane would let a local-only thread
// route to it normally (every mode permits controller-local traffic), so
// the refusal proof needs an external-only registry -- matching
// internal/conductor/privacy_test.go's own externalOnlyRegistry() shape
// exactly, duplicated here since that helper is unexported to its
// package.
func newAcceptanceExternalOnlyRegistry(t *testing.T) *registry.Reader {
	t.Helper()
	db := openAcceptanceRegistryDB(t)
	reg := registry.NewRegistry(db, newTestClock())
	ctx := context.Background()
	external := acceptanceProviderRecord("accept-provider-external-only", "https://api.example.com")
	if err := reg.AddProvider(ctx, external); err != nil {
		t.Fatalf("AddProvider(external-only): %v", err)
	}
	if err := reg.UpsertLane(ctx, registry.LaneRecord{
		LaneName: acceptanceExternalLaneName, ProviderName: external.Name, Weight: 1,
		Capacity: registry.CapacityInteractiveUsage, State: registry.LaneStateAvailable,
	}); err != nil {
		t.Fatalf("UpsertLane(external-only): %v", err)
	}
	return registry.NewReader(reg)
}

// acceptanceProviderRecord is a minimal, real, Validate()-passing
// ProviderRecord -- the same required-field set registry_test.go's own
// sampleProvider names (Driver/Auth/AuthRef/AccountKind/Tier), copied
// here rather than imported since sampleProvider is unexported to its
// own package.
func acceptanceProviderRecord(name, baseURL string) registry.ProviderRecord {
	return registry.ProviderRecord{
		Name:        name,
		Driver:      registry.DriverAnthropic,
		BaseURL:     baseURL,
		Auth:        registry.AuthKey,
		AuthRef:     registry.VaultKeyRef("provider." + name + ".key"),
		KnownModels: []string{"claude-3-5-sonnet-20241022"},
		AccountKind: registry.AccountPersonal,
		Tier:        registry.TierStrongest,
		Capabilities: provider.Capabilities{
			Search:  provider.CapabilitySupported,
			ToolUse: provider.CapabilitySupported,
		},
		HealthStatus: registry.HealthUnknown,
	}
}

// acceptanceQuotaCounter is a real conductor.QuotaSpiller implementation
// (not a mock of Router.Select) whose only behavior is counting calls --
// the AC's own "no external lane call is made" is a call-count assertion,
// so the fake's ENTIRE observable surface is the fact under test, not a
// canned response standing in for FILTER 4's real ordering logic.
type acceptanceQuotaCounter struct {
	calls  int
	answer conductor.LaneID
}

func (q *acceptanceQuotaCounter) NextLane(_ context.Context, _ []conductor.LaneID) (conductor.LaneID, error) {
	q.calls++
	return q.answer, nil
}

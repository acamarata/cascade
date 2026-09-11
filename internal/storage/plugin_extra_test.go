// Purpose: internal/storage/plugin_extra_test.go — the branches
//
//	plugin_test.go's isolation/refusal-focused suite left uncovered:
//	NewPluginStorage's four required-argument refusals, Migrate's
//	delegation to the injected Migrator, the two wrapped error types'
//	Error() strings, and List's real Scan-failure passthrough (a scanning
//	backend, not merely "no panic" — see errScanStore below).
//
// SPORT: internal.storage.PluginStorage/ADDED (P1-E15-W4-S32-T3).
package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestNewPluginStorageRequiresEveryArgument drives all four non-nil-arg
// refusals: each is fail-closed (returns a KindInvalidInput error, never
// a PluginStorage with a nil-backed field a later call would panic on).
func TestNewPluginStorageRequiresEveryArgument(t *testing.T) {
	store := storetest.NewMemStore()
	grants := &fakeGrants{grants: map[string]error{}}
	scanner := fakeScanner{}
	migrator := noopMigrator{}

	cases := []struct {
		name       string
		store      provider.Store
		grants     storage.GrantChecker
		scanner    storage.SecretScanner
		migrator   storage.Migrator
		wantSubstr string
	}{
		{"nil-store", nil, grants, scanner, migrator, "Store"},
		{"nil-grants", store, nil, scanner, migrator, "GrantChecker"},
		{"nil-scanner", store, grants, nil, migrator, "SecretScanner"},
		{"nil-migrator", store, grants, scanner, nil, "Migrator"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps, err := storage.NewPluginStorage("widget-plugin", tc.store, tc.grants, tc.scanner, tc.migrator)
			if err == nil {
				t.Fatalf("NewPluginStorage(%s) = (%v, nil), want a KindInvalidInput refusal", tc.name, ps)
			}
			if ps != nil {
				t.Fatalf("NewPluginStorage(%s) returned a non-nil *PluginStorage alongside an error", tc.name)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Fatalf("NewPluginStorage(%s) kind = %v, want KindInvalidInput", tc.name, kind)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("NewPluginStorage(%s) error = %q, want it to mention %q", tc.name, err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestPluginStorageMigrateDelegates proves Migrate is real wiring, not
// dead code: it forwards to the injected Migrator and returns exactly
// what the Migrator returns, for both the plugin id and the migrations
// slice.
func TestPluginStorageMigrateDelegates(t *testing.T) {
	ctx := context.Background()
	recorder := &recordingMigrator{report: plugin.MigrationReport{AppliedVersions: []int{1}, CurrentVersion: 1}}
	ps, err := storage.NewPluginStorage("widget-plugin", storetest.NewMemStore(),
		&fakeGrants{grants: map[string]error{}}, fakeScanner{}, recorder)
	if err != nil {
		t.Fatalf("NewPluginStorage: %v", err)
	}

	migs := []plugin.Migration{{Version: 1, Steps: []plugin.MigrationStep{{Table: plugin.TableDef{
		Name:    "widgets",
		Columns: []plugin.ColumnDef{{Name: "id", Type: plugin.ColumnInteger, PrimaryKey: true}},
	}}}}}
	report, err := ps.Migrate(ctx, migs)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if report.CurrentVersion != 1 || len(report.AppliedVersions) != 1 || report.AppliedVersions[0] != 1 {
		t.Fatalf("Migrate report = %+v, want the recorder's exact report", report)
	}
	if recorder.gotPluginID != "widget-plugin" {
		t.Fatalf("Migrate delegated pluginID = %q, want %q", recorder.gotPluginID, "widget-plugin")
	}
	if len(recorder.gotMigrations) != 1 || recorder.gotMigrations[0].Version != 1 {
		t.Fatalf("Migrate delegated migrations = %v, want the same slice passed in", recorder.gotMigrations)
	}
}

// recordingMigrator is a Migrator double that records its call so
// TestPluginStorageMigrateDelegates can assert Migrate is real
// pass-through wiring, not merely "returns without panicking".
type recordingMigrator struct {
	report        plugin.MigrationReport
	gotPluginID   string
	gotMigrations []plugin.Migration
}

func (r *recordingMigrator) Apply(_ context.Context, pluginID string, migrations []plugin.Migration) (plugin.MigrationReport, error) {
	r.gotPluginID = pluginID
	r.gotMigrations = migrations
	return r.report, nil
}

// TestPluginStorageErrorStrings proves both wrapped error types' Error()
// carries the diagnostic fields callers rely on for log lines, not just
// that errors.As can find them (already covered by plugin_test.go).
func TestPluginStorageErrorStrings(t *testing.T) {
	ctx := context.Background()
	ps := newTestStorage(t, "widget-plugin", &fakeGrants{grants: map[string]error{}})

	_, err := ps.CrossDomainGet(ctx, "other-plugin", "k")
	var denied *storage.PluginStoragePermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("CrossDomainGet without a grant: error is %T, want *PluginStoragePermissionDeniedError", err)
	}
	if !strings.Contains(denied.Error(), "widget-plugin") || !strings.Contains(denied.Error(), "other-plugin") {
		t.Fatalf("PermissionDeniedError.Error() = %q, want it to name both plugin=widget-plugin and target=other-plugin", denied.Error())
	}

	serr := ps.Set(ctx, "creds", []byte("aws_key="+credentialShapedMarker))
	var sensitive *storage.PluginStorageSensitivePayloadError
	if !errors.As(serr, &sensitive) {
		t.Fatalf("Set(credential-shaped value): error is %T, want *PluginStorageSensitivePayloadError", serr)
	}
	if !strings.Contains(sensitive.Error(), "widget-plugin") || !strings.Contains(sensitive.Error(), "creds") {
		t.Fatalf("SensitivePayloadError.Error() = %q, want it to name plugin=widget-plugin and key=creds", sensitive.Error())
	}
}

// errScanStore is a real provider.Store whose Scan always fails, proving
// List propagates a genuine backend failure rather than swallowing it —
// distinct from storetest.MemStore, whose Scan can never error.
type errScanStore struct{ *storetest.MemStore }

func newErrScanStore() errScanStore { return errScanStore{storetest.NewMemStore()} }

func (errScanStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, cascade.New(cascade.KindUnavailable, "storage: scan backend unavailable")
}

func TestPluginStorageListPropagatesScanError(t *testing.T) {
	ctx := context.Background()
	ps, err := storage.NewPluginStorage("widget-plugin", newErrScanStore(),
		&fakeGrants{grants: map[string]error{}}, fakeScanner{}, noopMigrator{})
	if err != nil {
		t.Fatalf("NewPluginStorage: %v", err)
	}
	keys, err := ps.List(ctx, "")
	if err == nil {
		t.Fatal("List over a failing Scan backend = nil error, want the backend's failure")
	}
	if keys != nil {
		t.Fatalf("List over a failing Scan backend returned keys = %v, want nil", keys)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("List error kind = %v, want KindUnavailable (the backend's own kind, unwrapped)", kind)
	}
}

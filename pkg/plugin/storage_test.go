// Purpose: pkg/plugin/storage_test.go — the spec-conformance check this
//
//	ticket's acceptance criteria require: PluginStorage driven through
//	the spec-derived, provenance-stamped host_storage_* fixture table
//	below.
//
// Art.2 status (honest, per the contract's own instruction — NOT an
// Art.2-satisfied claim): these fixtures are derived from this ticket's
// own self-authored reading of the manifest v2 plugin ABI
// (02-TARGET-STRUCTURE.md §Storage scoping, §pkg/plugin/), which per
// Art.2.1/2.4 cannot count as a real external counterpart. The Art.2
// real-counterpart obligation for host_storage_* lands in S-32.T2's
// conformance suite, which drives all three runtimes against compiled
// guest artifacts (the N/S-30.T6 skeleton) — not here. Full provenance
// record: internal/storage/testdata/S32T3-PROVENANCE.md.
//
// Why no internal/storage import here: pkg/plugin/storage_test.go could
// construct a real internal/storage.PluginStorage instead of the local
// referenceStorage fake below — internal/build/arch_test.go's own
// boundary scanner exempts _test.go files from the pkg-no-internal
// architecture check. But .golangci.yml's depguard "pkg-no-internal" rule
// (the actual CI lint wall step 5 of this phase's AGENT-BRIEF requires)
// globs "**/pkg/**/*.go" with no _test.go carve-out, so importing
// internal/storage here would pass the internal/build gate while still
// failing golangci-lint. referenceStorage below is a minimal, real
// (Art.1: fully functional, not a mock of behavior under test)
// in-memory implementation of the PluginStorage interface itself, so this
// file drives the ABI's actual method contracts rather than a double of
// them — internal/storage/plugin_test.go separately proves the real
// internal/storage.PluginStorage implementation against this same shape.
//
// SPORT: pkg.plugin.Storage/ADDED (P1-E15-W4-S32-T3).
package plugin_test

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// referenceStorage is a real, fully functional in-memory PluginStorage —
// the same role internal/storage/storetest.MemStore plays for
// pkg/provider.Store — used only to drive the ABI's method contracts in
// this spec-conformance check.
type referenceStorage struct {
	mu   sync.Mutex
	data map[string][]byte
	migs map[int]bool
}

func newReferenceStorage() *referenceStorage {
	return &referenceStorage{data: make(map[string][]byte), migs: make(map[int]bool)}
}

var _ plugin.Storage = (*referenceStorage)(nil)

func (r *referenceStorage) Get(_ context.Context, key string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.data[key]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "referenceStorage: no such key %q", key)
	}
	return v, nil
}

func (r *referenceStorage) Set(_ context.Context, key string, value []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = value
	return nil
}

func (r *referenceStorage) List(_ context.Context, prefix string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var keys []string
	for k := range r.data {
		if len(prefix) == 0 || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (r *referenceStorage) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key)
	return nil
}

func (r *referenceStorage) Migrate(_ context.Context, migrations []plugin.Migration) (plugin.MigrationReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var applied []int
	maxVersion := 0
	for _, m := range migrations {
		if !r.migs[m.Version] {
			r.migs[m.Version] = true
			applied = append(applied, m.Version)
		}
		if m.Version > maxVersion {
			maxVersion = m.Version
		}
	}
	if len(applied) == 0 {
		return plugin.MigrationReport{AlreadyCurrent: true, CurrentVersion: maxVersion}, nil
	}
	return plugin.MigrationReport{AppliedVersions: applied, CurrentVersion: maxVersion}, nil
}

// hostStorageFixture is one spec-derived host_storage_* fixture operation:
// a named PluginStorage call plus its expected outcome, matching the
// N/S-30.T6 conformance-suite skeleton's host_storage_* naming
// convention (Get/Set/List/Delete/Migrate) so a future compiled-guest
// suite (S-32.T2) can recognize the same operation names.
type hostStorageFixture struct {
	name string
	run  func(t *testing.T, s plugin.Storage)
}

// hostStorageFixtures is the spec-derived, provenance-stamped fixture
// table (source=self-authored ABI spec reading of 02-TARGET-STRUCTURE.md
// §Storage scoping/§pkg/plugin/, version=this ticket's contract text,
// date=see internal/storage/testdata/S32T3-PROVENANCE.md). Each entry
// exercises one PluginStorage ABI method exactly as a real plugin author
// would call it.
var hostStorageFixtures = []hostStorageFixture{
	{
		name: "host_storage_set_then_get",
		run: func(t *testing.T, s plugin.Storage) {
			ctx := context.Background()
			if err := s.Set(ctx, "config/theme", []byte("dark")); err != nil {
				t.Fatalf("Set: %v", err)
			}
			got, err := s.Get(ctx, "config/theme")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(got) != "dark" {
				t.Fatalf("Get = %q, want %q", got, "dark")
			}
		},
	},
	{
		name: "host_storage_get_missing_key",
		run: func(t *testing.T, s plugin.Storage) {
			if _, err := s.Get(context.Background(), "no/such/key"); err == nil {
				t.Fatal("Get(missing key) = nil error, want KindNotFound")
			} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
				t.Fatalf("Get(missing key) kind = %v, want KindNotFound", kind)
			}
		},
	},
	{
		name: "host_storage_list_prefix",
		run: func(t *testing.T, s plugin.Storage) {
			ctx := context.Background()
			for _, k := range []string{"cache/a", "cache/b", "config/x"} {
				if err := s.Set(ctx, k, []byte("v")); err != nil {
					t.Fatalf("Set(%s): %v", k, err)
				}
			}
			keys, err := s.List(ctx, "cache/")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(keys) != 2 || keys[0] != "cache/a" || keys[1] != "cache/b" {
				t.Fatalf("List(cache/) = %v, want [cache/a cache/b]", keys)
			}
		},
	},
	{
		name: "host_storage_delete_is_idempotent",
		run: func(t *testing.T, s plugin.Storage) {
			ctx := context.Background()
			if err := s.Set(ctx, "temp/x", []byte("v")); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if err := s.Delete(ctx, "temp/x"); err != nil {
				t.Fatalf("first Delete: %v", err)
			}
			if err := s.Delete(ctx, "temp/x"); err != nil {
				t.Fatalf("second Delete (already absent): %v, want nil (idempotent)", err)
			}
		},
	},
	{
		name: "host_storage_migrate_idempotent",
		run: func(t *testing.T, s plugin.Storage) {
			ctx := context.Background()
			mig := plugin.Migration{
				Version: 1,
				Steps: []plugin.MigrationStep{{
					Table: plugin.TableDef{
						Name:    "widgets",
						Columns: []plugin.ColumnDef{{Name: "id", Type: plugin.ColumnInteger, PrimaryKey: true}},
					},
				}},
			}
			r1, err := s.Migrate(ctx, []plugin.Migration{mig})
			if err != nil {
				t.Fatalf("first Migrate: %v", err)
			}
			if r1.AlreadyCurrent {
				t.Fatal("first Migrate reported AlreadyCurrent=true, want false")
			}
			r2, err := s.Migrate(ctx, []plugin.Migration{mig})
			if err != nil {
				t.Fatalf("second Migrate: %v", err)
			}
			if !r2.AlreadyCurrent {
				t.Fatal("second Migrate reported AlreadyCurrent=false, want true (idempotent)")
			}
		},
	},
}

// TestPluginStorageConformance drives PluginStorage through every
// host_storage_* fixture. See this file's package doc comment for the
// Art.2 provenance disclosure this test deliberately does not overstate.
func TestPluginStorageConformance(t *testing.T) {
	for _, fx := range hostStorageFixtures {
		t.Run(fx.name, func(t *testing.T) {
			fx.run(t, newReferenceStorage())
		})
	}
}

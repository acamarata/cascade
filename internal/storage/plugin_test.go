// Purpose: internal/storage/plugin_test.go — the isolation and refusal
//
//	tests P1-E15-W4-S32-T3's acceptance criteria require: the cross-domain
//	gate's three branches (grant-present, grant-absent, grant-expired),
//	the sensitive-payload guard's trigger and clean paths, and
//	PluginDomainRegistry's single-writer and idempotent-reinstall
//	behavior.
//
// External package (storage_test, not storage): plugin.go's production
// code cannot import internal/policy or internal/secrets directly (import
// cycle, see plugin.go's own package doc comment) — but this file, as an
// external test package, is free to declare its own realistic fakes over
// GrantChecker/SecretScanner without needing either import, matching the
// composition-root adapter shape a future ticket will build.
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
)

// fakeGrants is a minimal GrantChecker double: "subject/capability" maps
// to nil (allow) or a non-nil error (deny), matching CheckGrant's shape.
type fakeGrants struct {
	grants map[string]error
}

func (f *fakeGrants) CheckGrant(_ context.Context, subjectID, capability string) error {
	key := subjectID + "/" + capability
	err, ok := f.grants[key]
	if !ok {
		return errors.New("fake: no grant recorded") // absent
	}
	return err // nil (present) or the recorded denial (e.g. expired)
}

// fakeScanner flags values containing the marker below (stands in for a
// real *secrets.Detector hit).
type fakeScanner struct{}

// credentialShapedMarker avoids a contiguous credential-shaped literal in
// source (GitHub push protection blocks those even in test fixtures).
const credentialShapedMarker = "AKIA" + "7YQ2XPLM4RZV6WTB"

func (fakeScanner) HasSecret(content []byte) bool {
	return len(content) > 0 && strings.Contains(string(content), credentialShapedMarker)
}

// noopMigrator satisfies storage.Migrator (plugin_migrate_test.go covers
// the real migration path).
type noopMigrator struct{}

func (noopMigrator) Apply(context.Context, string, []plugin.Migration) (plugin.MigrationReport, error) {
	return plugin.MigrationReport{AlreadyCurrent: true}, nil
}

func newTestStorage(t *testing.T, pluginID string, grants *fakeGrants) *storage.PluginStorage {
	t.Helper()
	ps, err := storage.NewPluginStorage(pluginID, storetest.NewMemStore(), grants, fakeScanner{}, noopMigrator{})
	if err != nil {
		t.Fatalf("NewPluginStorage(%q): %v", pluginID, err)
	}
	return ps
}

func TestPluginStorageGetSetListDelete(t *testing.T) {
	ctx := context.Background()
	ps := newTestStorage(t, "widget-plugin", &fakeGrants{grants: map[string]error{}})

	if err := ps.Set(ctx, "alpha", []byte("hello")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ps.Set(ctx, "beta", []byte("world")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := ps.Get(ctx, "alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Get = %q, want %q", got, "hello")
	}

	keys, err := ps.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 || keys[0] != "alpha" || keys[1] != "beta" {
		t.Fatalf("List = %v, want [alpha beta]", keys)
	}

	if err := ps.Delete(ctx, "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := ps.Get(ctx, "alpha"); err == nil {
		t.Fatal("Get after Delete = nil error, want KindNotFound")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("Get after Delete kind = %v, want KindNotFound", kind)
	}
}

// TestPluginStorageIsolation proves A cannot read B's data via its own
// namespace, even by guessing B's key.
func TestPluginStorageIsolation(t *testing.T) {
	ctx := context.Background()
	shared := storetest.NewMemStore()

	grants := &fakeGrants{grants: map[string]error{}}
	a, err := storage.NewPluginStorage("plugin-a", shared, grants, fakeScanner{}, noopMigrator{})
	if err != nil {
		t.Fatalf("NewPluginStorage(a): %v", err)
	}
	b, err := storage.NewPluginStorage("plugin-b", shared, grants, fakeScanner{}, noopMigrator{})
	if err != nil {
		t.Fatalf("NewPluginStorage(b): %v", err)
	}

	if err := b.Set(ctx, "secret", []byte("b's data")); err != nil {
		t.Fatalf("b.Set: %v", err)
	}

	if _, err := a.Get(ctx, "secret"); err == nil {
		t.Fatal("a.Get(b's key) = nil error, want KindNotFound (a's own namespace has no such key)")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("a.Get(b's key) kind = %v, want KindNotFound", kind)
	}

	keys, err := a.List(ctx, "")
	if err != nil {
		t.Fatalf("a.List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("a.List = %v, want empty (b's keys must never leak into a's namespace)", keys)
	}
}

// newRequester builds a "requester-plugin" PluginStorage over shared
// (factored out to keep TestPluginStorageCrossDomain under funlen).
func newRequester(t *testing.T, shared *storetest.MemStore, grants map[string]error) *storage.PluginStorage {
	t.Helper()
	requester, err := storage.NewPluginStorage("requester-plugin", shared, &fakeGrants{grants: grants}, fakeScanner{}, noopMigrator{})
	if err != nil {
		t.Fatalf("NewPluginStorage(requester): %v", err)
	}
	return requester
}

func TestPluginStorageCrossDomain(t *testing.T) {
	ctx := context.Background()
	shared := storetest.NewMemStore()

	target, err := storage.NewPluginStorage("target-plugin", shared, &fakeGrants{grants: map[string]error{}}, fakeScanner{}, noopMigrator{})
	if err != nil {
		t.Fatalf("NewPluginStorage(target): %v", err)
	}
	if err := target.Set(ctx, "shared-key", []byte("target's value")); err != nil {
		t.Fatalf("target.Set: %v", err)
	}

	t.Run("grant-present", func(t *testing.T) {
		requester := newRequester(t, shared, map[string]error{
			"requester-plugin/plugin.cross_domain.target-plugin": nil,
		})
		got, err := requester.CrossDomainGet(ctx, "target-plugin", "shared-key")
		if err != nil {
			t.Fatalf("CrossDomainGet with a present grant: %v", err)
		}
		if string(got) != "target's value" {
			t.Fatalf("CrossDomainGet = %q, want %q", got, "target's value")
		}
	})

	t.Run("grant-absent", func(t *testing.T) {
		requester := newRequester(t, shared, map[string]error{}) // nothing recorded
		_, err := requester.CrossDomainGet(ctx, "target-plugin", "shared-key")
		assertPermissionDenied(t, err)
	})

	t.Run("grant-expired", func(t *testing.T) {
		requester := newRequester(t, shared, map[string]error{
			"requester-plugin/plugin.cross_domain.target-plugin": errors.New("fake: grant expired"),
		})
		_, err := requester.CrossDomainGet(ctx, "target-plugin", "shared-key")
		assertPermissionDenied(t, err)
	})

	t.Run("malformed-scope", func(t *testing.T) {
		requester := newRequester(t, shared, map[string]error{})
		// "Not-A-Valid-Id" fails pluginIDPattern (uppercase not permitted):
		// refused before the grant map is ever consulted.
		_, err := requester.CrossDomainGet(ctx, "Not-A-Valid-Id", "shared-key")
		assertPermissionDenied(t, err)
	})
}

func assertPermissionDenied(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("got nil error, want a typed PermissionDenied refusal")
	}
	var denied *storage.PluginStoragePermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("error is %T, want *storage.PluginStoragePermissionDeniedError", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Fatalf("kind = %v, want KindPermissionDenied", kind)
	}
}

// TestPluginStorageSensitivePayload covers trigger and clean paths.
func TestPluginStorageSensitivePayload(t *testing.T) {
	ctx := context.Background()
	ps := newTestStorage(t, "widget-plugin", &fakeGrants{grants: map[string]error{}})

	t.Run("trigger", func(t *testing.T) {
		err := ps.Set(ctx, "creds", []byte("aws_key="+credentialShapedMarker))
		if err == nil {
			t.Fatal("Set(credential-shaped value) = nil error, want *PluginStorageSensitivePayloadError")
		}
		var sensitive *storage.PluginStorageSensitivePayloadError
		if !errors.As(err, &sensitive) {
			t.Fatalf("error is %T, want *storage.PluginStorageSensitivePayloadError", err)
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
			t.Fatalf("kind = %v, want KindPolicyDenied", kind)
		}
		if _, gerr := ps.Get(ctx, "creds"); gerr == nil {
			t.Fatal("value was persisted despite the sensitive-payload refusal")
		} else if kind, ok := cascade.KindOf(gerr); !ok || kind != cascade.KindNotFound {
			t.Fatalf("Get(creds) kind = %v, want KindNotFound (never stored)", kind)
		}
	})

	t.Run("clean", func(t *testing.T) {
		if err := ps.Set(ctx, "plain", []byte("just an ordinary value")); err != nil {
			t.Fatalf("Set(clean value): %v", err)
		}
	})
}

func TestPluginDomainRegistry(t *testing.T) {
	reg := storage.NewPluginDomainRegistry()

	created, err := reg.Register("widget-plugin", "owner-a", 1)
	if err != nil || !created {
		t.Fatalf("first Register: created=%v err=%v, want true, nil", created, err)
	}

	t.Run("idempotent-reinstall", func(t *testing.T) {
		created, err := reg.Register("widget-plugin", "owner-a", 1)
		if err != nil {
			t.Fatalf("re-Register same owner/version: %v", err)
		}
		if created {
			t.Fatal("re-Register same owner/version reported created=true, want false (already current)")
		}
	})

	t.Run("second-writer-refused", func(t *testing.T) {
		_, err := reg.Register("widget-plugin", "owner-b", 1)
		if !errors.Is(err, storage.ErrPluginDomainOwned) {
			t.Fatalf("Register from a different owner = %v, want ErrPluginDomainOwned", err)
		}
	})

	t.Run("version-upgrade-same-owner", func(t *testing.T) {
		created, err := reg.Register("widget-plugin", "owner-a", 2)
		if err != nil || !created {
			t.Fatalf("upgrade Register: created=%v err=%v, want true, nil", created, err)
		}
		v, ok := reg.Version("widget-plugin")
		if !ok || v != 2 {
			t.Fatalf("Version after upgrade = (%d,%v), want (2,true)", v, ok)
		}
	})

	t.Run("malformed-id-refused", func(t *testing.T) {
		if _, err := reg.Register("Not Valid", "owner-a", 1); err == nil {
			t.Fatal("Register(malformed id) = nil error, want KindInvalidInput")
		}
	})
}

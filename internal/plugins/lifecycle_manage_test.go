package plugins

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingTeardown proves RemovePlugin actually calls Teardown, rather
// than merely being wired to a type that could.
type recordingTeardown struct {
	called bool
	name   string
	err    error
}

func (r *recordingTeardown) Teardown(_ context.Context, name string) error {
	r.called = true
	r.name = name
	return r.err
}

func TestSetEnabled_ToggleAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec, err := SetEnabled(ctx, store, "demo", false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if rec.Enabled {
		t.Fatal("SetEnabled(false) returned Enabled=true")
	}
	stored, _, _ := LoadMetadata(ctx, store, "demo")
	if stored.Enabled {
		t.Fatal("disable did not persist")
	}

	// Idempotent: disabling an already-disabled record is a no-op, not an error.
	rec2, err := SetEnabled(ctx, store, "demo", false)
	if err != nil || rec2.Enabled {
		t.Fatalf("idempotent disable: rec=%+v err=%v", rec2, err)
	}

	rec3, err := SetEnabled(ctx, store, "demo", true)
	if err != nil || !rec3.Enabled {
		t.Fatalf("re-enable: rec=%+v err=%v", rec3, err)
	}
}

func TestSetEnabled_UnknownPluginRefused(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if _, err := SetEnabled(ctx, store, "ghost", true); err == nil {
		t.Fatal("SetEnabled on an unknown plugin succeeded, want KindNotFound")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

func TestRemovePlugin_RunsTeardownAndDeletes(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo", InstalledVersion: "1.0.0"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	td := &recordingTeardown{}
	rec, err := RemovePlugin(ctx, store, td, "demo")
	if err != nil {
		t.Fatalf("RemovePlugin: %v", err)
	}
	if !td.called || td.name != "demo" {
		t.Fatalf("Teardown was not driven with the real plugin name: called=%v name=%q", td.called, td.name)
	}
	if rec.InstalledVersion != "1.0.0" {
		t.Fatalf("RemovePlugin returned %+v, want the removed record's prior state", rec)
	}
	if _, ok, _ := LoadMetadata(ctx, store, "demo"); ok {
		t.Fatal("metadata record still present after RemovePlugin")
	}
}

func TestRemovePlugin_TeardownFailureAbortsRemoval(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	td := &recordingTeardown{err: errors.New("uninstall hook: exit 1")}

	if _, err := RemovePlugin(ctx, store, td, "demo"); err == nil {
		t.Fatal("RemovePlugin with a failing teardown succeeded, want the teardown error")
	}
	if _, ok, _ := LoadMetadata(ctx, store, "demo"); !ok {
		t.Fatal("metadata record was deleted despite a teardown failure")
	}
}

func TestRemovePlugin_NilTeardownIsNotAnError(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := RemovePlugin(ctx, store, nil, "demo"); err != nil {
		t.Fatalf("RemovePlugin with nil teardown: %v", err)
	}
}

func TestRemovePlugin_UnknownPluginRefused(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if _, err := RemovePlugin(ctx, store, nil, "ghost"); err == nil {
		t.Fatal("RemovePlugin on an unknown plugin succeeded, want KindNotFound")
	}
}

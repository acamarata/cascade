// Purpose: tests for FileCache, the production RegistryCache: atomic
//
//	write, get-miss, get-hit, and key-validation refusal paths.
//
// SPORT: pkg/plugin registry-client tests (ADD) — P1-E24-W5-S50-T1.
package plugin_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func TestFileCache_GetMiss(t *testing.T) {
	c := plugin.FileCache{Dir: t.TempDir()}
	_, ok, err := c.Get(context.Background(), "index")
	if err != nil || ok {
		t.Fatalf("Get(missing) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestFileCache_PutThenGet(t *testing.T) {
	c := plugin.FileCache{Dir: t.TempDir()}
	ctx := context.Background()

	if err := c.Put(ctx, "index", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	data, ok, err := c.Get(ctx, "index")
	if err != nil || !ok {
		t.Fatalf("Get after Put = ok=%v err=%v", ok, err)
	}
	if string(data) != `{"a":1}` {
		t.Fatalf("Get after Put = %q", data)
	}

	// No stray .tmp file should survive a successful Put (proves the
	// rename, not a copy, landed the final file).
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("stray tmp file left behind: %s", e.Name())
		}
	}
}

func TestFileCache_PutOverwrites(t *testing.T) {
	c := plugin.FileCache{Dir: t.TempDir()}
	ctx := context.Background()

	if err := c.Put(ctx, "index", []byte("v1")); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := c.Put(ctx, "index", []byte("v2")); err != nil {
		t.Fatalf("Put v2: %v", err)
	}
	data, ok, err := c.Get(ctx, "index")
	if err != nil || !ok || string(data) != "v2" {
		t.Fatalf("Get after overwrite = %q ok=%v err=%v, want v2", data, ok, err)
	}
}

func TestFileCache_InvalidKeyRefused(t *testing.T) {
	c := plugin.FileCache{Dir: t.TempDir()}
	ctx := context.Background()

	for _, key := range []string{"", "../escape", "a/b", `a\b`} {
		if err := c.Put(ctx, key, []byte("x")); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Put(%q) error = %v, want KindInvalidInput", key, err)
		}
		if _, _, err := c.Get(ctx, key); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Get(%q) error = %v, want KindInvalidInput", key, err)
		}
	}
}

func TestFileCache_GetUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// A directory in place of the expected file makes os.ReadFile fail
	// with something other than IsNotExist, exercising the KindUnavailable
	// wrap path.
	if err := os.Mkdir(filepath.Join(dir, "index.json"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	c := plugin.FileCache{Dir: dir}
	_, _, err := c.Get(context.Background(), "index")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Get on directory-as-file error = %v, want KindUnavailable", err)
	}
}

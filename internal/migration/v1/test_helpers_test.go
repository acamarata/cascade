package v1

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

var testInstant = time.Date(2026, 9, 12, 19, 28, 30, 0, time.UTC)

type testClock struct{}

func (testClock) Now() time.Time { return testInstant }

type mapCustody struct {
	values      map[string][]byte
	failSetName string
	failOnce    bool
}

func newMapCustody() *mapCustody      { return &mapCustody{values: map[string][]byte{}} }
func (c *mapCustody) Name() string    { return "test-memory" }
func (c *mapCustody) Available() bool { return true }

func (c *mapCustody) Set(_ context.Context, name string, value []byte) error {
	if name == c.failSetName && c.failOnce {
		c.failOnce = false
		return cascade.New(cascade.KindUnavailable, "test custody refused set")
	}
	c.values[name] = append([]byte(nil), value...)
	return nil
}

func (c *mapCustody) Get(_ context.Context, name string) ([]byte, error) {
	value, ok := c.values[name]
	if !ok {
		return nil, secrets.ErrSecretNotFound(name)
	}
	return append([]byte(nil), value...), nil
}

func (c *mapCustody) Delete(_ context.Context, name string) error {
	if _, ok := c.values[name]; !ok {
		return secrets.ErrSecretNotFound(name)
	}
	delete(c.values, name)
	return nil
}

func (c *mapCustody) List(context.Context) ([]string, error) {
	names := make([]string, 0, len(c.values))
	for name := range c.values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func testBroker(t *testing.T, custody secrets.Custody) *secrets.Broker {
	t.Helper()
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func testRegistry(t *testing.T) (*registry.Registry, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "providers.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := registry.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, testClock{}, "", ""); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return registry.NewRegistry(db, testClock{}), db
}

func stageFixture(t *testing.T, fixture, rootRelative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", fixture))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	target := filepath.Join(root, filepath.FromSlash(rootRelative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, testInstant, testInstant); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSource(t *testing.T, root, relative string, data []byte) string {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, testInstant, testInstant); err != nil {
		t.Fatal(err)
	}
	return target
}

func assertKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != want {
		t.Fatalf("error kind = (%v, %v), want %v; err=%v", got, ok, want, err)
	}
}

func assertSentinel(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v does not wrap %v", err, want)
	}
}

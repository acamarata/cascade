package secrets

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// memCustody is an in-memory Custody for broker tests. It is a test double
// for the STORE, not for the broker logic under test, and every method
// behaves exactly as the real backends do (validated names, not-found
// refusals, sorted name lists).
type memCustody struct {
	entries map[string][]byte
	failOn  string
	err     error
}

func newMemCustody() *memCustody { return &memCustody{entries: map[string][]byte{}} }

func (m *memCustody) Name() string    { return "memory" }
func (m *memCustody) Available() bool { return true }

func (m *memCustody) fail(op string) error {
	if m.failOn == op {
		return m.err
	}
	return nil
}

func (m *memCustody) Set(_ context.Context, name string, value []byte) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	if err := m.fail("set"); err != nil {
		return err
	}
	m.entries[name] = append([]byte(nil), value...)
	return nil
}

func (m *memCustody) Get(_ context.Context, name string) ([]byte, error) {
	if err := m.fail("get"); err != nil {
		return nil, err
	}
	v, ok := m.entries[name]
	if !ok {
		return nil, ErrSecretNotFound(name)
	}
	return v, nil
}

func (m *memCustody) Delete(_ context.Context, name string) error {
	if err := m.fail("delete"); err != nil {
		return err
	}
	if _, ok := m.entries[name]; !ok {
		return ErrSecretNotFound(name)
	}
	delete(m.entries, name)
	return nil
}

func (m *memCustody) List(_ context.Context) ([]string, error) {
	if err := m.fail("list"); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	return sortedNames(names), nil
}

// allowGate authorises everything, standing in for a satisfied elevation
// session.
type allowGate struct{ seen []string }

func (g *allowGate) Authorize(_ context.Context, verb string) error {
	g.seen = append(g.seen, verb)
	return nil
}

// denyGate refuses everything, standing in for a session with no
// attestation.
type denyGate struct{}

func (denyGate) Authorize(_ context.Context, verb string) error {
	return cascade.Newf(cascade.KindElevationRequired, "ELEVATION_REQUIRED for %s", verb)
}

func newTestBroker(t *testing.T, gate ElevationGate) (*Broker, *memCustody) {
	t.Helper()
	custody := newMemCustody()
	b, err := NewBroker(custody, gate)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	return b, custody
}

func TestNewBrokerRefusesNilCustody(t *testing.T) {
	if _, err := NewBroker(nil, &allowGate{}); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("a nil custody was accepted: %v", err)
	}
	b, _ := newTestBroker(t, &allowGate{})
	if b.Backend() != "memory" {
		t.Fatalf("Backend() = %q", b.Backend())
	}
}

func TestBrokerGetRequiresElevation(t *testing.T) {
	b, custody := newTestBroker(t, denyGate{})
	ctx := context.Background()
	custody.entries["TOKEN"] = []byte("s3cr3t")
	_, err := b.Get(ctx, "TOKEN")
	if !isKind(err, cascade.KindElevationRequired) {
		t.Fatalf("Get without elevation = %v, want ELEVATION_REQUIRED", err)
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatal("the refusal echoed the secret value")
	}
}

func TestBrokerGetWithElevation(t *testing.T) {
	gate := &allowGate{}
	b, custody := newTestBroker(t, gate)
	ctx := context.Background()
	custody.entries["TOKEN"] = []byte("s3cr3t")
	got, err := b.Get(ctx, "TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("s3cr3t")) {
		t.Fatal("Get returned the wrong value")
	}
	if len(gate.seen) != 1 || gate.seen[0] != VerbGet {
		t.Fatalf("gate saw %v, want [%s]", gate.seen, VerbGet)
	}
}

// TestBrokerNilGateRefusesElevatedVerbs is the fail-closed case: a broker
// wired without an elevation gate must refuse, never treat "no gate" as
// "no authorisation needed".
func TestBrokerNilGateRefusesElevatedVerbs(t *testing.T) {
	b, custody := newTestBroker(t, nil)
	ctx := context.Background()
	custody.entries["TOKEN"] = []byte("s3cr3t")
	if _, err := b.Get(ctx, "TOKEN"); !isKindOneOf(err, cascade.KindElevationRequired, cascade.KindUnsupported) {
		t.Fatalf("Get with no gate = %v, want a refusal", err)
	}
	if err := b.Rotate(ctx, "TOKEN", []byte("new")); !isKindOneOf(err, cascade.KindElevationRequired, cascade.KindUnsupported) {
		t.Fatalf("Rotate with no gate = %v, want a refusal", err)
	}
	if string(custody.entries["TOKEN"]) != "s3cr3t" {
		t.Fatal("a refused Rotate still wrote to the store")
	}
}

func isKindOneOf(err error, kinds ...cascade.Kind) bool {
	for _, k := range kinds {
		if isKind(err, k) {
			return true
		}
	}
	return false
}

func TestBrokerSetModes(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	ctx := context.Background()

	res, err := b.Set(ctx, "TOKEN", []byte("one"), SetUpdate)
	if err != nil || res.Name != "TOKEN" || res.Replaced {
		t.Fatalf("first Set = %+v, %v", res, err)
	}
	res, err = b.Set(ctx, "TOKEN", []byte("two"), SetUpdate)
	if err != nil || res.Name != "TOKEN" || !res.Replaced {
		t.Fatalf("update Set = %+v, %v", res, err)
	}
	if string(custody.entries["TOKEN"]) != "two" {
		t.Fatal("update did not overwrite in place")
	}
	res, err = b.Set(ctx, "TOKEN", []byte("three"), SetRename)
	if err != nil || res.Name != "TOKEN_2" || res.Replaced {
		t.Fatalf("rename Set = %+v, %v", res, err)
	}
	res, err = b.Set(ctx, "TOKEN", []byte("four"), SetRename)
	if err != nil || res.Name != "TOKEN_3" {
		t.Fatalf("second rename Set = %+v, %v", res, err)
	}
	if _, err := b.Set(ctx, "TOKEN", []byte("five"), SetRefuse); !isKind(err, cascade.KindConflict) {
		t.Fatalf("refuse Set = %v, want a conflict", err)
	}
	if string(custody.entries["TOKEN"]) != "two" {
		t.Fatal("a refused Set still wrote")
	}
}

func TestBrokerSetInvalidName(t *testing.T) {
	b, _ := newTestBroker(t, &allowGate{})
	if _, err := b.Set(context.Background(), "bad name", []byte("v"), SetUpdate); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Set with a bad name = %v", err)
	}
}

func TestBrokerFreeNameExhaustion(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	custody.entries["N"] = []byte("v")
	for i := 2; i <= maxCollisionSuffix; i++ {
		custody.entries["N_"+strconv.Itoa(i)] = []byte("v")
	}
	if _, err := b.Set(context.Background(), "N", []byte("v"), SetRename); !isKind(err, cascade.KindConflict) {
		t.Fatalf("an exhausted suffix space = %v, want a conflict", err)
	}
}

func TestBrokerRotate(t *testing.T) {
	gate := &allowGate{}
	b, custody := newTestBroker(t, gate)
	ctx := context.Background()
	if err := b.Rotate(ctx, "MISSING", []byte("v")); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("rotating an absent name = %v, want not-found", err)
	}
	if _, ok := custody.entries["MISSING"]; ok {
		t.Fatal("a refused Rotate created the secret")
	}
	custody.entries["TOKEN"] = []byte("old")
	if err := b.Rotate(ctx, "TOKEN", []byte("new")); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if string(custody.entries["TOKEN"]) != "new" {
		t.Fatal("Rotate did not replace the value")
	}
	if err := b.Rotate(ctx, "bad name", []byte("v")); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Rotate with a bad name = %v", err)
	}
	if gate.seen[len(gate.seen)-1] != VerbRotate {
		t.Fatalf("gate saw %v", gate.seen)
	}
}

func TestBrokerListAndDelete(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	ctx := context.Background()
	custody.entries["B"] = []byte("2")
	custody.entries["A"] = []byte("1")
	names, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if strings.Join(names, ",") != "A,B" {
		t.Fatalf("List = %v", names)
	}
	if err := b.Delete(ctx, "A"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := b.Delete(ctx, "A"); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
	if err := b.Delete(ctx, "bad name"); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Delete with a bad name = %v", err)
	}
}

func TestBrokerSurfacesStoreFailures(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	ctx := context.Background()
	custody.err = ErrCustodyUnavailable("memory", errors.New("bus down"))

	custody.failOn = "list"
	if _, err := b.List(ctx); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("List over a broken store = %v", err)
	}
	if _, err := b.Exists(ctx, "A"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Exists over a broken store = %v", err)
	}
	if _, err := b.Set(ctx, "A", []byte("v"), SetUpdate); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Set over a broken store = %v", err)
	}
	if err := b.Rotate(ctx, "A", []byte("v")); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Rotate over a broken store = %v", err)
	}

	custody.failOn = "set"
	if _, err := b.Set(ctx, "A", []byte("v"), SetUpdate); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Set with a failing write = %v", err)
	}
	custody.failOn = "get"
	if _, err := b.Get(ctx, "A"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Get with a failing read = %v", err)
	}
}

//go:build linux

package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newUnreachableSecretService builds the linux backend with a connect
// function that always fails, which is exactly the state of a headless host
// with no session bus.
func newUnreachableSecretService() *secretServiceCustody {
	return &secretServiceCustody{
		service: "cascade-test",
		connect: func() (*dbus.Conn, error) { return nil, errors.New("no session bus") },
	}
}

// TestSecretServiceRefusesWhenBusUnavailable is the fail-closed assertion:
// with no session bus every operation returns a typed unavailable error and
// none returns an empty result a caller could read as "no secrets".
func TestSecretServiceRefusesWhenBusUnavailable(t *testing.T) {
	s := newUnreachableSecretService()
	ctx := context.Background()
	if s.Name() != linuxCustodyName {
		t.Fatalf("Name() = %q", s.Name())
	}
	if s.Available() {
		t.Fatal("Available() = true with no session bus")
	}
	if err := s.Set(ctx, "TOKEN", []byte("v")); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Set = %v, want unavailable", err)
	}
	if _, err := s.Get(ctx, "TOKEN"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Get = %v, want unavailable", err)
	}
	if err := s.Delete(ctx, "TOKEN"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Delete = %v, want unavailable", err)
	}
	names, err := s.List(ctx)
	if !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("List = %v, want unavailable", err)
	}
	if names != nil {
		t.Fatal("List returned a name slice alongside its refusal")
	}
}

func TestSecretServiceValidatesNames(t *testing.T) {
	s := newUnreachableSecretService()
	ctx := context.Background()
	if err := s.Set(ctx, "bad name", []byte("v")); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Set = %v", err)
	}
	if _, err := s.Get(ctx, ""); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Get = %v", err)
	}
	if err := s.Delete(ctx, "bad name"); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Delete = %v", err)
	}
}

// TestSelectCustodyFallsBackWhenSecretServiceUnavailable proves the
// documented fallback: an unreachable secret service selects the encrypted
// file vault, which then round-trips.
func TestSelectCustodyFallsBackWhenSecretServiceUnavailable(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/cascade-test-bus")
	custody, err := SelectCustody(Config{Service: "cascade-test", Dir: t.TempDir(), Passphrase: "p"})
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	if custody.Name() != fileVaultName {
		t.Fatalf("selected %q, want the file vault", custody.Name())
	}
	ctx := context.Background()
	if err := custody.Set(ctx, "A", []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := custody.Get(ctx, "A")
	if err != nil || string(got) != "1" {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestPlatformElevatedRefusalIsNilOnLinux(t *testing.T) {
	if platformElevatedRefusal() != nil {
		t.Fatal("linux is a tier-1 platform; it must not refuse elevated verbs outright")
	}
}

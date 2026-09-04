//go:build darwin

package secrets

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeSecurity is a stand-in for /usr/bin/security: it records every
// invocation and answers from an in-memory item table, so the unit lane
// exercises every branch of the darwin backend without touching the real
// user keychain (which no test may write to).
type fakeSecurity struct {
	items map[string]string // account -> hex value
	calls [][]string
	fail  map[string]string // subcommand -> stderr to fail with
}

func newFakeSecurity() *fakeSecurity {
	return &fakeSecurity{items: map[string]string{}, fail: map[string]string{}}
}

func (f *fakeSecurity) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if len(args) == 0 {
		return nil, &runnerError{err: errors.New("exit status 1"), stderr: "no subcommand"}
	}
	sub := args[0]
	if stderr, ok := f.fail[sub]; ok {
		return nil, &runnerError{err: errors.New("exit status 1"), stderr: stderr}
	}
	switch sub {
	case "list-keychains":
		return []byte("\"/Users/x/Library/Keychains/login.keychain-db\"\n"), nil
	case "add-generic-password":
		f.items[flagValue(args, "-a")] = flagValue(args, "-X")
		return nil, nil
	case "find-generic-password":
		value, ok := f.items[flagValue(args, "-a")]
		if !ok {
			return nil, &runnerError{err: errors.New("exit status 44"), stderr: "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain."}
		}
		return []byte(value + "\n"), nil
	case "delete-generic-password":
		account := flagValue(args, "-a")
		if _, ok := f.items[account]; !ok {
			return nil, &runnerError{err: errors.New("exit status 44"), stderr: "security: The specified item could not be found in the keychain."}
		}
		delete(f.items, account)
		return nil, nil
	}
	return nil, &runnerError{err: errors.New("exit status 2"), stderr: "unknown subcommand"}
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func newFakeKeychain(t *testing.T) (*keychainCustody, *fakeSecurity) {
	t.Helper()
	fake := newFakeSecurity()
	custody, err := platformCustody(Config{Service: "cascade-unit-test", Runner: fake.run})
	if err != nil {
		t.Fatalf("platformCustody: %v", err)
	}
	kc, ok := custody.(*keychainCustody)
	if !ok {
		t.Fatalf("platformCustody returned %T", custody)
	}
	return kc, fake
}

func TestKeychainRoundTripFake(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	ctx := context.Background()
	if kc.Name() != darwinCustodyName {
		t.Fatalf("Name() = %q", kc.Name())
	}
	if !kc.Available() {
		t.Fatal("Available() = false with a working security tool")
	}
	if err := kc.Set(ctx, "TOKEN", []byte("s3cr3t")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := kc.Get(ctx, "TOKEN")
	if err != nil || string(got) != "s3cr3t" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	names, err := kc.List(ctx)
	if err != nil || len(names) != 1 || names[0] != "TOKEN" {
		t.Fatalf("List = %v, %v", names, err)
	}
	// The value must never travel as plaintext argv: it goes as hex.
	for _, call := range fake.calls {
		for _, arg := range call {
			if arg == "s3cr3t" {
				t.Fatal("the secret value was passed as a plaintext argument")
			}
		}
	}
	if err := kc.Delete(ctx, "TOKEN"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	names, err = kc.List(ctx)
	if err != nil || len(names) != 0 {
		t.Fatalf("List after Delete = %v, %v", names, err)
	}
}

func TestKeychainNotFoundAndUnavailable(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	ctx := context.Background()
	if _, err := kc.Get(ctx, "MISSING"); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("Get of an absent name = %v", err)
	}
	if err := kc.Delete(ctx, "MISSING"); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("Delete of an absent name = %v", err)
	}
	// A failure the tool does NOT describe as not-found must surface as
	// unavailable, never as "the secret is not there".
	fake.fail["find-generic-password"] = "security: User interaction is not allowed."
	if _, err := kc.Get(ctx, "TOKEN"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("an unreadable keychain = %v, want unavailable", err)
	}
	if _, err := kc.List(ctx); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("List over an unreadable keychain = %v", err)
	}
	fake.fail["delete-generic-password"] = "security: User interaction is not allowed."
	if err := kc.Delete(ctx, "TOKEN"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Delete over an unreadable keychain = %v", err)
	}
	fake.fail["add-generic-password"] = "security: write failed"
	if err := kc.Set(ctx, "TOKEN", []byte("v")); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Set over an unwritable keychain = %v", err)
	}
	fake.fail["list-keychains"] = "boom"
	if kc.Available() {
		t.Fatal("Available() = true with a failing security tool")
	}
}

// TestKeychainErrorsWithholdToolDiagnostics is the redaction assertion:
// the security tool can quote the arguments it was given, and Set's
// arguments carry the value, so a wrapped failure must not carry that text.
func TestKeychainErrorsWithholdToolDiagnostics(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	const canary = "canary-secret-value"
	fake.fail["add-generic-password"] = "security: add-generic-password -X " + hex.EncodeToString([]byte(canary)) + " failed"
	err := kc.Set(context.Background(), "TOKEN", []byte(canary))
	if err == nil {
		t.Fatal("a failing Set reported success")
	}
	text := err.Error()
	if strings.Contains(text, hex.EncodeToString([]byte(canary))) || strings.Contains(text, canary) {
		t.Fatalf("the error carries the tool's diagnostics: %v", err)
	}
	if redactRunner(errNope) != errNope {
		t.Fatal("redactRunner rewrote a non-runner error")
	}
}

func TestKeychainRefusesBadNames(t *testing.T) {
	kc, _ := newFakeKeychain(t)
	ctx := context.Background()
	if err := kc.Set(ctx, "bad name", []byte("v")); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Set = %v", err)
	}
	if _, err := kc.Get(ctx, "bad name"); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Get = %v", err)
	}
	if err := kc.Delete(ctx, "bad name"); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Delete = %v", err)
	}
}

func TestKeychainIndexCorrupt(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	fake.items[indexAccount] = hex.EncodeToString([]byte("not a json list"))
	if _, err := kc.List(context.Background()); !isKind(err, cascade.KindIntegrity) {
		t.Fatalf("a corrupt index = %v, want an integrity refusal", err)
	}
}

func TestKeychainIndexAddIsIdempotent(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	ctx := context.Background()
	for range 3 {
		if err := kc.Set(ctx, "TOKEN", []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	raw, err := hex.DecodeString(fake.items[indexAccount])
	if err != nil {
		t.Fatalf("decoding the index: %v", err)
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		t.Fatalf("index is not a name list: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("index = %v, want one entry", names)
	}
}

func TestDecodeKeychainValueNonHex(t *testing.T) {
	// A value written by another tool comes back as text; it is accepted
	// rather than refused, so a pre-existing entry stays readable.
	got, err := decodeKeychainValue([]byte("plain-text\n"))
	if err != nil || string(got) != "plain-text" {
		t.Fatalf("decodeKeychainValue = %q, %v", got, err)
	}
}

func TestPlatformElevatedRefusalIsNilOnDarwin(t *testing.T) {
	if platformElevatedRefusal() != nil {
		t.Fatal("darwin is a tier-1 platform; it must not refuse elevated verbs outright")
	}
}

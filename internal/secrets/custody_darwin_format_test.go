//go:build darwin

package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeSecurityOutput models security -w: printable ASCII is raw; other bytes are hex.
func fakeSecurityOutput(encoded string) ([]byte, error) {
	value, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("fake security received invalid hex")
	}
	for _, b := range value {
		if b < 0x20 || b > 0x7e {
			return []byte(hex.EncodeToString(value) + "\n"), nil
		}
	}
	return append(value, '\n'), nil
}

// keychainFormatValues contains public fixtures covering ambiguous tool output.
func keychainFormatValues() [][]byte {
	return [][]byte{
		[]byte("deadbeefcafe0123"), []byte("12345678"),
		[]byte(strings.Repeat("ab", 20)), []byte("abc"), []byte("café"),
		{0x01, 0x02, 0xff, 0xfe}, {},
	}
}

// TestKeychainCV1RoundTripHexShaped covers byte preservation through the tool fake.
func TestKeychainCV1RoundTripHexShaped(t *testing.T) {
	kc, _ := newFakeKeychain(t)
	ctx := context.Background()
	for i, value := range keychainFormatValues() {
		if err := kc.Set(ctx, "FORMAT", value); err != nil {
			t.Fatalf("fixture %d: Set failed", i)
		}
		got, err := kc.Get(ctx, "FORMAT")
		if err != nil || !bytes.Equal(got, value) {
			t.Errorf("fixture %d: round trip did not preserve bytes", i)
		}
	}
}

// TestKeychainCV1ArgvIsHexOfPrefixedForm checks the exact storage envelope.
func TestKeychainCV1ArgvIsHexOfPrefixedForm(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	for i, value := range keychainFormatValues() {
		fake.calls = nil
		if err := kc.Set(context.Background(), "FORMAT", value); err != nil {
			t.Fatalf("fixture %d: Set failed", i)
		}
		call := fake.calls[0]
		encoded := flagValue(call, "-X")
		stored, err := hex.DecodeString(encoded)
		if err != nil || string(stored) != "cv1:"+base64.StdEncoding.EncodeToString(value) {
			t.Errorf("fixture %d: incorrect storage envelope", i)
		}
		for _, arg := range call {
			if len(value) > 0 && strings.Contains(arg, string(value)) {
				t.Errorf("fixture %d: plaintext appeared in argv", i)
			}
		}
	}
}

// TestKeychainPreCV1ItemReadsRaw rejects heuristic hex decoding of older items.
func TestKeychainPreCV1ItemReadsRaw(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	value := []byte("deadbeefcafe0123")
	fake.items[kc.account("LEGACY")] = hex.EncodeToString(value)
	got, err := kc.Get(context.Background(), "LEGACY")
	if err != nil || !bytes.Equal(got, value) {
		t.Fatal("pre-cv1 item was not returned as raw text")
	}
}

// TestKeychainCV1BadBase64IsIntegrity checks classification and redaction.
func TestKeychainCV1BadBase64IsIntegrity(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	const output = "cv1:@@@"
	fake.items[kc.account("BROKEN")] = hex.EncodeToString([]byte(output))
	got, err := kc.Get(context.Background(), "BROKEN")
	if !errors.Is(err, cascade.ErrIntegrity) || !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatal("malformed cv1 did not return an integrity error")
	}
	if got != nil || !strings.Contains(err.Error(), "BROKEN") {
		t.Fatal("integrity error must name the item and return no value")
	}
	for _, withheld := range []string{output, "@@@", hex.EncodeToString([]byte(output))} {
		if strings.Contains(err.Error(), withheld) {
			t.Fatal("integrity error disclosed tool output")
		}
	}
}

// TestFakeSecurityPrintsLikeRealTool fixes the probe's output behavior in the fake.
func TestFakeSecurityPrintsLikeRealTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
	rows := []struct {
		value []byte
		want  string
	}{
		{[]byte("first"), "first\n"},
		{[]byte("café"), "636166c3a9\n"},
		{[]byte{0x01, 0x02, 0xff, 0xfe}, "0102fffe\n"},
		{[]byte{}, "\n"},
	}
	fake := newFakeSecurity()
	for i, row := range rows {
		_, err := fake.run(context.Background(), securityBin, "add-generic-password",
			"-a", "FORMAT", "-X", hex.EncodeToString(row.value), fakeKeychainPath)
		if err != nil {
			t.Fatalf("row %d: fake write failed", i)
		}
		got, err := fake.run(context.Background(), securityBin, "find-generic-password",
			"-a", "FORMAT", "-w", fakeKeychainPath)
		if err != nil || string(got) != row.want {
			t.Errorf("row %d: fake output disagrees with probe", i)
		}
	}
}

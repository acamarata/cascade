package secrets

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenPassphrase unlocks testdata/file-vault-golden.age. The fixture was
// produced by the real `age` tool, not by this package (see testdata/
// README.md), so a test that decrypts it proves interoperability rather
// than self-consistency.
const goldenPassphrase = "cascade-file-vault-golden-passphrase"

func TestAgeDecryptRealAgeFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "file-vault-golden.age"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	plain, err := ageDecrypt(goldenPassphrase, raw)
	if err != nil {
		t.Fatalf("decrypting the real-age fixture: %v", err)
	}
	const want = `{"DEMO_TOKEN":"c2FtcGxlLXZhbHVl"}`
	if string(plain) != want {
		t.Fatalf("fixture plaintext = %q, want %q", plain, want)
	}
}

func TestAgeDecryptRealAgeFixtureWrongPassphrase(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "file-vault-golden.age"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if _, err := ageDecrypt("not-the-passphrase", raw); err == nil {
		t.Fatal("a wrong passphrase decrypted the fixture")
	}
}

func TestAgeRoundTripSizes(t *testing.T) {
	sizes := []int{0, 1, 100, ageChunkSize - 1, ageChunkSize, ageChunkSize + 1, 2*ageChunkSize + 7}
	for _, size := range sizes {
		plain := make([]byte, size)
		if _, err := rand.Read(plain); err != nil {
			t.Fatalf("entropy: %v", err)
		}
		sealed, err := ageEncrypt("pw", plain, 2, rand.Reader)
		if err != nil {
			t.Fatalf("encrypt size %d: %v", size, err)
		}
		got, err := ageDecrypt("pw", sealed)
		if err != nil {
			t.Fatalf("decrypt size %d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d round-tripped to %d bytes", size, len(got))
		}
	}
}

func TestAgeEncryptRefusals(t *testing.T) {
	if _, err := ageEncrypt("", []byte("x"), 2, rand.Reader); err == nil {
		t.Fatal("an empty passphrase was accepted")
	}
	if _, err := ageEncrypt("pw", []byte("x"), 0, rand.Reader); err == nil {
		t.Fatal("work factor 0 was accepted")
	}
	if _, err := ageEncrypt("pw", []byte("x"), ageMaxLogN+1, rand.Reader); err == nil {
		t.Fatal("an out-of-range work factor was accepted")
	}
	if _, err := ageEncrypt("pw", []byte("x"), 2, errReader{}); err == nil {
		t.Fatal("a failing entropy source was accepted")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestAgeDecryptRefusesMalformed(t *testing.T) {
	good, err := ageEncrypt("pw", []byte("value"), 2, rand.Reader)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	cases := map[string][]byte{
		"empty":     {},
		"no footer": []byte("age-encryption.org/v1\n-> scrypt AAAA 2\nAAAA\n"),
		"footer unterminated": append([]byte("age-encryption.org/v1\n-> scrypt AAAA 2\nAAAA\n--- "),
			[]byte("nonewline")...),
		"tampered body":   tamper(good, len(good)-1),
		"tampered header": tamper(good, 30),
	}
	for name, input := range cases {
		if _, err := ageDecrypt("pw", input); err == nil {
			t.Fatalf("%s: malformed input decrypted", name)
		}
	}
	if _, err := ageDecrypt("", good); err == nil {
		t.Fatal("an empty passphrase decrypted a file")
	}
}

func TestAgeDecryptTruncatedPayload(t *testing.T) {
	good, err := ageEncrypt("pw", []byte("value"), 2, rand.Reader)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	idx := bytes.Index(good, []byte("\n"+ageFooter+" "))
	nl := bytes.IndexByte(good[idx+len("\n"+ageFooter+" "):], '\n')
	headerEnd := idx + len("\n"+ageFooter+" ") + nl + 1
	if _, err := ageDecrypt("pw", good[:headerEnd+4]); err == nil {
		t.Fatal("a truncated payload decrypted")
	}
}

func TestAgeParseHeaderRefusals(t *testing.T) {
	cases := [][]string{
		{"wrong-intro", "-> scrypt AAAAAAAAAAAAAAAAAAAAAA 2", "AAAA"},
		{ageIntro, "-> x25519 abc def", "AAAA"},
		{ageIntro, "-> scrypt !!! 2", "AAAA"},
		{ageIntro, "-> scrypt AAAAAAAAAAAAAAAAAAAAAA 99", "AAAA"},
		{ageIntro, "-> scrypt AAAAAAAAAAAAAAAAAAAAAA 2", "!!!"},
		{ageIntro, "-> scrypt AAAAAAAAAAAAAAAAAAAAAA 2"},
	}
	for _, lines := range cases {
		if _, _, _, err := ageParseHeader(lines); err == nil {
			t.Fatalf("header %q was accepted", strings.Join(lines, "|"))
		}
	}
}

func TestAgeSplitRejectsBadMAC(t *testing.T) {
	if _, _, err := ageSplit([]byte("age-encryption.org/v1\n--- !!!!\nbody")); err == nil {
		t.Fatal("a non-base64 header mac was accepted")
	}
}

// tamper flips one bit at index i.
func tamper(in []byte, i int) []byte {
	out := append([]byte(nil), in...)
	out[i] ^= 0x01
	return out
}

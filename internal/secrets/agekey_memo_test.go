// Purpose: proves the scrypt memo returns the same key as a fresh derivation,
// keys on every input, and never lets a changed or wrong-key file through.
// SPORT: secrets.filevault.keymemo tests (CHANGE).

package secrets

import (
	"bytes"
	"testing"
)

func TestMemoScryptKeyMatchesFreshDerivation(t *testing.T) {
	salt := bytes.Repeat([]byte{7}, 16)
	want, err := ageScryptKey("fake-pass", salt, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 { // the second call is served from the memo
		got, err := memoScryptKey("fake-pass", salt, 4)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("call %d: key %x, %v; want %x", i, got, err, want)
		}
		got[0] ^= 0xff // a caller mutating its copy must not corrupt the memo
	}
	for name, in := range map[string]struct {
		pass string
		salt []byte
		logN int
	}{
		"passphrase": {"fake-other", salt, 4},
		"salt":       {"fake-pass", bytes.Repeat([]byte{8}, 16), 4},
		"work":       {"fake-pass", salt, 5},
	} {
		got, err := memoScryptKey(in.pass, in.salt, in.logN)
		fresh, _ := ageScryptKey(in.pass, in.salt, in.logN)
		if err != nil || bytes.Equal(got, want) || !bytes.Equal(got, fresh) {
			t.Fatalf("%s change: memo key %x, fresh %x, err %v", name, got, fresh, err)
		}
	}
}

func TestMemoScryptDecryptStillAuthenticates(t *testing.T) {
	file, err := ageEncrypt("fake-pass", []byte("fake payload"), 4, bytes.NewReader(bytes.Repeat([]byte{3}, 256)))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		plain, err := ageDecryptWith("fake-pass", file, memoScryptKey)
		if err != nil || string(plain) != "fake payload" {
			t.Fatalf("decrypt = %q, %v", plain, err)
		}
	}
	if _, err := ageDecryptWith("fake-wrong", file, memoScryptKey); err == nil {
		t.Fatal("a wrong passphrase decrypted")
	}
	tampered := append([]byte(nil), file...)
	tampered[len(tampered)-1] ^= 1
	if _, err := ageDecryptWith("fake-pass", tampered, memoScryptKey); err == nil {
		t.Fatal("a tampered file decrypted after the key was memoized")
	}
}

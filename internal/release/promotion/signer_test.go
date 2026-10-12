package promotion

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// testKey is an in-test minisign key: an ed25519 key derived from a fixed
// seed byte, stamped with a key id, producing real minisign "ED" signatures
// that minisign.ParseSignature and minisign.Verify accept.
type testKey struct {
	priv ed25519.PrivateKey
	id   [8]byte
	pub  minisign.PublicKey
}

func newTestKey(t *testing.T, seed byte) testKey {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	k := testKey{priv: priv, id: [8]byte{seed, 1, 2, 3, 4, 5, 6, 7}}
	raw := append(append([]byte("Ed"), k.id[:]...), priv.Public().(ed25519.PublicKey)...)
	pub, err := minisign.ParsePublicKey(k.pubFileFrom(raw))
	if err != nil {
		t.Fatalf("test key does not parse: %v", err)
	}
	k.pub = pub
	return k
}

func (testKey) pubFileFrom(raw []byte) []byte {
	return []byte("untrusted comment: minisign public key\n" + base64.StdEncoding.EncodeToString(raw) + "\n")
}

// pubFile is the armored minisign public key file for k.
func (k testKey) pubFile() []byte {
	return k.pubFileFrom(append(append([]byte("Ed"), k.id[:]...), k.priv.Public().(ed25519.PublicKey)...))
}

// sign returns a minisign signature file over msg (prehashed "ED" mode)
// whose global signature binds comment.
func (k testKey) sign(msg []byte, comment string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(k.priv, h[:])
	data := append(append([]byte("ED"), k.id[:]...), sig...)
	global := ed25519.Sign(k.priv, append(append([]byte{}, sig...), comment...))
	return []byte("untrusted comment: signature\n" + base64.StdEncoding.EncodeToString(data) +
		"\ntrusted comment: " + comment + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

const (
	testVersion = "2.1.0"
	testRC      = "v2.1.0-rc.1"
	testFinal   = "v2.1.0"
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
)

var testClock = runtime.NewFixedClock(time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC))

// testImages is a well-formed cascade_images.txt.
func testImages() string {
	return "sha256:" + strings.Repeat("a", 64) + "  cascade-linux-amd64\n" +
		"sha256:" + strings.Repeat("b", 64) + "  cascade-linux-arm64\n" +
		"sha256:" + strings.Repeat("c", 64) + "  cascade\n"
}

// testFiles is the signed payload of a dist-<id> at testVersion (name ->
// content), without the checksums file, its signature or the cask.
func testFiles() map[string]string {
	files := map[string]string{"install.sh": "#!/bin/sh\necho install\n", imagesName: testImages()}
	for _, p := range platforms {
		a := archiveFile(testVersion, p)
		files[a] = "archive bytes for " + p
		files[a+".sbom.json"] = `{"sbom":"` + p + `"}`
	}
	return files
}

// checksumsFor renders goreleaser-style checksum lines for files.
func checksumsFor(files map[string]string) []byte {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(files)) {
		b.WriteString(digest([]byte(files[name])) + "  " + name + "\n")
	}
	return []byte(b.String())
}

// writeSignedSet writes a genuine signed dist-<id> under a fresh temp dir.
func writeSignedSet(t *testing.T, k testKey) string {
	t.Helper()
	dir := t.TempDir()
	files := testFiles()
	sums := checksumsFor(files)
	files[checksumsName] = string(sums)
	files[signatureName] = string(k.sign(sums, "cascade "+testVersion+" checksums"))
	files[caskName] = "cask \"cascade\" do\nend\n"
	for name, body := range files {
		writeFile(t, dir, name, body)
	}
	return dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wantRefusal asserts err is a cascade error of kind whose message contains
// every fragment (identity and message, never errors.Is alone).
func wantRefusal(t *testing.T, err error, kind cascade.Kind, fragments ...string) {
	t.Helper()
	var ce *cascade.Error
	if err == nil || !errors.As(err, &ce) {
		t.Fatalf("want a %v cascade error containing %q, got %v", kind, fragments, err)
	}
	if got, _ := cascade.KindOf(err); got != kind {
		t.Fatalf("want kind %v, got %v (%v)", kind, got, err)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Fatalf("error %q does not contain %q", err, f)
		}
	}
}

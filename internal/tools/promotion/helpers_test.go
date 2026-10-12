package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

// e2eKey is an ephemeral in-test minisign key (real "ED" signatures that
// internal/minisign verifies); seed picks a distinct key.
type e2eKey struct {
	priv ed25519.PrivateKey
	id   []byte
}

func newE2EKey(seed byte) e2eKey {
	return e2eKey{priv: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32)), id: []byte{seed, 8, 7, 6, 5, 4, 3, 2}}
}

func (k e2eKey) pubFile() []byte {
	raw := append(append([]byte("Ed"), k.id...), k.priv.Public().(ed25519.PublicKey)...)
	return []byte("untrusted comment: minisign public key\n" + base64.StdEncoding.EncodeToString(raw) + "\n")
}

func (k e2eKey) sign(msg []byte, comment string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(k.priv, h[:])
	global := ed25519.Sign(k.priv, append(append([]byte{}, sig...), comment...))
	data := append(append([]byte("ED"), k.id...), sig...)
	return []byte("untrusted comment: signature\n" + base64.StdEncoding.EncodeToString(data) +
		"\ntrusted comment: " + comment + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

const (
	e2eVersion = "2.3.0"
	e2eRC      = "v2.3.0-rc.2"
	e2eFinal   = "v2.3.0"
	e2eCommit  = "89abcdef0123456789abcdef0123456789abcdef"
	indexSHA   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

var e2eClock = runtime.NewFixedClock(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC))

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// signedSnapshot writes a signed dist-<id> (five archives, SBOMs,
// install.sh, cascade_images.txt, checksums, signature, cask) into a temp
// dir and returns it with the key's public file path.
func signedSnapshot(t *testing.T, k e2eKey) (dir, pubPath string) {
	t.Helper()
	dir = t.TempDir()
	files := map[string]string{
		"install.sh": "#!/bin/sh\n",
		"cascade_images.txt": "sha256:" + strings.Repeat("a", 64) + "  cascade-linux-amd64\n" +
			"sha256:" + strings.Repeat("b", 64) + "  cascade-linux-arm64\n" + indexSHA + "  cascade\n",
	}
	for _, p := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64"} {
		ext := map[bool]string{true: ".zip", false: ".tar.gz"}[p == "windows_amd64"]
		name := "cascade_" + e2eVersion + "_" + p + ext
		files[name] = "archive " + p + strings.Repeat("x", 200)
		files[name+".sbom.json"] = "{}" + p
	}
	var sums strings.Builder
	for _, n := range slices.Sorted(maps.Keys(files)) {
		sums.WriteString(sum([]byte(files[n])) + "  " + n + "\n")
	}
	files["cascade_checksums.txt"] = sums.String()
	files["cascade_checksums.txt.minisig"] = string(k.sign([]byte(sums.String()), "cascade "+e2eVersion+" checksums"))
	files["Casks/cascade.rb"] = "cask \"cascade\" do\nend\n"
	for n, body := range files {
		put(t, dir, n, body)
	}
	pubPath = filepath.Join(t.TempDir(), "minisign.pub")
	put(t, filepath.Dir(pubPath), "minisign.pub", string(k.pubFile()))
	return dir, pubPath
}

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// publicCopy byte-copies every file of dir except the cask into a new dir.
func publicCopy(t *testing.T, dir string) string {
	t.Helper()
	pub := t.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		put(t, pub, e.Name(), string(b))
	}
	return pub
}

// runTool runs the tool with args and returns exit code, stdout and stderr.
func runTool(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	w := output.New(&stdout, &stderr, false, false, false, true)
	code := run(w, e2eClock, args)
	return code, stdout.String(), stderr.String()
}

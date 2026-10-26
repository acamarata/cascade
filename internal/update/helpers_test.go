package update

// Purpose: shared fixtures for this package's tests: an ephemeral minisign
//   signer that emits real minisign-format bytes (internal/minisign parses
//   and verifies them; nothing here parses or verifies), tar.gz and zip
//   builders, and a signed release dist writer.
// Inputs: a seed byte per key; archive members; a dist spec.
// Outputs: files under t.TempDir() only.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/pkg/cascade"
)

// testKey is an ephemeral minisign key; seed picks a distinct key.
type testKey struct {
	priv ed25519.PrivateKey
	id   []byte
}

func newTestKey(seed byte) testKey {
	return testKey{priv: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32)), id: []byte{seed, 1, 2, 3, 4, 5, 6, 7}}
}

func (k testKey) pubFile() []byte {
	raw := append(append([]byte("Ed"), k.id...), k.priv.Public().(ed25519.PublicKey)...)
	return []byte("untrusted comment: minisign public key\n" + base64.StdEncoding.EncodeToString(raw) + "\n")
}

func (k testKey) pub(t *testing.T) minisign.PublicKey {
	t.Helper()
	p, err := minisign.ParsePublicKey(k.pubFile())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// sign emits a prehashed ("ED") minisign signature file over msg.
func (k testKey) sign(msg []byte, comment string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(k.priv, h[:])
	global := ed25519.Sign(k.priv, append(append([]byte{}, sig...), comment...))
	data := append(append([]byte("ED"), k.id...), sig...)
	return []byte("untrusted comment: signature\n" + base64.StdEncoding.EncodeToString(data) +
		"\ntrusted comment: " + comment + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// member is one archive entry; typ 0 is a regular file.
type member struct {
	name, body, link string
	typ              byte
}

func tarGz(t *testing.T, ms []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range ms {
		typ := m.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: m.name, Mode: 0o755, Size: int64(len(m.body)), Typeflag: typ, Linkname: m.link}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		must(t, tw.WriteHeader(h))
		if typ == tar.TypeReg {
			_, err := tw.Write([]byte(m.body))
			must(t, err)
		}
	}
	must(t, tw.Close())
	must(t, gz.Close())
	return buf.Bytes()
}

func zipBytes(t *testing.T, ms []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range ms {
		h := &zip.FileHeader{Name: m.name, Method: zip.Deflate}
		body := m.body
		if m.link != "" {
			h.SetMode(os.ModeSymlink | 0o777)
			body = m.link
		}
		w, err := zw.CreateHeader(h)
		must(t, err)
		_, err = w.Write([]byte(body))
		must(t, err)
	}
	must(t, zw.Close())
	return buf.Bytes()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// distSpec describes one signed release dist.
type distSpec struct {
	version  string
	comment  string // trusted comment; default "cascade <version> checksums"
	platform Platform
	members  []member // default: helper, plugin, cascade carrying the version
	key      testKey
	extra    []string // extra checksum lines
	tag      string   // metadata.json tag; default "v"+version
}

func defaultMembers(version string) []member {
	return []member{
		{name: "cascade-elevate-helper", body: "helper " + version},
		{name: "cascade-github", body: "github " + version},
		{name: "cascade", body: "cascade " + version},
		{name: "README.md", body: "readme"},
	}
}

func archiveNameFor(version string, p Platform) string {
	ext := ".tar.gz"
	if p.OS == "windows" {
		ext = ".zip"
	}
	return "cascade_" + version + "_" + p.OS + "_" + p.Arch + ext
}

// writeDist writes a signed dist into dir and returns the archive name and digest.
func writeDist(t *testing.T, dir string, s distSpec) (string, string) {
	t.Helper()
	if s.platform == (Platform{}) {
		s.platform = testPlatform
	}
	if s.members == nil {
		s.members = defaultMembers(s.version)
	}
	if s.comment == "" {
		s.comment = "cascade " + s.version + " checksums"
	}
	if s.tag == "" {
		s.tag = "v" + s.version
	}
	name := archiveNameFor(s.version, s.platform)
	var archive []byte
	if s.platform.OS == "windows" {
		archive = zipBytes(t, s.members)
	} else {
		archive = tarGz(t, s.members)
	}
	sum := hexSum(archive)
	lines := []string{sum + "  " + name, hexSum([]byte("install")) + "  install.sh"}
	lines = append(lines, s.extra...)
	sums := []byte(strings.Join(lines, "\n") + "\n")
	must(t, os.MkdirAll(dir, 0o755))
	put(t, dir, name, archive)
	put(t, dir, checksumsName, sums)
	put(t, dir, checksumsName+".minisig", s.key.sign(sums, s.comment))
	put(t, dir, "metadata.json", []byte(`{"project_name":"cascade","tag":"`+s.tag+`","version":"`+s.version+`"}`))
	return name, sum
}

func put(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	must(t, os.WriteFile(filepath.Join(dir, name), b, 0o644))
}

// wantKind asserts err is a cascade error of kind whose text contains msg.
func wantKind(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v (%T), want a cascade error of kind %v", err, err, kind)
	}
	if ce.Kind != kind || !strings.Contains(err.Error(), msg) {
		t.Fatalf("error = %v (kind %v), want kind %v containing %q", err, ce.Kind, kind, msg)
	}
}

// executablesUnder lists every known executable name found anywhere under dir.
func executablesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		switch strings.TrimSuffix(info.Name(), ".exe") {
		case "cascade", "cascade-github", "cascade-elevate-helper":
			found = append(found, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return found
}

var testPlatform = Platform{OS: "linux", Arch: "amd64"}

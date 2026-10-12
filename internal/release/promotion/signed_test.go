package promotion

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// substitute replaces the linux_amd64 archive in dir with other bytes and
// rewrites its checksums line to match, leaving the old signature in place.
func substitute(t *testing.T, dir string) {
	t.Helper()
	name := archiveFile(testVersion, "linux_amd64")
	old, evil := readFile(t, dir, name), []byte("substituted archive bytes")
	writeFile(t, dir, name, string(evil))
	sums := strings.Replace(string(readFile(t, dir, checksumsName)), digest(old), digest(evil), 1)
	writeFile(t, dir, checksumsName, sums)
}

// checkSubstitutedRecord forges a record that matches a substituted unsigned
// set and asserts VerifySigned still refuses it.
func checkSubstitutedRecord(t *testing.T, k testKey) {
	t.Helper()
	dir := writeSignedSet(t, k)
	rec := recordFromDir(t, k, dir)
	substitute(t, dir)
	staging, _ := SetFromDir(dir)
	rec.Archives = maps.Clone(rec.Archives)
	rec.Archives["linux_amd64"] = staging[archiveFile(testVersion, "linux_amd64")]
	rec.ChecksumsSHA256 = staging[checksumsName]
	public := maps.Clone(staging)
	delete(public, caskName)
	if _, err := VerifyIdentical(rec, staging, public); err != nil {
		t.Fatalf("precondition: the forged record must match the substituted set: %v", err)
	}
	_, err := VerifySigned(dir, k.pub, testFinal)
	wantRefusal(t, err, cascade.KindIntegrity, signatureName, "does not verify")
}

func TestVerifySignedRefusesSubstitutedSet(t *testing.T) {
	k, other := newTestKey(t, 7), newTestKey(t, 9)
	genuine := writeSignedSet(t, k)
	rep, err := VerifySigned(genuine, k.pub, testFinal)
	if err != nil || len(rep.Files) != 14 || len(rep.Images) != 3 {
		t.Fatalf("the genuine signed set must pass: %v %+v", err, rep)
	}

	t.Run("record_json matching a substituted unsigned set", func(t *testing.T) { checkSubstitutedRecord(t, k) })
	resign := func(key testKey, comment string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			writeFile(t, dir, signatureName, string(key.sign(readFile(t, dir, checksumsName), comment)))
		}
	}
	cases := []struct {
		name, final, frag string
		edit              func(t *testing.T, dir string)
	}{
		{"signed by another key", testFinal, "does not verify", resign(other, "cascade "+testVersion+" checksums")},
		{"trusted comment names another version", testFinal, "trusted comment", resign(k, "cascade 2.1.1 checksums")},
		{"dispatched for another version", "v2.1.1", "trusted comment", func(*testing.T, string) {}},
		{"archive byte flipped", testFinal, "signed set " + archiveFile(testVersion, "darwin_amd64") + " has sha256", func(t *testing.T, dir string) {
			writeFile(t, dir, archiveFile(testVersion, "darwin_amd64"), "archive bytes for darwin_amd65")
		}},
		{"extra file", testFinal, "signed set has extra file extra.bin", func(t *testing.T, dir string) { writeFile(t, dir, "extra.bin", "x") }},
		{"missing file", testFinal, "signed set is missing install.sh", func(t *testing.T, dir string) { removeFile(t, dir, "install.sh") }},
		{"missing signature", testFinal, signatureName + " is missing", func(t *testing.T, dir string) { removeFile(t, dir, signatureName) }},
		{"missing checksums", testFinal, checksumsName + " is missing", func(t *testing.T, dir string) { removeFile(t, dir, checksumsName) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeSignedSet(t, k)
			c.edit(t, dir)
			_, err := VerifySigned(dir, k.pub, c.final)
			wantRefusal(t, err, cascade.KindIntegrity, c.frag)
		})
	}
	_, err = VerifySigned(genuine, k.pub, "v2.1.0-rc.1")
	wantRefusal(t, err, cascade.KindInvalidInput, "not v2.Y.Z")
}

func removeFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteRefusesUnsignedImageDigest(t *testing.T) {
	k := newTestKey(t, 7)
	dir := writeSignedSet(t, k)
	rep, err := VerifySigned(dir, k.pub, testFinal)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(testImages()), "\n") {
		f := strings.Fields(l)
		want[f[1]] = f[0]
	}
	if rec := recordFromDir(t, k, dir); !reflect.DeepEqual(rec.Images, want) || !reflect.DeepEqual(rep.Images, want) {
		t.Fatalf("Record.Images %v / report %v, want the signed lines %v", rec.Images, rep.Images, want)
	}
	if name, err := RequireSignedImage(rep, want["cascade"]); err != nil || name != "cascade" {
		t.Fatalf("the signed index digest must pass: %q %v", name, err)
	}
	unsigned := "sha256:" + strings.Repeat("e", 64)
	_, err = RequireSignedImage(rep, unsigned)
	wantRefusal(t, err, cascade.KindIntegrity, unsigned, "not in the signed "+imagesName)
	_, err = RequireSignedImage(rep, "cascade:latest")
	wantRefusal(t, err, cascade.KindInvalidInput, "not sha256")

	edited := writeSignedSet(t, k)
	writeFile(t, edited, imagesName, strings.Replace(testImages(), strings.Repeat("c", 64), strings.Repeat("e", 64), 1))
	_, err = VerifySigned(edited, k.pub, testFinal)
	wantRefusal(t, err, cascade.KindIntegrity, "signed set "+imagesName+" has sha256")
}

func TestCheckKey(t *testing.T) {
	k, other := newTestKey(t, 7), newTestKey(t, 9)
	pub := k.pubFile()
	if err := CheckKey(pub, pub, k.pub); err != nil {
		t.Fatalf("identical key: %v", err)
	}
	oneByte := []byte(strings.Replace(string(pub), "public key", "public kez", 1))
	wantRefusal(t, CheckKey(oneByte, pub, k.pub), cascade.KindIntegrity, "byte-identical")
	wantRefusal(t, CheckKey([]byte("garbage\n"), pub, k.pub), cascade.KindIntegrity, "does not parse")
	wantRefusal(t, CheckKey(pub, pub, other.pub), cascade.KindIntegrity, "compiled into this binary")
}

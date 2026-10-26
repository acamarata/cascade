package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func receiptWithFiles(installDir string, names ...string) string {
	var files []string
	for _, n := range names {
		files = append(files, `{"name":"`+n+`","sha256":"`+hexSum([]byte(n))+`"}`)
	}
	return `{"schema_version":1,"channel":"script","release_tag":"v2.0.0","archive_name":"a","archive_sha256":"` +
		hexSum([]byte("a")) + `","install_dir":"` + installDir + `","installed_at":"x","files":[` + strings.Join(files, ",") + `]}`
}

// TestReadReceiptRefusesUnsafeNames is acceptance [13].
func TestReadReceiptRefusesUnsafeNames(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "bin")
	must(t, os.MkdirAll(install, 0o755))
	link := filepath.Join(dir, "link")
	must(t, os.Symlink(install, link))
	write := func(body string) string {
		p := filepath.Join(dir, "r.json")
		must(t, os.WriteFile(p, []byte(body), 0o644))
		return p
	}
	for name, msg := range map[string]string{
		"../x":        `install receipt refused: file name "../x" is not a bare name`,
		"bin/cascade": `install receipt refused: file name "bin/cascade" is not a bare name`,
		"hosts":       `install receipt refused: file name "hosts" is not a cascade executable`,
	} {
		_, err := ReadReceipt(write(receiptWithFiles(install, "cascade", name)))
		wantKind(t, err, cascade.KindIntegrity, msg)
	}
	for installDir, msg := range map[string]string{
		"relative/bin": `install receipt refused: install_dir "relative/bin" is not absolute`,
		link:           "install receipt refused: install_dir " + link + " is not a real directory",
	} {
		_, err := ReadReceipt(write(receiptWithFiles(installDir, "cascade")))
		wantKind(t, err, cascade.KindIntegrity, msg)
	}
	r, err := ReadReceipt(write(receiptWithFiles(install, "cascade-elevate-helper", "cascade-github", "cascade")))
	must(t, err)
	if len(r.Files) != 3 {
		t.Fatalf("files = %+v", r.Files)
	}
}

// TestParseReceiptRefusals covers the remaining structural refusals.
func TestParseReceiptRefusals(t *testing.T) {
	base := receiptWithFiles("/abs", "cascade")
	cases := map[string]struct{ body, msg string }{
		"not json":       {"{", "not valid JSON"},
		"trailing data":  {base + "{}", "trailing data after the JSON object"},
		"schema 2":       {strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1), "schema_version 2 is not 1"},
		"channel":        {strings.Replace(base, `"channel":"script"`, `"channel":"pip"`, 1), `unknown channel "pip"`},
		"relative dir":   {receiptWithFiles("rel", "cascade"), `install_dir "rel" is not absolute`},
		"no tag":         {strings.Replace(base, `"release_tag":"v2.0.0"`, `"release_tag":""`, 1), `release_tag "" is not v + a SemVer version`},
		"local tag":      {strings.Replace(base, `"release_tag":"v2.0.0"`, `"release_tag":"local"`, 1), `release_tag "local" is not v + a SemVer version`},
		"archive digest": {strings.Replace(base, hexSum([]byte("a")), "zz", 1), "archive_sha256 is not a sha256 digest"},
		"duplicate":      {receiptWithFiles("/abs", "cascade", "cascade"), `file name "cascade" is listed twice`},
		"no cascade":     {receiptWithFiles("/abs", "cascade-github"), "files[] does not list cascade"},
		"bad digest":     {strings.Replace(base, hexSum([]byte("cascade")), "x", 1), `file "cascade" has no sha256 digest`},
		"too large":      {base + strings.Repeat(" ", maxReceiptBytes), "larger than 65536 bytes"},
	}
	for name, tc := range cases {
		_, err := parseReceipt([]byte(tc.body))
		if err == nil {
			t.Fatalf("%s: parseReceipt accepted %q", name, tc.body[:min(len(tc.body), 60)])
		}
		wantKind(t, err, cascade.KindIntegrity, "install receipt refused: "+tc.msg)
	}
	if _, err := ReadReceipt(filepath.Join(t.TempDir(), "missing.json")); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("missing receipt = %v", err)
	}
	err := WriteReceipt(filepath.Join(t.TempDir(), "r.json"), Receipt{SchemaVersion: 2})
	wantKind(t, err, cascade.KindIntegrity, "install receipt refused: schema_version 2 is not 1")
}

// FuzzInstallReceiptParse is acceptance [10]: no input panics, and any
// accepted receipt names only known executables.
func FuzzInstallReceiptParse(f *testing.F) {
	f.Add([]byte(receiptWithFiles("/abs", "cascade")))
	f.Add([]byte(receiptWithFiles("/abs", "cascade-elevate-helper", "cascade-github", "cascade")))
	f.Add([]byte(`{"schema_version":1,"files":[{"name":"../x"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := parseReceipt(data)
		if err != nil {
			return
		}
		for _, file := range r.Files {
			if !knownExecutable(file.Name, receiptGOOS) || !isHexDigest(file.SHA256) {
				t.Fatalf("accepted unsafe file %+v", file)
			}
		}
		if !filepath.IsAbs(r.InstallDir) || r.SchemaVersion != 1 {
			t.Fatalf("accepted %+v", r)
		}
	})
}

package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSourceFromEnv(t *testing.T) {
	dist := t.TempDir()
	s, err := SourceFromEnv(envOf(nil))
	if err != nil || s.BaseURL != DefaultBaseURL || s.BaseURLHost() != "" {
		t.Fatalf("default source = %+v, %v", s, err)
	}
	for _, good := range []string{"http://127.0.0.1:8080/r", "http://localhost/r", "https://mirror.example/r"} {
		if _, err := SourceFromEnv(envOf(map[string]string{EnvBaseURL: good})); err != nil {
			t.Fatalf("%s refused: %v", good, err)
		}
	}
	for _, bad := range []string{"http://example.com/r", "https://u:p@example.com/r", "https://example.com/r?x=1", "ftp://x/r", "relative"} {
		if _, err := SourceFromEnv(envOf(map[string]string{EnvBaseURL: bad})); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s accepted: %v", bad, err)
		}
	}
	if _, err := SourceFromEnv(envOf(map[string]string{EnvDist: "rel/dist"})); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("relative dist accepted: %v", err)
	}
	if s, err := SourceFromEnv(envOf(map[string]string{EnvDist: dist})); err != nil || s.Dist != dist || s.fileURL("v1", "a") != "a" {
		t.Fatalf("dist source = %+v, %v", s, err)
	}
}

func TestLatestStableAndDirFetcher(t *testing.T) {
	dist := t.TempDir()
	if _, err := LatestStable(context.Background(), DirFetcher{Dir: dist}, Source{BaseURL: DefaultBaseURL}); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("non-locator in base mode = %v", err)
	}
	for body, kind := range map[string]cascade.Kind{"{": cascade.KindIntegrity, `{"tag":"bad tag"}`: cascade.KindUnavailable} {
		put(t, dist, "metadata.json", []byte(body))
		if _, err := LatestStable(context.Background(), DirFetcher{Dir: dist}, Source{Dist: dist}); !cascade.HasKind(err, kind) {
			t.Fatalf("metadata %q = %v, want %v", body, err, kind)
		}
	}
	for target, want := range map[string]string{"https://github.com/o/r/releases/tag/v2.0.1": "v2.0.1", "/releases/tag/": "", "https://x/tag/a/b": "", "%zz": ""} {
		if got := latestTagFrom(target); got != want {
			t.Fatalf("latestTagFrom(%q) = %q, want %q", target, got, want)
		}
	}
	must(t, os.Mkdir(filepath.Join(dist, "sub"), 0o755))
	must(t, os.Symlink(filepath.Join(dist, "metadata.json"), filepath.Join(dist, "link")))
	for name, kind := range map[string]cascade.Kind{"../x": cascade.KindInvalidInput, "": cascade.KindInvalidInput,
		"missing": cascade.KindNotFound, "sub": cascade.KindIntegrity, "link": cascade.KindIntegrity} {
		if _, err := (DirFetcher{Dir: dist}).Get(context.Background(), name); !cascade.HasKind(err, kind) {
			t.Fatalf("DirFetcher(%q) = %v, want %v", name, err, kind)
		}
	}
	put(t, dist, "big", make([]byte, maxMetadataBytes+1))
	rc, err := DirFetcher{Dir: dist}.Get(context.Background(), "big")
	must(t, err)
	if _, err := readCapped(rc, maxMetadataBytes, "big"); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("oversize read = %v", err)
	}
}

func TestTrustRoot(t *testing.T) {
	if _, err := TrustRoot(envOf(nil)); err != nil {
		t.Fatalf("embedded release key = %v", err)
	}
	dir, key := t.TempDir(), newTestKey(5)
	put(t, dir, "k.pub", key.pubFile())
	put(t, dir, "bad.pub", []byte("garbage"))
	pub, err := TrustRoot(envOf(map[string]string{EnvMinisignPubkey: filepath.Join(dir, "k.pub")}))
	if err != nil || pub.KeyID != key.pub(t).KeyID {
		t.Fatalf("override key = %+v, %v", pub, err)
	}
	if _, err := TrustRoot(envOf(map[string]string{EnvMinisignPubkey: filepath.Join(dir, "bad.pub")})); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("garbage key = %v", err)
	}
	if _, err := TrustRoot(envOf(map[string]string{EnvMinisignPubkey: filepath.Join(dir, "none")})); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("missing key = %v", err)
	}
}

func TestDefaultReceiptPath(t *testing.T) {
	for env, want := range map[string]string{"CASCADE_HOME": "/c/install-receipt.json", "HOME": "/h/.cascade/install-receipt.json",
		"USERPROFILE": "/u/.cascade/install-receipt.json", "": ""} {
		vals := map[string]string{"CASCADE_HOME": "/c", "HOME": "/h", "USERPROFILE": "/u"}
		got := DefaultReceiptPath(func(k string) string {
			if k == env {
				return vals[k]
			}
			return ""
		})
		if got != filepath.FromSlash(want) && (want != "" || got != "") {
			t.Fatalf("%s: DefaultReceiptPath = %q, want %q", env, got, want)
		}
	}
}

// TestRecordedLatestRedirects reads the recorded releases/latest answers
// and the metadata.json shape (testdata/README.md provenance).
func TestRecordedLatestRedirects(t *testing.T) {
	for file, want := range map[string]string{"releases-latest-tag.txt": "v2.18.3", "releases-latest-none.txt": ""} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		must(t, err)
		var loc string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.ToLower(line), "location: ") {
				loc = strings.TrimSpace(line[len("location: "):])
			}
		}
		if got := latestTagFrom(loc); got != want {
			t.Fatalf("%s: tag %q, want %q", file, got, want)
		}
	}
	tag, err := LatestStable(context.Background(), DirFetcher{Dir: "testdata"}, Source{Dist: "testdata"})
	if err != nil || tag != "v2.0.1-e2e.2" {
		t.Fatalf("metadata.json tag = %q, %v", tag, err)
	}
}

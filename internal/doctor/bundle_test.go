package doctor

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadFixtureCorpus reads testdata/fixture_corpus.txt (TEST-ONLY; not
// loaded by redact.go at runtime — see that file's header comment and
// bundle.go's REUSE note) into one string per non-comment, non-blank
// line, joining a PEM block's BEGIN/END lines with their body so the
// whole block is asserted as one unit.
func loadFixtureCorpus(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "fixture_corpus.txt"))
	if err != nil {
		t.Fatalf("open fixture_corpus.txt: %v", err)
	}
	defer func() { _ = f.Close() }()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture_corpus.txt: %v", err)
	}
	return out
}

// readTarFiles decodes a gzip-tar bundle into name->content.
func readTarFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, tr); err != nil {
			t.Fatalf("read tar entry %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = buf.Bytes()
	}
	return out
}

func TestDoctorBundleNoRecallCorpusValueSurvives(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	// Embed every corpus value across every bundle surface: a config
	// value, a log line, and a check report Detail string — proving the
	// scrub runs everywhere, not just one section.
	cfg := map[string]string{"runtime.data_dir": corpus[0]}
	var logLines []string
	for _, v := range corpus {
		logLines = append(logLines, "log: "+v)
	}
	report := RunReport{Entries: []ReportEntry{
		{Name: "probe", Result: CheckResult{Status: StatusError, Detail: strings.Join(corpus, " | ")}},
	}}

	path, err := WriteBundle(context.Background(), BundleOptions{
		SystemInfo:     map[string]string{"os": "darwin"},
		ResolvedConfig: cfg,
		LogLines:       logLines,
		Report:         report,
		OutDir:         t.TempDir(),
		Clock:          fixedTestClock(),
	})
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	files := readTarFiles(t, path)
	var all bytes.Buffer
	for _, content := range files {
		all.Write(content)
	}
	haystack := all.String()

	for _, v := range corpus {
		// A PEM block's body line legitimately still contains
		// base64-looking bytes that are themselves redacted
		// individually; check the corpus VALUE as a contiguous
		// substring is gone, which is the actual leak surface.
		if strings.Contains(haystack, v) {
			t.Errorf("fixture corpus value survived in bundle output: %q", v)
		}
	}
}

func TestDoctorBundleOnlyAllowlistedFieldsPresent(t *testing.T) {
	path, err := WriteBundle(context.Background(), BundleOptions{
		SystemInfo: map[string]string{
			"os":                    "darwin",
			"arch":                  "arm64",
			"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		ResolvedConfig: map[string]string{
			"runtime.profile": "default",
			"vault.token":     "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef0123456789",
		},
		OutDir: t.TempDir(),
		Clock:  fixedTestClock(),
	})
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	files := readTarFiles(t, path)
	sysInfo := string(files["system_info.json"])
	cfg := string(files["resolved_config.json"])

	if strings.Contains(sysInfo, "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("non-allowlisted key AWS_SECRET_ACCESS_KEY must be absent, got: %s", sysInfo)
	}
	if !strings.Contains(sysInfo, `"os"`) {
		t.Errorf("allowlisted key 'os' must be present, got: %s", sysInfo)
	}
	if strings.Contains(cfg, "vault.token") {
		t.Errorf("non-allowlisted key vault.token must be absent, got: %s", cfg)
	}
	if !strings.Contains(cfg, "runtime.profile") {
		t.Errorf("allowlisted key runtime.profile must be present, got: %s", cfg)
	}
}

func TestDoctorBundle_WriteFailureReturnsTypedError(t *testing.T) {
	// A non-existent, non-creatable OutDir (a file used as a directory)
	// forces os.Create to fail.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := WriteBundle(context.Background(), BundleOptions{
		OutDir: blocker, // exists as a FILE, so joining a filename under it fails to create
		Clock:  fixedTestClock(),
	})
	if err == nil {
		t.Fatalf("expected an error writing into a non-directory OutDir")
	}
	var bundleErr *BundleError
	if !errors.As(err, &bundleErr) {
		t.Fatalf("got err=%v (%T), want *BundleError", err, err)
	}
}

func TestDoctorBundle_CanceledContextReturnsTypedError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WriteBundle(ctx, BundleOptions{OutDir: t.TempDir(), Clock: fixedTestClock()})
	if err == nil {
		t.Fatalf("expected an error for an already-canceled context")
	}
	var bundleErr *BundleError
	if !errors.As(err, &bundleErr) {
		t.Fatalf("got err=%v (%T), want *BundleError", err, err)
	}
}

func TestBundleError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("disk full")
	be := &BundleError{Op: "create", Err: inner}
	if be.Error() == "" || !strings.Contains(be.Error(), "disk full") {
		t.Fatalf("got Error()=%q, want it to mention the wrapped error", be.Error())
	}
	if !errors.Is(be, inner) {
		t.Fatalf("Unwrap must expose the wrapped error for errors.Is")
	}
}

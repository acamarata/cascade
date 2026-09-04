// Purpose: tests for `cascade doctor bundle` - the archive it writes, the
//
//	redaction canary over every byte of it, and the absolute-path filter on
//	the resolved-config section. Split from doctor_test.go to keep both
//	files under Art.10.3's 300-line cap (R-14.117).
//
// Constraints: writes only under t.TempDir() (Art.7.1); no network I/O
//
//	(Art.7.2); the bundle is produced by the real command through the real
//	root tree, never by calling internal/doctor directly (Art.2).
//
// SPORT: cmd/cascade/doctor (ADD) - doctor command, bundle subcommand.
package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
)

// TestDoctorBundleIsMountedOnRoot proves the bundle subcommand resolves under
// the mounted doctor command.
func TestDoctorBundleIsMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"doctor", "bundle"})
	if err != nil || found.Name() != "bundle" {
		t.Fatalf("doctor bundle is not mounted: found=%v err=%v", found.Name(), err)
	}
}

// doctorCanary is a secret-shaped value planted in a check message. Every
// surface below is asserted not to contain it.
const doctorCanary = "cascade-canary-9f3b2ae7c1d84f60b5e2a7c9d1f4e8b3"

// TestDoctorRedactsHumanOutput proves a secret echoed back by a check never
// reaches the rendered human report.
func TestDoctorRedactsHumanOutput(t *testing.T) {
	deps := testDoctorDeps(t, &fakeCheck{
		name:    "leaky",
		status:  doctor.StatusError,
		message: "auth failed: api_key=" + doctorCanary,
	})
	out, err := execRootDoctor(t, deps, "doctor")
	if err == nil {
		t.Fatal("expected the failing check to fail the run")
	}
	if strings.Contains(out, doctorCanary) {
		t.Fatalf("planted secret reached the human report\noutput:\n%s", out)
	}
	if strings.Contains(err.Error(), doctorCanary) {
		t.Fatalf("planted secret reached the error message: %v", err)
	}
	if !strings.Contains(out, "leaky") {
		t.Errorf("redaction removed the check row entirely\noutput:\n%s", out)
	}
}

// TestDoctorRedactsJSONOutput proves the same for the --json envelope, which
// is a separate serialisation path from the human table.
func TestDoctorRedactsJSONOutput(t *testing.T) {
	deps := testDoctorDeps(t, &fakeCheck{
		name:    "leaky",
		status:  doctor.StatusWarn,
		message: "token=" + doctorCanary,
	})
	out, _ := execRootDoctor(t, deps, "--json", "doctor")
	if strings.Contains(out, doctorCanary) {
		t.Fatalf("planted secret reached the --json envelope\noutput:\n%s", out)
	}
}

// TestDoctorBundleRedactsAndDropsMachinePaths writes a real bundle and scans
// every byte of the archive for the planted secret and for an absolute
// machine path taken from the environment.
func TestDoctorBundleRedactsAndDropsMachinePaths(t *testing.T) {
	deps := testDoctorDeps(t, &fakeCheck{
		name:    "leaky",
		status:  doctor.StatusOK,
		message: "secret=" + doctorCanary,
	})
	out, err := execRootDoctor(t, deps, "doctor", "bundle")
	if err != nil {
		t.Fatalf("doctor bundle: %v", err)
	}
	path := strings.TrimPrefix(strings.TrimSpace(out), "wrote diagnostic bundle: ")
	if filepath.Dir(path) != deps.BundleDir {
		t.Fatalf("bundle written outside the injected directory: %s", path)
	}
	body := readBundle(t, path)
	if strings.Contains(body, doctorCanary) {
		t.Fatal("planted secret reached the diagnostic bundle")
	}
	// No JSON string VALUE in the archive may be an absolute path: that is
	// how a developer machine's directory layout leaks into a pasted bug
	// report. Entry names and keys are unaffected (neither starts with "/").
	for _, line := range strings.Split(body, "\n") {
		if idx := strings.Index(line, `": "`); idx >= 0 {
			value := line[idx+len(`": "`):]
			if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
				t.Errorf("bundle carries an absolute machine path: %s", line)
			}
		}
	}
}

// readBundle returns the concatenated contents of every entry in the gzip-tar
// bundle at path, plus the entry names.
func readBundle(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // path is produced by this test's own bundle run
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip bundle: %v", err)
	}
	defer func() { _ = gz.Close() }()

	var sb strings.Builder
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar entry: %v", err)
		}
		sb.WriteString(hdr.Name + "\n")
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar body: %v", err)
		}
		sb.Write(b)
		sb.WriteString("\n")
	}
	if !strings.Contains(sb.String(), "check_report.json") {
		t.Fatalf("bundle is missing the check report section:\n%s", sb.String())
	}
	return sb.String()
}

// TestIsAbsolutePathValue pins the platform cases the config filter covers.
func TestIsAbsolutePathValue(t *testing.T) {
	cases := map[string]bool{
		"/home/example/.cascade": true,
		`C:\Users\example`:       true,
		`\\server\share`:         true,
		"info":                   false,
		"":                       false,
		"relative/path":          false,
	}
	for in, want := range cases {
		if got := isAbsolutePathValue(in); got != want {
			t.Errorf("isAbsolutePathValue(%q) = %v, want %v", in, got, want)
		}
	}
}

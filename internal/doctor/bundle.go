package doctor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

// Purpose: `cascade doctor bundle` — packages a redacted, shareable
//
//	gzip-tar diagnostic artifact (ticket contract task 6).
//
// Inputs: BundleOptions — system info, resolved config, daemon log tail,
//
//	and a check RunReport, all as caller-supplied data (this package
//	never reads a live filesystem/database itself; the CLI handler
//	gathers those and passes them in, keeping WriteBundle unit-testable
//	without touching real system/daemon state).
//
// Outputs: the written bundle's absolute path.
// Constraints: every string value crossing into the tar is redacted via
//
//	redact.go first — FilterAllowedFields for the two structured
//	sections, RedactText for log lines and report Message/Detail/
//	Remediation text (§D-31: "the same detector pass runs over every log
//	line ... not only structured config fields"). A write failure
//	returns a typed *BundleError. Output path defaults to os.TempDir()
//	per the contract; tests always set BundleOptions.OutDir to
//	t.TempDir() (Art.7.1).
//
// SPORT: placeholder: doctor/bundle (ADD).

// BundleError is the typed error every WriteBundle failure returns.
type BundleError struct {
	Op  string
	Err error
}

// Error implements the error interface.
func (e *BundleError) Error() string {
	return fmt.Sprintf("doctor: bundle %s: %v", e.Op, e.Err)
}

// Unwrap exposes Err for errors.Is/As.
func (e *BundleError) Unwrap() error { return e.Err }

// BundleOptions carries WriteBundle's inputs.
type BundleOptions struct {
	// SystemInfo is a flat key->value map (os, arch, go_version,
	// cascade_version, num_cpu, ...).
	SystemInfo map[string]string
	// ResolvedConfig is a flat, dotted-path key->stringified-value map
	// (the same shape as Config.EffectiveEntries, stringified).
	ResolvedConfig map[string]string
	// LogLines is the daemon log tail to include (already truncated to N
	// lines by the caller; WriteBundle applies no truncation of its
	// own).
	LogLines []string
	// Report is the doctor check run report to include.
	Report RunReport
	// AllowedFields gates SystemInfo/ResolvedConfig; nil defaults to
	// DefaultAllowedFields().
	AllowedFields AllowedFields
	// OutDir is the directory the bundle file is written into; ""
	// defaults to os.TempDir() (production). Tests set t.TempDir().
	OutDir string
	// Clock supplies the bundle filename's timestamp and tar entry
	// mod-times; nil defaults to the system clock (production only).
	Clock cascaderuntime.Clock
}

// WriteBundle writes a redacted gzip-tar diagnostic bundle per opts and
// returns its absolute path.
func WriteBundle(ctx context.Context, opts BundleOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", &BundleError{Op: "context", Err: err}
	}
	allowed := opts.AllowedFields
	if allowed == nil {
		allowed = DefaultAllowedFields()
	}
	clock := opts.Clock
	if clock == nil {
		clock = cascaderuntime.NewSystemClock()
	}
	outDir := opts.OutDir
	if outDir == "" {
		outDir = os.TempDir()
	}

	now := clock.Now().UTC()
	path := filepath.Join(outDir, fmt.Sprintf("cascade-doctor-bundle-%s.tar.gz", now.Format("20060102-150405")))

	if err := writeBundleArchive(path, now, opts, allowed); err != nil {
		return "", err
	}
	return path, nil
}

// writeBundleArchive does the actual tar/gzip construction, factored out
// of WriteBundle to stay under Art.10.3's 50-line function cap.
func writeBundleArchive(path string, now time.Time, opts BundleOptions, allowed AllowedFields) error {
	f, err := os.Create(path)
	if err != nil {
		return &BundleError{Op: "create", Err: err}
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	sections := map[string]any{
		"system_info.json":     FilterAllowedFields(opts.SystemInfo, allowed),
		"resolved_config.json": FilterAllowedFields(opts.ResolvedConfig, allowed),
		"check_report.json":    redactReport(opts.Report),
	}
	for name, data := range sections {
		if err := writeJSONEntry(tw, now, name, data); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			_ = f.Close()
			return err
		}
	}
	logText := strings.Join(redactJoinedLines(opts.LogLines), "\n")
	if err := writeTextEntry(tw, now, "daemon_logs.txt", logText); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		_ = f.Close()
		return err
	}

	return closeBundleWriters(tw, gz, f)
}

// closeBundleWriters closes the tar writer, gzip writer, then the file,
// in that order, surfacing the first error as a typed *BundleError.
func closeBundleWriters(tw *tar.Writer, gz *gzip.Writer, f *os.File) error {
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		_ = f.Close()
		return &BundleError{Op: "tar-close", Err: err}
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		return &BundleError{Op: "gzip-close", Err: err}
	}
	if err := f.Close(); err != nil {
		return &BundleError{Op: "file-close", Err: err}
	}
	return nil
}

// writeJSONEntry marshals data and writes it as one tar entry named name.
func writeJSONEntry(tw *tar.Writer, modTime time.Time, name string, data any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return &BundleError{Op: "marshal " + name, Err: err}
	}
	return writeTarEntry(tw, modTime, name, b)
}

// writeTextEntry writes text as one tar entry named name.
func writeTextEntry(tw *tar.Writer, modTime time.Time, name, text string) error {
	return writeTarEntry(tw, modTime, name, []byte(text))
}

// writeTarEntry writes one tar header+body pair.
func writeTarEntry(tw *tar.Writer, modTime time.Time, name string, body []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(body)),
		ModTime: modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return &BundleError{Op: "write-header " + name, Err: err}
	}
	if _, err := tw.Write(body); err != nil {
		return &BundleError{Op: "write-body " + name, Err: err}
	}
	return nil
}

// redactReport returns a copy of report with every entry's Message/
// Detail/Remediation run through RedactText — the run report is
// human-authored free text (a check's error message can legitimately
// echo back an offending config value or connection string) and gets the
// same log-payload scrub as daemon log lines.
func redactReport(report RunReport) RunReport {
	out := RunReport{Entries: make([]ReportEntry, len(report.Entries)), GeneratedAt: report.GeneratedAt}
	for i, e := range report.Entries {
		r := e.Result
		r.Message = RedactText(r.Message)
		r.Detail = RedactText(r.Detail)
		r.Remediation = RedactText(r.Remediation)
		fx := e.FixedBy
		fx.Delta = RedactText(fx.Delta)
		out.Entries[i] = ReportEntry{Name: e.Name, Result: r, Fixed: e.Fixed, FixedBy: fx}
	}
	return out
}

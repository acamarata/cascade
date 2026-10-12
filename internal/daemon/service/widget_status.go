package service

// Purpose: the read side of the widget agent - the WidgetStatusReport shape,
//   the `launchctl print` parser (loaded or not), and the widget's own
//   self-report file (bundle_path, window_level, written_at, first_rows_at)
//   classified into RUNNING_FROM_TRASH or MOVED against the installed bundle.
// Inputs: `launchctl print gui/<uid>/<label>` output, the installed plist and
//   the self-report JSON at the plist's CASCADE_WIDGET_STATUS_PATH value.
// Outputs: WidgetStatusReport, which `cascade widget status --json` renders
//   and the doctor widget check consumes.
// Constraints: pure parsing here; the callers (widget_darwin.go) own the I/O.
//   A missing or unreadable self-report is "no self-report", never an error:
//   a widget that has not started yet has not written one.
// SPORT: internal/daemon/service (P1-WID-04).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Placement values: where the running widget says its bundle lives, against
// where the installer put it.
const (
	PlacementOK               = ""
	PlacementRunningFromTrash = "RUNNING_FROM_TRASH"
	PlacementMoved            = "MOVED"
)

// WidgetSelfReport is the widget's own status file.
type WidgetSelfReport struct {
	BundlePath  string `json:"bundle_path"`
	WindowLevel *int   `json:"window_level,omitempty"`
	WrittenAt   string `json:"written_at,omitempty"`
	FirstRowsAt string `json:"first_rows_at,omitempty"`
}

// WidgetStatusReport is the `cascade widget status --json` shape.
type WidgetStatusReport struct {
	Installed bool `json:"installed"`
	// Managed is false when the plist at the widget path was not written by
	// cascade; nothing else in the report is then trustworthy or filled.
	Managed bool `json:"managed"`
	// ProgramValid is true only when the plist bytes equal what the installer
	// renders for one of the fixed bundle candidates.
	ProgramValid  bool              `json:"program_valid"`
	Loaded        bool              `json:"loaded"`
	State         string            `json:"state,omitempty"`
	PlistPath     string            `json:"plist_path"`
	BundlePath    string            `json:"bundle_path,omitempty"`
	BundlePresent bool              `json:"bundle_present"`
	SelfReport    *WidgetSelfReport `json:"self_report,omitempty"`
	Placement     string            `json:"placement,omitempty"`
	// StatusPath is where the widget is told to write its self-report.
	StatusPath string `json:"status_path,omitempty"`
	// SelfReportError is set when the self-report file exists but cannot be
	// read or parsed.
	SelfReportError string `json:"self_report_error,omitempty"`
}

// parseLaunchctlPrint reports whether `launchctl print` output describes a
// loaded service for target, and the service's `state = ...` value. A loaded
// service's output opens with "<target> = {"; anything else is not loaded.
func parseLaunchctlPrint(out []byte, target string) (loaded bool, state string) {
	lines := strings.Split(string(out), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != target+" = {" {
		return false, ""
	}
	for _, line := range lines[1:] {
		if v, ok := strings.CutPrefix(line, "\tstate = "); ok {
			return true, strings.TrimSpace(v)
		}
	}
	return true, ""
}

// parseSelfReport decodes the widget's status file; malformed JSON is an error.
func parseSelfReport(data []byte) (*WidgetSelfReport, error) {
	var r WidgetSelfReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// readWidgetSelfReport fills rep from the status file the plist names. An
// absent file is "has not reported" (nothing set); an unreadable or malformed
// one sets SelfReportError.
func readWidgetSelfReport(sp string, rep *WidgetStatusReport) {
	if sp == "" {
		return
	}
	rep.StatusPath = sp
	raw, err := os.ReadFile(sp) //nolint:gosec // path recorded by our own installer
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		rep.SelfReportError = err.Error()
		return
	}
	r, perr := parseSelfReport(raw)
	if perr != nil {
		rep.SelfReportError = "parse " + sp + ": " + perr.Error()
		return
	}
	rep.SelfReport = r
	rep.Placement = classifyPlacement(rep.BundlePath, r)
}

// inTrash reports whether path has a .Trash or .Trashes component (a bundle
// trashed from another volume lives under /Volumes/<v>/.Trashes/<uid>/).
func inTrash(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if part == ".Trash" || part == ".Trashes" {
			return true
		}
	}
	return false
}

// classifyPlacement compares the self-reported bundle with the installed one.
func classifyPlacement(installed string, r *WidgetSelfReport) string {
	if r == nil || r.BundlePath == "" {
		return PlacementOK
	}
	switch {
	case inTrash(r.BundlePath):
		return PlacementRunningFromTrash
	case filepath.Clean(r.BundlePath) != filepath.Clean(installed):
		return PlacementMoved
	}
	return PlacementOK
}

// matchWidgetPlist is the only trust decision about an installed plist: the
// bytes must equal, byte for byte, what the installer renders for one of the
// fixed bundle candidates under the current config. The document is never
// parsed, so no reader/launchd disagreement can hide an extra key. The bundle
// and status path come from the matching render inputs. Whitespace or comment
// differences also read as edited (fail-closed).
func matchWidgetPlist(cfg WidgetConfig, data []byte) (bundle, statusPath string, ok bool) {
	if cfg.Paths == nil {
		return "", "", false
	}
	env, err := widgetEnvironment(cfg)
	if err != nil {
		return "", "", false
	}
	for _, c := range widgetCandidates(cfg) {
		if bytes.Equal(data, renderWidgetPlist(widgetExecutablePath(c), env, widgetLogPath(cfg))) {
			return c, env["CASCADE_WIDGET_STATUS_PATH"], true
		}
	}
	return "", "", false
}

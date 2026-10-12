package doctor

// Purpose: the `widget` doctor check - whether the macOS menu-bar widget's
//   LaunchAgent is installed, its bundle is where the installer put it, the
//   running widget agrees about where it lives and how its window is levelled,
//   and a loaded widget's daemon answers status.widget in time. It is a FILTER
//   check: the default `cascade doctor` report leaves it out and
//   `cascade doctor --widget` runs only it.
// Inputs: a WidgetStateReader (installed plist, bundle presence, launchd
//   loaded state, the widget's self-report) and a WidgetDaemonProbe
//   (status.widget), both injected by cmd/cascade so this package imports
//   neither internal/daemon nor internal/client.
// Outputs: a CheckResult: OK when not installed (with the install remedy) or
//   healthy; WARN for a missing bundle, a bundle running from the Trash or
//   moved, an installed-but-unloaded agent, or a loaded agent that has given
//   no readable self-report; ERROR for a plist cascade did not write or whose
//   program is not the app, a window level below normalWindow-1, or a loaded
//   agent whose daemon gives no status.widget answer within the timeout.
// Constraints: Run never writes; an unreadable subject is ERROR, never OK.
// SPORT: internal/doctor (P1-WID-04).

import (
	"context"
	"fmt"
	"time"
)

const (
	// WidgetCheckName is the registry name `doctor --widget` selects.
	WidgetCheckName = "widget"
	// widgetProbeTimeout bounds the status.widget probe.
	widgetProbeTimeout = 2 * time.Second
	// normalWindow is the window level of an ordinary window; the widget
	// may sit one level below it (a desktop-adjacent panel) but no lower.
	normalWindow = 0
	// widgetInstallRemedy is the remedy for a missing install.
	widgetInstallRemedy = "cascade widget install"
	// widgetLabel is the launchd label of the widget agent.
	widgetLabel = "com.acamarata.cascade.widget"
)

// Placement values the state reader reports; they match the installer's.
const (
	WidgetPlacementTrash = "RUNNING_FROM_TRASH"
	WidgetPlacementMoved = "MOVED"
)

// WidgetState is everything the check reads about the widget.
type WidgetState struct {
	// Unsupported marks a platform without the widget; the check is then OK
	// with no install remedy.
	Unsupported bool
	Installed   bool
	// Managed is false when the plist at the widget path was not written by
	// cascade. ProgramValid is true only when it runs the fixed bundle
	// executable and nothing else.
	Managed      bool
	ProgramValid bool
	Loaded       bool
	// State is launchd's service state ("running", "not running", ...).
	State         string
	BundlePath    string // where the installer put the bundle
	BundlePresent bool
	// Placement is "", WidgetPlacementTrash or WidgetPlacementMoved.
	Placement string
	// ReportedBundlePath is the bundle the running widget says it runs from.
	ReportedBundlePath string
	// WindowLevel is the self-reported window level; nil when unreported.
	WindowLevel *int
	// SelfReported is true when the widget's self-report file parsed.
	SelfReported bool
	// StatusPath is where the widget should write its self-report.
	StatusPath string
	// SelfReportError is set when the self-report exists but is unreadable.
	SelfReportError string
}

// WidgetStateReader reads the widget's installed state.
type WidgetStateReader interface {
	WidgetState(ctx context.Context) (WidgetState, error)
}

// WidgetDaemonProbe asks the daemon for status.widget; a non-nil error means
// no usable answer.
type WidgetDaemonProbe interface {
	ProbeStatusWidget(ctx context.Context) error
}

type widgetCheck struct {
	state   WidgetStateReader
	probe   WidgetDaemonProbe
	timeout time.Duration
}

// NewWidgetCheck builds the widget check with the 2 s status.widget bound.
func NewWidgetCheck(state WidgetStateReader, probe WidgetDaemonProbe) Check {
	return widgetCheck{state: state, probe: probe, timeout: widgetProbeTimeout}
}

func (widgetCheck) Name() string { return WidgetCheckName }
func (widgetCheck) Describe() string {
	return "the menu-bar widget LaunchAgent is installed, placed, levelled and answering"
}
func (widgetCheck) Metadata() CheckMeta { return CheckMeta{} }
func (widgetCheck) Fix(context.Context) (FixResult, error) {
	return FixResult{}, ErrCheckNotFixable
}

// Run evaluates the widget, first failure wins.
func (c widgetCheck) Run(ctx context.Context) (CheckResult, error) {
	if c.state == nil {
		return CheckResult{Status: StatusError, Message: "widget state reader is not configured"}, nil
	}
	st, err := c.state.WidgetState(ctx)
	if err != nil {
		return CheckResult{Status: StatusError, Message: "could not read the widget state", Detail: err.Error()}, nil
	}
	if r, done := c.staticVerdict(st); done {
		return r, nil
	}
	return c.liveVerdict(ctx, st), nil
}

// staticVerdict judges what needs no daemon call.
func (c widgetCheck) staticVerdict(st WidgetState) (CheckResult, bool) {
	if r, done := identityVerdict(st); done {
		return r, true
	}
	switch {
	case !st.BundlePresent:
		return CheckResult{
			Status:      StatusWarn,
			Message:     "widget bundle is missing at " + st.BundlePath,
			Remediation: "put CascadeMenuBar.app back in /Applications or ~/Applications, then run " + widgetInstallRemedy,
		}, true
	case st.Placement == WidgetPlacementTrash || st.Placement == WidgetPlacementMoved:
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("widget %s: installed at %s, running from %s", st.Placement, st.BundlePath, st.ReportedBundlePath),
			Remediation: "restore the bundle to the installed path, or reinstall: " + widgetInstallRemedy,
		}, true
	case st.WindowLevel != nil && *st.WindowLevel < normalWindow-1:
		return CheckResult{
			Status:      StatusError,
			Message:     fmt.Sprintf("widget window level %d is below the allowed minimum %d", *st.WindowLevel, normalWindow-1),
			Remediation: "restart the widget; if it persists, reinstall: " + widgetInstallRemedy,
		}, true
	case !st.Loaded:
		return CheckResult{Status: StatusWarn, Message: "widget LaunchAgent is installed but not loaded", Remediation: widgetInstallRemedy}, true
	}
	return reportVerdict(st)
}

// identityVerdict judges whether the subject is absent or is not ours: no
// other row may vouch for a launch agent cascade did not write.
func identityVerdict(st WidgetState) (CheckResult, bool) {
	switch {
	case st.Unsupported:
		return CheckResult{Status: StatusOK, Message: "the widget is not supported on this platform"}, true
	case !st.Installed:
		return CheckResult{Status: StatusOK, Message: "widget LaunchAgent not installed", Remediation: widgetInstallRemedy}, true
	case !st.Managed:
		return CheckResult{
			Status:      StatusError,
			Message:     "a launch agent not written by cascade holds " + widgetLabel,
			Remediation: "inspect ~/Library/LaunchAgents/" + widgetLabel + ".plist and remove it if you did not create it",
		}, true
	case !st.ProgramValid:
		return CheckResult{
			Status:      StatusError,
			Message:     "the widget launch agent differs from what cascade installs, so it does not run the CascadeMenuBar app as installed",
			Remediation: "run `cascade widget uninstall` then `cascade widget install`, or inspect ~/Library/LaunchAgents/" + widgetLabel + ".plist",
		}, true
	}
	return CheckResult{}, false
}

// reportVerdict warns when a loaded widget has given no usable self-report:
// the daemon answering says nothing about the widget itself.
func reportVerdict(st WidgetState) (CheckResult, bool) {
	switch {
	case st.SelfReportError != "":
		return CheckResult{
			Status:      StatusWarn,
			Message:     "the widget self-report cannot be read: " + st.SelfReportError,
			Remediation: "restart the widget; if it persists, reinstall: " + widgetInstallRemedy,
		}, true
	case !st.SelfReported:
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("the widget has not reported (state %s) to %s", stateOrUnknown(st.State), st.StatusPath),
			Remediation: "open the widget; if it still does not report, reinstall: " + widgetInstallRemedy,
		}, true
	}
	return CheckResult{}, false
}

// stateOrUnknown names an empty launchd state.
func stateOrUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// liveVerdict asks the daemon for status.widget within the timeout.
func (c widgetCheck) liveVerdict(ctx context.Context, _ WidgetState) CheckResult {
	if c.probe == nil {
		return CheckResult{Status: StatusError, Message: "widget daemon probe is not configured"}
	}
	pctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.probe.ProbeStatusWidget(pctx); err != nil {
		return CheckResult{
			Status:      StatusError,
			Message:     fmt.Sprintf("widget is loaded but status.widget did not answer within %s", c.timeout),
			Detail:      err.Error(),
			Remediation: "start the daemon: cascade daemon start",
		}
	}
	return CheckResult{Status: StatusOK, Message: "widget installed, loaded and status.widget answers"}
}

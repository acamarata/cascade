// Purpose: binds the `widget` doctor check (internal/doctor) to the real
//
//	widget installer state (internal/daemon/service) and to the daemon's
//	status.widget call (internal/client), and holds the two filter helpers
//	that keep the check out of the default report. internal/doctor imports
//	neither of those packages; every dependency crosses here.
//
// Inputs: the run's PathProvider; production widgetDeps and the daemon socket
//
//	resolved from config.toml, exactly as the other daemon-backed checks do.
//
// Outputs: a doctor.WidgetStateReader and a doctor.WidgetDaemonProbe.
// Constraints: the check is a FILTER like storage: withoutWidgetCheck drops it
//
//	from the default run and onlyWidgetCheck narrows to it (P1-CORE-09's
//	mechanism, mirrored, not a second one). The probe is bounded by the
//	context the check hands it (2 s).
//
// SPORT: cmd/cascade/doctor (P1-WID-04).
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// widgetRPC is the daemon call seam: one status.widget round trip, result
// discarded. The production value names the method as a constant so the
// RPC method gate can resolve it.
type widgetRPC func(ctx context.Context) error

// widgetDoctorBindings returns the production state reader and daemon probe.
func widgetDoctorBindings(paths runtime.PathProvider) (doctor.WidgetStateReader, doctor.WidgetDaemonProbe) {
	deps := productionWidgetDeps()
	deps.Paths = paths
	return widgetDoctorBindingsFrom(deps, widgetDaemonRPC(paths))
}

// widgetDoctorBindingsFrom binds the check's interfaces to deps and rpc.
func widgetDoctorBindingsFrom(deps widgetDeps, rpc widgetRPC) (doctor.WidgetStateReader, doctor.WidgetDaemonProbe) {
	return widgetStateSource{deps: deps}, widgetStatusProbe{rpc: rpc}
}

// widgetDaemonRPC dials the daemon socket config.toml resolves and issues
// status.widget through the SDK client.
func widgetDaemonRPC(paths runtime.PathProvider) widgetRPC {
	return func(ctx context.Context) error {
		deps := statusDeps{Paths: paths, Getenv: os.Getenv, Environ: os.Environ, DialContext: client.UnixDialer}
		settings, err := resolveStatusSocket(ctx, deps)
		if err != nil {
			return err
		}
		var raw json.RawMessage
		return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), statusDialTimeout).Do(ctx, daemon.MethodStatusWidget, nil, &raw)
	}
}

// widgetStateSource reads the widget's state through the service installer.
type widgetStateSource struct{ deps widgetDeps }

// WidgetState folds service.WidgetStatusReport into doctor.WidgetState. A
// platform without the widget is "not installed".
func (s widgetStateSource) WidgetState(context.Context) (doctor.WidgetState, error) {
	cfg, err := widgetServiceConfig(s.deps)
	if err != nil {
		return doctor.WidgetState{}, err
	}
	rep, err := s.deps.Installer.Status(cfg)
	if cascade.HasKind(err, cascade.KindUnsupported) {
		return doctor.WidgetState{Unsupported: true}, nil
	}
	if err != nil {
		return doctor.WidgetState{}, err
	}
	st := doctor.WidgetState{
		Installed: rep.Installed, Managed: rep.Managed, ProgramValid: rep.ProgramValid,
		Loaded: rep.Loaded, State: rep.State, BundlePath: rep.BundlePath,
		BundlePresent: rep.BundlePresent, Placement: rep.Placement,
		StatusPath: rep.StatusPath, SelfReportError: rep.SelfReportError,
	}
	if rep.SelfReport != nil {
		st.SelfReported = true
		st.ReportedBundlePath, st.WindowLevel = rep.SelfReport.BundlePath, rep.SelfReport.WindowLevel
	}
	return st, nil
}

// widgetStatusProbe asks the daemon for status.widget.
type widgetStatusProbe struct{ rpc widgetRPC }

// ProbeStatusWidget issues status.widget and discards the snapshot.
func (p widgetStatusProbe) ProbeStatusWidget(ctx context.Context) error {
	return p.rpc(ctx)
}

// withoutWidgetCheck drops the widget check from the default run.
func withoutWidgetCheck(checks []doctor.Check) []doctor.Check {
	kept := make([]doctor.Check, 0, len(checks))
	for _, c := range checks {
		if c.Name() != doctor.WidgetCheckName {
			kept = append(kept, c)
		}
	}
	return kept
}

// onlyWidgetCheck narrows a run to the widget check; a registry without one
// yields nothing, which executeChecks refuses.
func onlyWidgetCheck(reg *doctor.CheckRegistry) []doctor.Check {
	check, ok := reg.Lookup(doctor.WidgetCheckName)
	if !ok {
		return nil
	}
	return []doctor.Check{check}
}

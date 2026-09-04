// Purpose: `cascade doctor bundle` (07-CLI-COMMAND-TREE §doctor), the
//
//	shareable-diagnostic half of the doctor command tree. Split from
//	doctor.go to keep both files under Art.10.3's 300-line cap (R-14.117:
//	a change may split a file it owns into siblings in the same package).
//
// Inputs: the same doctorDeps doctor.go injects.
// Outputs: the written bundle's path, via internal/output.Writer.
// Constraints: this file gathers the bundle's inputs and hands them to
//
//	internal/doctor.WriteBundle, which owns the allowlist filtering and the
//	secret scrub. No value is written here that has not passed through it.
//
// SPORT: cmd/cascade/doctor (ADD) - doctor command, bundle subcommand.
package main

import (
	"context"
	"fmt"
	goruntime "runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newDoctorBundleCmd builds `cascade doctor bundle`.
func newDoctorBundleCmd(deps doctorDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "bundle",
		Short: "Write a redacted diagnostic bundle for sharing",
		Long: "Package system information, the resolved config, and a check run into\n" +
			"a gzip-tar archive. Every value is filtered against an allowlist and\n" +
			"scanned for secret shapes before it is written.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctorBundle(cmd, deps)
		},
	}
}

// runDoctorBundle assembles the bundle's inputs and writes it.
func runDoctorBundle(cmd *cobra.Command, deps doctorDeps) error {
	ctx := cmd.Context()
	report, err := executeChecks(ctx, deps, &doctorFlags{})
	if err != nil {
		return err
	}
	path, err := doctor.WriteBundle(ctx, doctor.BundleOptions{
		SystemInfo:     doctorSystemInfo(),
		ResolvedConfig: doctorResolvedConfig(ctx, deps),
		Report:         redactDoctorReport(report),
		OutDir:         deps.BundleDir,
		Clock:          deps.Clock,
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade doctor bundle: write")
	}
	return doctorOutputWriter(cmd).Result(bundleView{Path: path})
}

// bundleView is `doctor bundle`'s result shape.
type bundleView struct {
	Path string `json:"path"`
}

// String renders the human form.
func (v bundleView) String() string { return "wrote diagnostic bundle: " + v.Path }

// doctorSystemInfo gathers the bundle's system section. Keys match
// doctor.DefaultAllowedFields; anything not on that allowlist is dropped by
// WriteBundle rather than redacted, so adding a key here cannot leak.
func doctorSystemInfo() map[string]string {
	return map[string]string{
		"os":              goruntime.GOOS,
		"arch":            goruntime.GOARCH,
		"go_version":      goruntime.Version(),
		"cascade_version": buildinfo.Version,
	}
}

// doctorResolvedConfig stringifies the effective config for the bundle. A
// load failure yields no config section rather than a failed bundle: a bundle
// is most wanted precisely when config is broken.
func doctorResolvedConfig(ctx context.Context, deps doctorDeps) map[string]string {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    deps.Paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range cfg.EffectiveEntries() {
		v := fmt.Sprintf("%v", e.Value)
		// doctor.DefaultAllowedFields gates these keys for CREDENTIAL
		// content, which is a different question from whether a value
		// identifies the machine it was collected on: runtime.data_dir is
		// allowlisted and holds an absolute path under the operator's home
		// directory. A bundle is written to be pasted into a bug report, so
		// the composition root, which is the layer that decides what is
		// handed to the bundle writer at all (bundle.go's own doc comment),
		// drops absolute paths before they get there.
		if isAbsolutePathValue(v) {
			continue
		}
		out[e.Key] = v
	}
	return out
}

// isAbsolutePathValue reports whether v looks like an absolute filesystem
// path on any supported platform (posix, Windows drive-letter, or UNC).
func isAbsolutePathValue(v string) bool {
	switch {
	case strings.HasPrefix(v, "/"), strings.HasPrefix(v, `\\`):
		return true
	case len(v) >= 3 && v[1] == ':' && (v[2] == '\\' || v[2] == '/'):
		return true
	default:
		return false
	}
}

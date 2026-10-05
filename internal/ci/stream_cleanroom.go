// Purpose (this file): the local executor's clean-room helpers: the
// controller's module populate-and-verify step, the forced per-run
// environment, the kind-to-step mapping and the target expansion. Split from
// stream_local.go to keep both files under the 300-line cap; the contract
// they implement (R18 M6 as revised by R18b N6) is stated in that file's
// header.
//
// Inputs: the ambient environment snapshot, the [ci.local] env names, the
// run directory and the shared module cache.
// Outputs: the run's environment, the controller's populate step, and the
// command lines of a kind's steps.
// Constraints: forced values win over AllowedEnv and over [ci.local] keys;
// an affected target is placed on a command line only after the safe-argv
// check.
// SPORT: internal.ci.cleanRoomEnv/ADDED (P1-CI-01).

package ci

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stepKindFor maps a requirement kind onto the runner's lint/test/build.
func stepKindFor(k RequirementKind) StepKind {
	switch k {
	case RequirementCompile:
		return StepBuild
	case RequirementUnit, RequirementIntegration, RequirementArchitecture:
		return StepTest
	case RequirementFormat, RequirementLint, RequirementSecurity:
		return StepLint
	}
	return StepLint
}

// targetArgs renders the plan's targets for a command line: "./..." for a
// full plan, the validated affected targets otherwise.
func targetArgs(plan CIRequirementPlan) (string, error) {
	if plan.Selection.Resolved() == TargetSelectionFull || len(plan.Targets) == 0 {
		return "./...", nil
	}
	parts := make([]string, 0, len(plan.Targets))
	for _, t := range plan.Targets {
		if t == TargetAll || !targetPattern.MatchString(string(t)) {
			return "", cascade.Newf(cascade.KindInvalidInput, "ci: local executor: unsafe target %q", t)
		}
		parts = append(parts, string(t))
	}
	return strings.Join(parts, " "), nil
}

// populate runs the controller's module populate-and-verify step.
func (l *localExecutor) populate(ctx context.Context, treeDir string) error {
	if err := os.MkdirAll(l.d.ModCache, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: local executor: creating the module cache")
	}
	env := append(append([]string(nil), l.d.Environ...), "GOMODCACHE="+l.d.ModCache, "GOTOOLCHAIN=local")
	fn := l.d.Populate
	if fn == nil {
		fn = goModPopulate
	}
	return fn(ctx, treeDir, env)
}

// goModPopulate is the default controller step: `go mod download`, then
// `go mod verify`, in dir with env. A tree without go.mod needs neither.
func goModPopulate(ctx context.Context, dir string, env []string) error {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return nil
	}
	for _, args := range [][]string{{"mod", "download"}, {"mod", "verify"}} {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			kind := cascade.KindUnavailable
			if args[1] == "verify" {
				kind = cascade.KindIntegrity
			}
			return cascade.Wrapf(kind, err, "ci: local executor: go %s refused the run: %s", args[1], strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// cleanRoomEnv builds a run's environment: AllowedEnv over the ambient
// snapshot, then the forced clean-room values, which win over everything.
func cleanRoomEnv(ambient, extraKeys []string, runDir, modCache string) ([]string, error) {
	home, tmp := filepath.Join(runDir, "home"), filepath.Join(runDir, "tmp")
	cache, gopath := filepath.Join(runDir, "gocache"), filepath.Join(runDir, "gopath")
	for _, d := range []string{home, tmp, cache, gopath} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: local executor: creating the run environment directories")
		}
	}
	if err := disableGoTelemetry(runtime.GOOS, home); err != nil {
		return nil, err
	}
	forced := map[string]string{
		"HOME": home, "USERPROFILE": home, "TMPDIR": tmp, "TMP": tmp, "TEMP": tmp,
		"GOCACHE": cache, "GOPATH": gopath, "GOMODCACHE": modCache,
		"GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
	}
	for k, v := range runConfigEnv(home) {
		forced[k] = v
	}
	var out []string
	for _, kv := range AllowedEnv(ambient, extraKeys) {
		name, _, _ := strings.Cut(kv, "=")
		if _, isForced := forced[name]; !isForced {
			out = append(out, kv)
		}
	}
	names := make([]string, 0, len(forced))
	for n := range forced {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, n+"="+forced[n])
	}
	return out, nil
}

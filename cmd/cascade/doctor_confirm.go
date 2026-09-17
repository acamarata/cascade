// Purpose: `cascade doctor --fix`'s confirmation prompt, split out of
//
//	doctor_mounts.go under its own 300-line cap (Art.10.3).
//
// Constraints: a --fix run that cannot ask refuses rather than assuming
//
//	consent; see confirmDoctorFix's own doc comment.
//
// SPORT: cmd/cascade/doctor (CHG) — P1-E16-W4-S34-T4.
package main

import (
	"bufio"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// confirmDoctorFix gates `--fix`. It runs BEFORE any check, so a refusal
// costs nothing and nothing is mutated on the way to it.
//
// Under CASCADE_NO_INPUT=1 it is a hard error rather than a silent
// proceed: --fix flushes a queue an operator may still want to review,
// and a non-interactive caller cannot be asked. Otherwise it asks once,
// and anything but an explicit yes refuses.
func confirmDoctorFix(cmd *cobra.Command, deps doctorDeps, f *doctorFlags) error {
	if !f.fix {
		return nil
	}
	if deps.Getenv("CASCADE_NO_INPUT") == "1" {
		return cascade.New(cascade.KindElevationRequired,
			"cascade doctor --fix needs an interactive confirmation and CASCADE_NO_INPUT=1 is set; "+
				"run it without CASCADE_NO_INPUT, or resolve each finding with the command its remediation names")
	}
	if deps.ConfirmFix == nil {
		return cascade.New(cascade.KindUnavailable,
			"cascade doctor --fix: no confirmation reader is configured; refusing to remediate unasked")
	}
	ok, err := deps.ConfirmFix(cmd)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.New(cascade.KindPermissionDenied,
			"cascade doctor --fix: not confirmed; nothing was changed")
	}
	return nil
}

// confirmOnStdin reads a single y/N answer from the command's own input
// stream. Anything but an explicit "y"/"yes" is a no, including an
// unreadable stream: a confirmation nobody gave is not a yes.
func confirmOnStdin(cmd *cobra.Command) (bool, error) {
	if _, err := cmd.ErrOrStderr().Write([]byte(
		"cascade doctor --fix will flush the pending quarantine queue. Continue? [y/N] ")); err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "cascade doctor: could not prompt for confirmation")
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// providerHealthSourceAdapter adapts openProviderStorage
// (provider_health_cmd.go) to doctor.ProviderHealthSource: each call
// opens the registry+health storage fresh and closes it before
// returning, mirroring how `cascade provider list/test/health` already
// treat that storage as per-invocation rather than long-lived.
type providerHealthSourceAdapter struct {
	// paths is the PathProvider the caller injected. It exists because
	// this adapter USED to discard it and call productionProviderDeps()
	// unconditionally, which meant `cascade doctor` always read the
	// DEFAULT provider store no matter what CASCADE_HOME resolved to. A
	// diagnostic that reports on a different store than the one the
	// operator configured is worse than no diagnostic, and it also leaked
	// real databases into the redirected HOME the hygiene lane asserts is
	// clean, which is how it was found.
	paths runtime.PathProvider
}

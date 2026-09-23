package main

// Purpose (this file): mounts hookpacks.CompletionGateDoctorCheck into
//   `cascade doctor` (R-16.47; CR-B round-3 D4 -- a doctor check nothing
//   runs is dead code). `cascade doctor` builds a fresh process per
//   invocation and never calls wireCompletionHookPack (the DAEMON-only
//   composition root, cmd/cascade/hooks.go), so hookpacks.DefaultRegistry
//   is always empty here -- mounting the check against it would report a
//   permanent, meaningless FAIL (testonly-allow.json's own retired entry
//   names this exact gap). This file never touches DefaultRegistry: it
//   builds its OWN local *hookpacks.HookRegistry and populates it only
//   when the CC harness's real installed settings file already carries
//   hookpacks.MethodCompletionCheck -- an exported, pure string constant
//   naming the completion-gate hook's own RPC method, present in every
//   rendered Stop/TaskCompleted command (completion_hook_command.go) --
//   the one signal doctor CAN observe without a live daemon.
// Inputs: the CC harness's real settings file (plugins/claude.HostPaths,
//   the same production path resolution `cascade init`'s own install
//   uses).
// Outputs: CompletionGateDoctorCheck mounted, StatusError only when the
//   settings file EXISTS but does not name the method; StatusOK when it
//   does, AND when no settings file exists at all -- mirroring
//   internal/context's own harnessResult ("a machine with no harness
//   installed is a machine cascade has nothing to wire into yet ...
//   reporting that as a problem would make every server install fail its
//   own doctor"; TestDoctorIsMountedOnRoot pins the same "healthy install
//   on a bare $HOME never errors" contract this file must not break).
// Constraints: CompletionCheck/ResolveJob on the placeholder gate/
//   resolver below are never called by CompletionGateDoctorCheck.Run (it
//   only inspects the registry's own registered descriptors) -- this is
//   a structural registration guard, never a stand-in for a security
//   seam.
// SPORT: cmd/cascade/doctor (CompletionGateDoctorCheck mount), CR-B D4.

import (
	"context"
	"os"
	"strings"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/claude"
)

// productionCompletionGateCheck builds the mounted doctor.Check.
func productionCompletionGateCheck() *hookpacks.CompletionGateDoctorCheck {
	return hookpacks.NewCompletionGateDoctorCheck(installedCompletionGateRegistry(), nil)
}

// installedCompletionGateRegistry builds a throwaway local HookRegistry
// (never hookpacks.DefaultRegistry) and registers the real
// completion-gate pack on it -- which makes CompletionGateDoctorCheck.Run
// report OK -- unless the harness's settings file EXISTS and is missing
// the RPC method: an installed-but-unwired harness is the one real
// FAIL case; no settings file at all is "nothing installed yet", OK.
func installedCompletionGateRegistry() *hookpacks.HookRegistry {
	reg := hookpacks.NewHookRegistry()
	if completionGateMissingFromAnInstalledSettingsFile() {
		return reg
	}
	_ = hookpacks.RegisterCompletionHookPack(reg, noopCompletionGate{}, noopJobResolver{})
	return reg
}

// completionGateMissingFromAnInstalledSettingsFile reads the CC harness's
// real settings file (plugins/claude.HostPaths) and reports true only
// when that file exists, is readable, and does not name the
// completion-gate hook's RPC method. A missing/unreadable file (no
// harness installed, or the environment could not be resolved) reports
// false -- not this check's fault, per the file-level Outputs note.
func completionGateMissingFromAnInstalledSettingsFile() bool {
	paths, err := claude.HostPaths()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(paths.Settings)
	if err != nil {
		return false
	}
	return !strings.Contains(string(raw), hookpacks.MethodCompletionCheck)
}

// noopCompletionGate/noopJobResolver satisfy RegisterCompletionHookPack's
// non-nil guard only. CompletionGateDoctorCheck.Run never calls either
// method (see the file-level Constraints note).
type noopCompletionGate struct{}

func (noopCompletionGate) CompletionCheck(context.Context, string) (bool, string, error) {
	return false, "", cascade.New(cascade.KindUnavailable, "doctor: completion gate not available outside the daemon")
}

type noopJobResolver struct{}

func (noopJobResolver) ResolveJob(context.Context, hookpacks.CompletionHookPayload) (string, error) {
	return "", cascade.New(cascade.KindUnavailable, "doctor: job resolver not available outside the daemon")
}

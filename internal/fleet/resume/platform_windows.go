//go:build windows

// Purpose: task 6, windows's own half — the sole entry point a Windows
//   caller needs, since Windows has no daemon (06-FORGE-SPEC §2 tier-2)
//   and therefore never has a journal.Store to construct a Manager
//   with in the first place.
// Inputs: none.
// Outputs: ErrWindowsUnsupported.
// Constraints: real code, no CASCADE-ALLOW, no stub. See resume.go's
//   RefuseOnGOOS doc comment for why the portable, cross-platform-tested
//   refusal logic itself lives there rather than in this
//   implicitly-windows-only-by-filename file (Go treats any "_windows.go"
//   suffix as build-tag-equivalent to windows regardless of an explicit
//   tag, which this file also carries per the established repo
//   convention — census_windows.go, sampler_windows.go).
// SPORT: internal.fleet.resume.ResumeManager/ADDED (P1-E13-W3-S27-T2).

package resume

// RefuseDaemonlessResume is Windows's sole resume entry point: any caller
// on this platform (D/S-07.T4's headless one-shot path included) gets the
// same typed refusal a full Manager.Run would reach via RefuseOnGOOS,
// without first having to construct a journal.Store this platform never
// has a daemon-backed one of.
func RefuseDaemonlessResume() error {
	return ErrWindowsUnsupported
}

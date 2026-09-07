//go:build windows

package census

// Purpose: windows's enumerateRaw — tier-2 per 06-FORGE-SPEC §2: no real
//
//	process enumeration is implemented here, so Enumerate always refuses
//	via ErrUnsupportedPlatform rather than returning a fabricated or
//	empty result. An empty list would mean "no processes found", which is
//	a lie on a platform this package cannot actually inspect; a typed
//	refusal is the truth (AGENT-BRIEF.md's non-negotiable for this
//	ticket).
//
// Constraints: this file must compile under GOOS=windows and import no
//
//	Windows-only syscall package at the top level (satisfied trivially:
//	it imports nothing). Mirrors
//	internal/fleet/governor/sampler_windows.go's collectMetrics precedent
//	exactly.
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

// enumerateRaw implements this package's platform seam for windows. It
// always refuses: see the file-level Purpose note above.
func enumerateRaw() ([]rawProcess, error) {
	return nil, ErrUnsupportedPlatform
}

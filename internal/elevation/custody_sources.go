// Purpose: own the production custody source registry.
// Inputs: platform and directory. Outputs: source descriptors.
// Constraints: Linux PAM is not protected custody. The linux passphrase
// presence source is registered by keystore_presence_tty_linux.go and is
// absent from every other platform. SPORT: elevation sources.

package elevation

import (
	"runtime"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PresenceAuditor records a presence enrolment before the key file is
// replaced. Begin writes the intent record and returns a finish callback
// that records the outcome (nil = the enrolment completed). A Begin error
// refuses the enrolment.
type PresenceAuditor interface {
	Begin(fingerprint, kind string) (finish func(error), err error)
}

// presenceSource and presenceEnroll are set by the linux-only presence
// files; they stay nil on darwin and windows, so no presence code is built
// there.
var (
	presenceSource *CustodySource
	presenceEnroll func(dir string, trust Backend, clock Clock, aud PresenceAuditor) (string, error)
)

// EnrollPresence creates or rotates the linux presence key: it prompts on
// the controlling terminal, audits before the file is replaced, and writes
// the trust record. It returns the new key's fingerprint. Any other
// platform refuses.
func EnrollPresence(dataDir string, trust Backend, clock Clock, aud PresenceAuditor) (string, error) {
	if presenceEnroll == nil {
		return "", cascade.New(cascade.KindUnsupported, "elevation: the passphrase presence tier is available on linux only")
	}
	return presenceEnroll(dataDir, trust, clock, aud)
}

// DefaultSources registers the supported production custody sources.
func DefaultSources() []CustodySource { return sourcesForOS(runtime.GOOS, NewKeystore) }
func sourcesForOS(goos string, platform func() ElevationKeystore) []CustodySource {
	if goos == "windows" {
		return nil
	}
	sources := []CustodySource{}
	if goos == "darwin" {
		sources = append(sources, CustodySource{Tier: CustodyPlatform, Name: "platform", Open: func(string) (ElevationKeystore, bool) {
			ks := platform()
			return ks, ks != nil && ks.IsAvailable()
		}})
	}
	if goos == "linux" {
		sources = append(sources, CustodySource{Tier: CustodyFile, Name: "linux-pam", Open: func(string) (ElevationKeystore, bool) { ks := platform(); return ks, ks != nil && ks.IsAvailable() }})
		if presenceSource != nil {
			sources = append(sources, *presenceSource)
		}
	}
	sources = append(sources, CustodySource{Tier: CustodyFile, Name: "file", Open: func(dir string) (ElevationKeystore, bool) {
		return fileCustodyKeystore(dir), dir != "" && (fileKeyExists(dir) || devkeysBuild)
	}})
	return sources
}

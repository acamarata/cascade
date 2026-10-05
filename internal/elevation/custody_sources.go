// Purpose: own the production custody source registry.
// Inputs: platform and directory. Outputs: source descriptors.
// Constraints: Linux PAM is not protected custody. SPORT: elevation sources.

package elevation

import "runtime"

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
	}
	sources = append(sources, CustodySource{Tier: CustodyFile, Name: "file", Open: func(dir string) (ElevationKeystore, bool) {
		return fileCustodyKeystore(dir), dir != "" && (fileKeyExists(dir) || devkeysBuild)
	}})
	return sources
}

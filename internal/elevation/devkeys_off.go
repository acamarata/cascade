//go:build !devkeys

package elevation

const devkeysBuild = false

// DevkeysBuild reports development file-signing support.
func DevkeysBuild() bool                           { return devkeysBuild }
func fileCustodyKeystore(string) ElevationKeystore { return nil }

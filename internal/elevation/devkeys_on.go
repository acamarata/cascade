//go:build devkeys

package elevation

const devkeysBuild = true

// DevkeysBuild reports development file-signing support.
func DevkeysBuild() bool                               { return devkeysBuild }
func fileCustodyKeystore(dir string) ElevationKeystore { return NewFileKeystore(dir) }

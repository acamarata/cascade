// Package pbdmode reads the per-Epic model mode (local or hosted) of a PBD
// phase.
package pbdmode

import "strings"

// Slug returns the lower-case part of an Epic code after its first dash, so
// Slug("P3-UI") is "ui". A code with no dash yields "".
func Slug(epic string) string {
	_, rest, ok := strings.Cut(epic, "-")
	if !ok {
		return ""
	}
	return strings.ToLower(rest)
}

// Package crunch holds the crunch run helpers.
package crunch

// ClampWidth returns width clamped to the range [0, limit].
// If width exceeds limit, it returns limit instead of erroring.
// Zero or negative widths are clamped to 0.
// A clamped width is never counted as a no-green pass.
func ClampWidth(width, limit int) int {
	if width < 0 {
		return 0
	}
	if width > limit {
		return limit
	}
	return width
}

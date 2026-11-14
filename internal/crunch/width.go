package crunch

// ClampWidth returns width clamped to the range [0, max].
// If width exceeds max, it returns max instead of erroring.
// Zero or negative widths are clamped to 0.
// A clamped width is never counted as a no-green pass.
func ClampWidth(width, max int) int {
	if width < 0 {
		return 0
	}
	if width > max {
		return max
	}
	return width
}

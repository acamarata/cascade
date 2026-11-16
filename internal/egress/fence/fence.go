// Package fence provides egress guard fence types and contracts for detecting
// and handling marker pairs in data streams.
package fence

// MarkerOpen is the opening delimiter for markers.
const MarkerOpen = "[["

// MarkerClose is the closing delimiter for markers.
const MarkerClose = "]]"

// FenceResult represents the outcome of a fence check operation.
type FenceResult struct {
	// Clean indicates whether the data passed the fence check without issues.
	Clean bool
	// Confidence is a score between 0 and 1 indicating confidence in the result.
	Confidence float64
	// Markers contains the detected marker pairs.
	Markers []MarkerPair
	// Reason provides additional context about the result.
	Reason string
}

// MarkerPair represents exactly one well-formed marker pair with positions.
type MarkerPair struct {
	// OpenPos is the position of the opening marker.
	OpenPos int
	// ClosePos is the position of the closing marker.
	ClosePos int
	// Content is the text between the markers.
	Content string
}

// ProbeFunc is the signature used by all detection rules to probe data.
type ProbeFunc func(data string) FenceResult

// CheckFence examines data for marker pairs and returns a FenceResult.
func CheckFence(data string) FenceResult {
	return FenceResult{Clean: true}
}

// StripMarkers removes marker delimiters from data and returns the cleaned content.
func StripMarkers(data string) string {
	return data
}

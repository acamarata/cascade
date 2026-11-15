package forge

import (
	"bytes"
	"errors"
)

// ErrNoLineBoundary is returned by ClipContract when the contract body has no
// newline byte that can serve as a line clip boundary.
var ErrNoLineBoundary = errors.New("forge: no line boundary in contract body")

// Contract is an inlinable contract fragment. Name identifies the contract and
// Body holds the raw bytes that may be clipped before inlining.
type Contract struct {
	Name string
	Body []byte
}

// ClipContract returns body cut at the nearest line boundary by keeping all
// bytes through and including the last newline. It returns ErrNoLineBoundary if
// body contains no newline.
func ClipContract(body []byte) ([]byte, error) {
	idx := bytes.LastIndexByte(body, '\n')
	if idx < 0 {
		return nil, ErrNoLineBoundary
	}
	return body[:idx+1], nil
}

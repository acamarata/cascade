package crunch

import (
	"errors"
	"io"
	"os"
)

// MarkerSizeCap is the maximum allowed size in bytes for a marker file.
const MarkerSizeCap = 65536

// ErrD1378 indicates that a marker file was rejected because it is oversized, non-regular, or otherwise invalid.
var ErrD1378 = errors.New("D1378: marker rejected")

// ReadMarker reads a marker file from the given path.
// It verifies that the path points to a regular file within the size cap.
// Oversized markers or non-regular files return ErrD1378 immediately without truncation.
// An empty file returns an empty byte slice and a nil error.
func ReadMarker(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, ErrD1378
	}

	if !info.Mode().IsRegular() {
		return nil, ErrD1378
	}

	if info.Size() > MarkerSizeCap {
		return nil, ErrD1378
	}

	if info.Size() == 0 {
		return []byte{}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, ErrD1378
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, ErrD1378
	}

	return data, nil
}

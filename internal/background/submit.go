// Package background validates lanejob submission payloads before processing.
package background

import (
	"errors"
	"fmt"
)

const (
	// maxSubmitFiles is the D3404 limit on host files in one submission.
	maxSubmitFiles = 8

	// maxSubmitFileSize is the D3404 per-file limit: 256 KiB.
	maxSubmitFileSize = 256 * 1024

	// maxSubmitTotalSize is the D3404 total limit: 1 MiB.
	maxSubmitTotalSize = 1024 * 1024
)

// ErrD3404Validation is returned when a submission violates D3404 limits.
var ErrD3404Validation = errors.New("D3404 validation error")

// SubmitFile is one host file described by a lanejob.submit payload.
type SubmitFile struct {
	// Name is the host file name.
	Name string

	// Size is the file size in bytes.
	Size int
}

// Submit validates a lanejob.submit payload against D3404 limits.
// It accepts at most 8 files, each at most 256 KiB, with a total at most 1 MiB.
func Submit(files []SubmitFile) error {
	if err := validateFileCount(files); err != nil {
		return err
	}

	total := 0
	for _, file := range files {
		if err := validateFileSize(file); err != nil {
			return err
		}
		total += file.Size
	}

	return validateTotalSize(total)
}

// validateFileCount enforces the D3404 maximum number of host files.
func validateFileCount(files []SubmitFile) error {
	if len(files) > maxSubmitFiles {
		return fmt.Errorf(
			"%w: too many files: got %d, limit %d",
			ErrD3404Validation,
			len(files),
			maxSubmitFiles,
		)
	}
	return nil
}

// validateFileSize enforces the D3404 per-file size limit.
func validateFileSize(file SubmitFile) error {
	if file.Size > maxSubmitFileSize {
		return fmt.Errorf(
			"%w: file %q too large: got %d bytes, limit %d",
			ErrD3404Validation,
			file.Name,
			file.Size,
			maxSubmitFileSize,
		)
	}
	return nil
}

// validateTotalSize enforces the D3404 total submission size limit.
func validateTotalSize(total int) error {
	if total > maxSubmitTotalSize {
		return fmt.Errorf(
			"%w: total too large: got %d bytes, limit %d",
			ErrD3404Validation,
			total,
			maxSubmitTotalSize,
		)
	}
	return nil
}

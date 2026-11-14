package background

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
)

// ErrInvalidEscalation is returned when the escalation input is invalid.
var ErrInvalidEscalation = errors.New("invalid escalation")

// ErrEscalationWrite is returned when a file I/O error occurs during escalation.
var ErrEscalationWrite = errors.New("escalation write failed")

// WriteEscalate appends a compact JSON representation of o to the file at path.
func WriteEscalate(path string, o Outcome) (err error) {
	if path == "" {
		return ErrInvalidEscalation
	}
	switch o.Status {
	case "done":
		// successful no-op even if Detail is nonempty
	case "needs-owner":
		if o.Detail == "" {
			return ErrInvalidEscalation
		}
	default:
		return ErrInvalidEscalation
	}

	data, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEscalationWrite, err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEscalationWrite, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("%w: %v", ErrEscalationWrite, cerr)
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEscalationWrite, err)
	}
	// Windows reports 0666 for every file, so POSIX group/other bits carry no meaning there.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: existing file has group/other permissions", ErrEscalationWrite)
	}

	n, err := f.Write(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEscalationWrite, err)
	}
	if n < len(data) {
		return fmt.Errorf("%w: %v", ErrEscalationWrite, io.ErrShortWrite)
	}
	return nil
}

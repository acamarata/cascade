// Package violation (clean_writer_target.go) proves the gate does NOT
// false-positive on legitimate output: fmt.Fprintln to a real io.Writer
// (never os.Stdout/os.Stderr), os.Stdin, and non-print fmt helpers must all
// pass clean.
package violation

import (
	"bytes"
	"fmt"
	"os"
)

func WriteToBuffer(buf *bytes.Buffer, msg string) {
	fmt.Fprintln(buf, msg)
}

func ReadStdin() (string, error) {
	var line string
	_, err := fmt.Fscanln(os.Stdin, &line)
	return line, err
}

func FormatOnly(msg string) string {
	return fmt.Sprintf("wrapped: %s", msg)
}

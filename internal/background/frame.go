package background

import (
	"errors"
	"fmt"
	"strings"
)

// Tag is the autopilot tag prepended to supervisor messages.
const Tag = "[autopilot]"

// FrameMessage prepends Tag and a turn frame to the body.
// It returns an error if n < 1, m < 1, or n > m.
func FrameMessage(n, m int, body string) (string, error) {
	if n < 1 || m < 1 || n > m {
		return "", errors.New("bad turn")
	}
	return fmt.Sprintf("%s turn %d of %d\n%s", Tag, n, m, body), nil
}

// ParseMessage extracts n, m and body from a framed message.
// It returns an error if the tag is missing, the frame is missing, or the turn numbers are invalid.
func ParseMessage(s string) (n, m int, body string, err error) {
	if !strings.HasPrefix(s, Tag) {
		return 0, 0, "", errors.New("missing tag")
	}

	idx := strings.Index(s, "\n")
	if idx == -1 {
		return 0, 0, "", errors.New("missing frame")
	}

	frame := s[:idx]
	body = s[idx+1:]

	prefix := Tag + " turn "
	if !strings.HasPrefix(frame, prefix) {
		return 0, 0, "", errors.New("missing frame")
	}

	rest := frame[len(prefix):]
	parts := strings.Split(rest, " of ")
	if len(parts) != 2 {
		return 0, 0, "", errors.New("missing frame")
	}

	_, err = fmt.Sscanf(parts[0], "%d", &n)
	if err != nil {
		return 0, 0, "", errors.New("bad turn")
	}
	_, err = fmt.Sscanf(parts[1], "%d", &m)
	if err != nil {
		return 0, 0, "", errors.New("bad turn")
	}

	if n < 1 || m < 1 || n > m {
		return 0, 0, "", errors.New("bad turn")
	}

	return n, m, body, nil
}

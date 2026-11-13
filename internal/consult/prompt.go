package consult

import (
	"errors"
	"fmt"
	"strings"
)

// SubChatPrompt composes a system prompt for a sub-chat.
// It includes the sub-chat's rootPath from the root and appends every
// manager's context, outermost first.
// It returns an error containing D6129 if rootPath is empty.
func SubChatPrompt(rootPath string, contexts []string) (string, error) {
	if rootPath == "" {
		return "", errors.New("D6129: empty root path")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sub-chat root path: %s\n", rootPath)
	for _, c := range contexts {
		b.WriteString(FormatContext(c))
	}
	return b.String(), nil
}

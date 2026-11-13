package consult

import "fmt"

// FormatContext formats a single context string for inclusion in a prompt.
func FormatContext(c string) string {
	return fmt.Sprintf("Context: %s\n", c)
}

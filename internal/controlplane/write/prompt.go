package write

import "fmt"

// InjectPrompt returns a system prompt note containing the current date,
// the finishing rule, and the source-limit instruction verbatim. It returns
// an error referencing D2729 when the finishing rule is empty.
func InjectPrompt(date, finishingRule string) (string, error) {
	if finishingRule == "" {
		return "", fmt.Errorf("D2729: finishing rule must not be empty")
	}

	return "System prompt note:\n" +
		"date: " + date + "\n" +
		"finishing rule: " + finishingRule + "\n" +
		"You must never invent sources.\n", nil
}

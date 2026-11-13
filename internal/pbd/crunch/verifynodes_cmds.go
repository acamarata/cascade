package crunch

import "strings"

// validateCommands checks the command list for validity.
// It returns false and ErrVerifyNodes if commands are empty,
// whitespace-only, or contain exact duplicates.
// If any command starts with tokens "go" and "test", it returns true, nil.
// Otherwise it returns false, nil.
func validateCommands(commands []string) (bool, error) {
	if len(commands) == 0 {
		return false, ErrVerifyNodes
	}

	seen := make(map[string]bool)
	requiresGoTest := false

	for _, cmd := range commands {
		if strings.TrimSpace(cmd) == "" {
			return false, ErrVerifyNodes
		}
		if seen[cmd] {
			return false, ErrVerifyNodes
		}
		seen[cmd] = true

		fields := strings.Fields(cmd)
		if len(fields) >= 2 && fields[0] == "go" && fields[1] == "test" {
			requiresGoTest = true
		}
	}

	return requiresGoTest, nil
}

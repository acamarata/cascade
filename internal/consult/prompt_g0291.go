// Package consult provides prompt generation and validation utilities.
package consult

import (
	"errors"
	"strings"
)

const identifierRule = "Retain every number, file path, and ID from the source text. Never drop or alter identifiers."

// FrontRolePrompt returns the front role system prompt, including the identifier retention rule.
func FrontRolePrompt() string {
	return "you are a rewriter\n\n" + identifierRule
}

// ValidatePrompt checks that p contains the identifier retention rule.
// It returns an error with code D4177 if the rule is missing.
func ValidatePrompt(p string) error {
	if !strings.Contains(p, identifierRule) {
		return errors.New("D4177: identifier retention rule missing")
	}
	return nil
}

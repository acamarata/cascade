// Package decide provides decision types and validation.
package decide

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// Question represents a decision question.
type Question struct {
	Kind  string
	Text  string
	Rules []Rule
}

// Rule is a function that evaluates a question.
type Rule func(q Question) (answer string, matched bool)

// Verdict represents the result of a decision.
type Verdict struct {
	Answer     string
	Confidence float64
	ID         string
}

// YES is a positive verdict.
const YES = "YES"

// NO is a negative verdict.
const NO = "NO"

// ESCALATE is an escalated verdict.
const ESCALATE = "ESCALATE"

// ErrBadQuestion is returned when a question is invalid.
var ErrBadQuestion = errors.New("bad question")

// Decide evaluates a question and returns a verdict.
func Decide(q Question) (Verdict, error) {
	if strings.TrimSpace(q.Kind) == "" {
		return Verdict{}, ErrBadQuestion
	}
	if strings.TrimSpace(q.Text) == "" {
		return Verdict{}, ErrBadQuestion
	}
	if strings.Contains(q.Kind, "\x00") {
		return Verdict{}, ErrBadQuestion
	}
	if strings.Contains(q.Text, "\x00") {
		return Verdict{}, ErrBadQuestion
	}
	for _, r := range q.Rules {
		if r == nil {
			return Verdict{}, ErrBadQuestion
		}
	}

	id := computeID(q.Kind, q.Text)

	for _, r := range q.Rules {
		answer, matched := r(q)
		if matched {
			if answer != YES && answer != NO && answer != ESCALATE {
				return Verdict{}, ErrBadQuestion
			}
			return Verdict{
				Answer: answer,
				ID:     id,
			}, nil
		}
	}

	return Verdict{ID: id}, nil
}

func computeID(kind, text string) string {
	data := []byte(kind + "\x00" + text)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

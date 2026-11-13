// Package docket provides structured checks for planning dockets.
package docket

import (
	"errors"
	"fmt"
	"strings"
)

// warnCodeD4633 is the warning code emitted for docket hygiene problems.
const warnCodeD4633 = "D4633"

// minWaveWidth is the smallest acceptable wave width.
const minWaveWidth = 8

// Warning is a structured warning emitted by docket checks.
type Warning struct {
	// Code identifies the warning class.
	Code string

	// Subject names the object that caused the warning.
	Subject string

	// Message describes the detected problem.
	Message string
}

// TicketInfo carries the ticket fields checked by WarnMissing.
type TicketInfo struct {
	// Context is the explanatory context attached to a ticket.
	Context string
}

// EpicInfo carries the epic fields checked by WarnMissing.
type EpicInfo struct {
	// CriticalReviewPlan reports whether the epic has a CRITICAL review plan.
	CriticalReviewPlan bool
}

// WarnInput is the input for WarnMissing.
type WarnInput struct {
	// Ticket is the ticket under inspection.
	Ticket TicketInfo

	// Epic is the epic under inspection.
	Epic EpicInfo

	// WaveWidth is the width of the wave under inspection.
	WaveWidth int
}

// WarnMissing returns D4633 warnings when the input ticket lacks context,
// the epic lacks a CRITICAL review plan, or the wave width is below 8.
// It returns an error for nil input and makes no network or external calls.
func WarnMissing(in *WarnInput) ([]Warning, error) {
	if in == nil {
		return nil, errors.New("docket: nil warning input")
	}

	var warnings []Warning

	if strings.TrimSpace(in.Ticket.Context) == "" {
		warnings = append(warnings, Warning{
			Code:    warnCodeD4633,
			Subject: "ticket",
			Message: "ticket is missing context",
		})
	}

	if !in.Epic.CriticalReviewPlan {
		warnings = append(warnings, Warning{
			Code:    warnCodeD4633,
			Subject: "epic",
			Message: "epic is missing a CRITICAL review plan",
		})
	}

	if in.WaveWidth < minWaveWidth {
		warnings = append(warnings, Warning{
			Code:    warnCodeD4633,
			Subject: "wave",
			Message: fmt.Sprintf("wave width %d is below the minimum %d", in.WaveWidth, minWaveWidth),
		})
	}

	return warnings, nil
}

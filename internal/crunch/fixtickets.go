// Package crunch provides status reconciliation and fix ticket generation.
package crunch

import "fmt"

// FixTicket represents a ticket created to address a failed check.
type FixTicket struct {
	// Title is a short summary of the issue.
	Title string
	// Body contains details about the failure and suggested fixes.
	Body string
}

// YieldFixTickets maps each failed (red) PR check into a gateway fix ticket.
func YieldFixTickets(failed []CheckResult) []FixTicket {
	var tickets []FixTicket

	for _, r := range failed {
		if r.State != "red" {
			continue
		}
		ticket := FixTicket{
			Title: fmt.Sprintf("Fix %s check failure", r.Name),
			Body:  fmt.Sprintf("Check %q failed with reason: %s", r.Name, r.Reason),
		}
		tickets = append(tickets, ticket)
	}

	return tickets
}

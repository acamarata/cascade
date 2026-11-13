package docket

import "errors"

// Incident represents a docket incident with a kind, subject, and extension references.
type Incident struct {
	Kind    string
	Subject string
	Refs    []string
}

// NewProgressStallIncident creates a progress_stall incident with the given subject.
// It returns an error and a zero Incident if the subject is empty.
func NewProgressStallIncident(subject string) (Incident, error) {
	if subject == "" {
		return Incident{}, errors.New("progress_stall: empty subject")
	}
	return Incident{
		Kind:    "progress_stall",
		Subject: subject,
		Refs:    []string{"P2-NSC-09", "P2-DEV-08", "D4926"},
	}, nil
}

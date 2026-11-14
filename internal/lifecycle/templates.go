package lifecycle

import (
	"bytes"
	"errors"
	"text/template"
)

// TemplateKind identifies a specific type of lifecycle template.
type TemplateKind string

const (
	// KindSeeded is the template for the seeded phase.
	KindSeeded TemplateKind = "seeded"
	// KindPlanConfirm is the template for the plan confirmation phase.
	KindPlanConfirm TemplateKind = "plan_confirm"
	// KindTicket is the template for the ticket phase.
	KindTicket TemplateKind = "ticket"
	// KindCrunch is the template for the crunch phase.
	KindCrunch TemplateKind = "crunch"
	// KindComplete is the template for the complete phase.
	KindComplete TemplateKind = "complete"
	// KindCreated is the template for the created phase.
	KindCreated TemplateKind = "created"
)

// TemplateData holds the information passed to a lifecycle template.
type TemplateData struct {
	ProjectID string
	TicketID  string
	Decisions []string
	AskBlock  string
	Progress  []string
}

var registry = make(map[TemplateKind]*template.Template)

// Register stores a template in the registry for later retrieval by Render.
func Register(kind TemplateKind, tmpl *template.Template) error {
	if kind == "" {
		return errors.New("template kind cannot be empty")
	}
	if tmpl == nil {
		return errors.New("template cannot be nil")
	}
	registry[kind] = tmpl
	return nil
}

// Render executes the template associated with the given kind.
// It returns an error if the kind is unregistered or if rendering fails.
func Render(kind TemplateKind, data TemplateData) (string, error) {
	tmpl, ok := registry[kind]
	if !ok {
		return "", errors.New("unknown template kind: " + string(kind))
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

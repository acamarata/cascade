package lifecycle

import "text/template"

func init() {
	Register(KindSeeded, template.Must(template.New("seeded").Parse("{{.ProjectID}} created, let's plan.")))
	Register(KindCreated, template.Must(template.New("created").Parse("Project {{.ProjectID}} created.")))
	Register(KindTicket, template.Must(template.New("ticket").Parse("Ticket {{.TicketID}} progress. {{range .Progress}}{{.}}. {{end}}")))
	Register(KindCrunch, template.Must(template.New("crunch").Parse("Crunch update for {{.ProjectID}}. This is a periodic update.")))
}

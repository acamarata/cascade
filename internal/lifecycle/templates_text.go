package lifecycle

import "text/template"

var funcMap = template.FuncMap{
	"inc": func(i int) int { return i + 1 },
}

// init registers all text templates for the lifecycle phases.
func init() {
	Register(KindSeeded, template.Must(template.New("seeded").Parse("{{.ProjectID}} created, let's plan.")))
	Register(KindCreated, template.Must(template.New("created").Parse("Project {{.ProjectID}} created.")))
	Register(KindTicket, template.Must(template.New("ticket").Parse("Ticket {{.TicketID}} progress. {{range .Progress}}{{.}}. {{end}}")))
	Register(KindCrunch, template.Must(template.New("crunch").Parse("Crunch update for {{.ProjectID}}. This is a periodic update.")))
	Register(KindPlanConfirm, template.Must(template.New("plan_confirm").Parse(
		"Plan ready for {{.ProjectID}}.\n{{range .Progress}}{{.}}\n{{end}}```ask\n{{.AskBlock}}\n```",
	)))
	Register(KindComplete, template.Must(template.New("complete").Funcs(funcMap).Parse(
		"Phase complete for {{.ProjectID}}.\n{{range .Progress}}{{.}}\n{{end}}Decisions made for you:\n{{range $i, $d := .Decisions}}{{inc $i}}. {{$d}}\n{{end}}Next: the following phase is seeded automatically.{{if .AskBlock}}\n```ask\n{{.AskBlock}}\n```{{end}}",
	)))
}

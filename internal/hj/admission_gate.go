package hj

// Ticket represents the resource requirements for a job or task.
type Ticket struct {
	ModelReqs   int
	CPU         int
	RAM         int
	VerifySlots int
}

// Budgets holds the available resources across different domains.
type Budgets struct {
	Model  int
	CPU    int
	RAM    int
	Verify int
}

// Admit checks whether the given ticket can be accommodated within the provided budgets.
// It returns true and an empty string if all budgets have sufficient room.
// Otherwise, it returns false and the name of the first exhausted budget (model, exec or verify).
func Admit(t Ticket, b Budgets) (bool, string) {
	if b.Model < t.ModelReqs {
		return false, "model"
	}
	if b.CPU < t.CPU || b.RAM < t.RAM {
		return false, "exec"
	}
	if b.Verify < t.VerifySlots {
		return false, "verify"
	}
	return true, ""
}

// Package pbd (status.go): the `pbd status`/`pbd board` read surfaces
// (N/S-29.T4) over the T1 projected PEWS state (internal/pews.Row), plus
// the mirrored `plugin.pbd.status`/`plugin.pbd.board` JSON-RPC shape
// (07-CLI-COMMAND-TREE.md's mirror rule) and their MCP tool counterparts.
// Inputs: RunCommand's args (tree root, optional phase) for the CLI path;
// a JSON {root, phase} object for StatusRPC/BoardRPC/DispatchTool.
// ReadRowsFromStore takes a pkg/provider.Store directly, for a caller
// that already has one. Outputs: StatusReport/BoardReport, built by
// BuildStatusReport/BuildBoardReport from a []pews.Row regardless of
// source — the ONE read model this ticket's HOW section requires.
// Constraints: imports pkg/** and internal/pews ONLY (Art.10.2); no
// invented status field or board layout beyond pews.Row's own fields.
// SPORT: plugins/pbd status/board (ADD) — P1-E14-W3-S29-T4.
package pbd

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// statusCommandName is "summary", not the contract's literal "status":
// pkg/plugin/validate.go's rule R5 (outside this ticket's files_scope)
// rejects any provides.commands entry named "status" — a reserved utility
// verb, checked by bare name regardless of namespace. "board" is not
// reserved. statusRPCMethod/statusToolName below keep the contract's
// literal "plugin.pbd.status"/"cascade_plugin_pbd_status" — R5 only
// constrains CommandSpec.Name, not RPCMethod or ToolSpec.Name.
const (
	statusCommandName = "summary"
	boardCommandName  = "board"
	statusRPCMethod   = "plugin." + pluginID + ".status"
	boardRPCMethod    = "plugin." + pluginID + "." + boardCommandName
)

// statusToolName and boardToolName are the MCP tool names the ratified
// mirror rule derives from the RPC method names: "cascade_" plus the
// method with its dots turned to underscores (07-CLI-COMMAND-TREE.md
// §Mirror rule, e.g. "provider.list" -> "cascade_provider_list").
const (
	statusToolName = "cascade_plugin_pbd_status"
	boardToolName  = "cascade_plugin_pbd_board"
)

// StatusReport is `pbd status`'s result: a summary count over the
// projected PEWS rows for one phase.
type StatusReport struct {
	Phase        string         `json:"phase"`
	Total        int            `json:"total"`
	ByWeight     map[string]int `json:"by_weight"`
	ByModelClass map[string]int `json:"by_model_class"`
}

// BoardColumn is one BoardReport column: every row sharing one model
// class, sorted by canonical id.
type BoardColumn struct {
	ModelClass string        `json:"model_class"`
	Tickets    []BoardTicket `json:"tickets"`
}

// BoardTicket is one board entry — the projected row's own id/title/
// weight fields, nothing invented.
type BoardTicket struct {
	CanonicalID string `json:"canonical_id"`
	Title       string `json:"title"`
	Weight      string `json:"weight"`
}

// BoardReport is `pbd board`'s result: every projected row for one phase,
// grouped into columns by model class (the closest existing field to a
// workflow stage; no new status/state field is introduced).
type BoardReport struct {
	Phase   string        `json:"phase"`
	Columns []BoardColumn `json:"columns"`
}

// RunStatus loads the PEWS tree rooted at root (ticket ids named under
// phase, or DefaultPhase when phase is empty) and summarizes it. Like
// RunValidate/RunLint, it reads the tree directly rather than through a
// pkg/provider.Store: RunCommand's fixed (ctx, name, args []string)
// signature carries no channel for injecting a Store, and no daemon
// composition root exists yet to supply one (see this file's own doc
// comment and internal/build/testonly-allow.json). ReadRowsFromStore
// below is the literal T1-projected-state read path for a caller that
// already holds a Store.
func RunStatus(root, phase string) (StatusReport, error) {
	rows, resolvedPhase, err := rowsFromTree(root, phase)
	if err != nil {
		return StatusReport{}, err
	}
	return BuildStatusReport(resolvedPhase, rows), nil
}

// RunBoard is RunStatus's board-shaped counterpart.
func RunBoard(root, phase string) (BoardReport, error) {
	rows, resolvedPhase, err := rowsFromTree(root, phase)
	if err != nil {
		return BoardReport{}, err
	}
	return BuildBoardReport(resolvedPhase, rows), nil
}

// rowsFromTree loads root/phase and converts every ticket into the same
// pews.Row shape internal/pews/projector.go's rowFromRecord produces —
// duplicated here (rowFromRecord is unexported and projector.go is
// outside this ticket's files_scope) rather than reread from a store, so
// `pbd status`/`pbd board` work standalone without a running daemon.
func rowsFromTree(root, phase string) ([]pews.Row, string, error) {
	if phase == "" {
		phase = DefaultPhase
	}
	tree, err := pews.NewStore(root, phase).Load()
	if err != nil {
		return nil, "", err
	}
	rows := make([]pews.Row, 0, len(tree.Tickets))
	for _, tr := range tree.Tickets {
		rows = append(rows, pews.Row{
			CanonicalID: tr.CanonicalID,
			ID:          tr.Ticket.ID,
			Title:       tr.Ticket.Title,
			Weight:      string(tr.Ticket.Weight),
			ModelClass:  string(tr.Ticket.ModelClass),
			Phase:       tree.Phase,
		})
	}
	return rows, phase, nil
}

// ReadRowsFromStore scans store's projected rows out of
// pews.DefaultProjectionNamespace (the T1 projector's namespace, empty
// prefix — this ticket adds no second key scheme) and decodes each as a
// pews.Row, sorted by CanonicalID for deterministic output. It is the
// literal T1-files-to-database-projection read path: a daemon composition
// root (or a test proving that path) supplies store directly, since
// neither RunCommand nor DispatchTool's fixed signatures carry one.
func ReadRowsFromStore(ctx context.Context, store provider.Store) ([]pews.Row, error) {
	it, err := store.Scan(ctx, pews.DefaultProjectionNamespace, "")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "pbd: status: scan projected rows")
	}
	defer func() { _ = it.Close() }()
	var rows []pews.Row
	for it.Next(ctx) {
		var r pews.Row
		if uerr := json.Unmarshal(it.Value(), &r); uerr != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, uerr, "pbd: status: decode projected row")
		}
		rows = append(rows, r)
	}
	if ierr := it.Err(); ierr != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, ierr, "pbd: status: scan projected rows")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CanonicalID < rows[j].CanonicalID })
	return rows, nil
}

// BuildStatusReport aggregates rows into a StatusReport for phase.
func BuildStatusReport(phase string, rows []pews.Row) StatusReport {
	report := StatusReport{Phase: phase, Total: len(rows), ByWeight: map[string]int{}, ByModelClass: map[string]int{}}
	for _, r := range rows {
		report.ByWeight[r.Weight]++
		report.ByModelClass[r.ModelClass]++
	}
	return report
}

// BuildBoardReport groups rows into a BoardReport for phase, one column
// per distinct model class, columns sorted alphabetically (deterministic;
// not the mech/build/heavy/review/arbiter execution order, which
// internal/pews keeps unexported) and tickets within a column sorted by
// canonical id.
func BuildBoardReport(phase string, rows []pews.Row) BoardReport {
	byClass := map[string][]BoardTicket{}
	for _, r := range rows {
		byClass[r.ModelClass] = append(byClass[r.ModelClass], BoardTicket{
			CanonicalID: r.CanonicalID, Title: r.Title, Weight: r.Weight,
		})
	}
	classes := make([]string, 0, len(byClass))
	for c := range byClass {
		classes = append(classes, c)
	}
	sort.Strings(classes)

	columns := make([]BoardColumn, 0, len(classes))
	for _, c := range classes {
		tickets := byClass[c]
		sort.Slice(tickets, func(i, j int) bool { return tickets[i].CanonicalID < tickets[j].CanonicalID })
		columns = append(columns, BoardColumn{ModelClass: c, Tickets: tickets})
	}
	return BoardReport{Phase: phase, Columns: columns}
}

// statusRequest is StatusRPC/BoardRPC/DispatchTool's shared JSON request
// shape: a tree root (required) and an optional phase override.
type statusRequest struct {
	Root  string `json:"root"`
	Phase string `json:"phase"`
}

// decodeStatusRequest parses data as a statusRequest, refusing an empty
// payload or an empty root.
func decodeStatusRequest(data []byte) (statusRequest, error) {
	if len(data) == 0 {
		return statusRequest{}, cascade.New(cascade.KindInvalidInput, "pbd: status/board: a request body is required")
	}
	var req statusRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return statusRequest{}, cascade.Wrap(cascade.KindInvalidInput, err, "pbd: status/board: malformed request")
	}
	if req.Root == "" {
		return statusRequest{}, cascade.New(cascade.KindInvalidInput, "pbd: status/board: root must not be empty")
	}
	return req, nil
}

// StatusRPC is plugin.pbd.status's handler body: shaped exactly like
// internal/rpc.HandlerFunc (ctx, json.RawMessage) (any, error) by
// structural convention, without importing internal/rpc (Art.10.2) — a
// composition root registers it directly (Go's assignability rule accepts
// an unnamed-func-typed value for a named func-type parameter with the
// same underlying type), and status_test.go's TestStatusBoardMirroredMethods
// proves exactly that with a real internal/rpc.Registry.
func StatusRPC(_ context.Context, params json.RawMessage) (any, error) {
	req, err := decodeStatusRequest(params)
	if err != nil {
		return nil, err
	}
	return RunStatus(req.Root, req.Phase)
}

// BoardRPC is StatusRPC's board-shaped counterpart.
func BoardRPC(_ context.Context, params json.RawMessage) (any, error) {
	req, err := decodeStatusRequest(params)
	if err != nil {
		return nil, err
	}
	return RunBoard(req.Root, req.Phase)
}

// DispatchTool services the two policy-filtered MCP tools this ticket
// adds (statusToolName, boardToolName), decoding input as StatusRPC/
// BoardRPC's own {root, phase} request shape and returning the
// JSON-encoded report. Any other name refuses, matching pbd.go's prior
// no-tools behavior for DispatchIntent. Defined here rather than pbd.go
// purely for that file's own 300-line budget (Art.10.5).
func (handlers) DispatchTool(ctx context.Context, name string, input []byte) ([]byte, error) {
	switch name {
	case statusToolName:
		report, err := StatusRPC(ctx, input)
		if err != nil {
			return nil, err
		}
		return json.Marshal(report)
	case boardToolName:
		report, err := BoardRPC(ctx, input)
		if err != nil {
			return nil, err
		}
		return json.Marshal(report)
	default:
		return nil, cascade.Newf(cascade.KindUnsupported, "pbd: no tool named %q", name)
	}
}

// runStatusOrBoardCommand is RunCommand's status/board entry point: args[0]
// is the tree root (required), args[1] an optional phase override,
// matching validate/lint's own arg convention. It returns nil on a
// successful read and the *cascade.Error otherwise — RunCommand's fixed
// (ctx, name, args []string) error return carries no data channel, so the
// report itself is reached through RunStatus/RunBoard above, exported
// exactly as RunValidate/RunLint are for the same reason.
func runStatusOrBoardCommand(name string, args []string) error {
	if len(args) == 0 || args[0] == "" {
		return cascade.Newf(cascade.KindInvalidInput, "pbd %s: a tree root argument is required", name)
	}
	phase := ""
	if len(args) > 1 {
		phase = args[1]
	}
	if name == statusCommandName {
		_, err := RunStatus(args[0], phase)
		return err
	}
	_, err := RunBoard(args[0], phase)
	return err
}

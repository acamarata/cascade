// Package codex is the agent driver for the codex App Server: it launches
// `codex app-server` and speaks its stdio JSON-RPC (one JSON object per
// line, methods and fields from the committed schema under
// testdata/protocol/1/schema).
//
// One job is one child: initialize, the initialized notification,
// thread/start and turn/start, then notifications until turn/completed.
// Every notification reaches the job's event log as an AgentEvent whose
// Native field carries the method and the filtered raw frame, mapped or
// not. Every server request the schema names is answered exactly once:
// approvals (command, file change, permissions) are surfaced on
// ApprovalRequests and answered only through ResolveApproval, a timeout or
// a cancel; the driver never answers one on its own. Requests for user
// input, dynamic tools, credentials or attestation get a JSON-RPC error.
//
// The sandbox is read-only for ToolPolicy none and readonly and
// workspace-write for all; the approval policy is always on-request with
// the user as reviewer. No flag or parameter can select a wider sandbox or
// an automatic approval. The deprecated `codex mcp-server` launch path is
// refused with ErrForbiddenLaunchPath.
//
// Egress: the child talks to its vendor over its own TLS connections; this
// driver does not see or inspect that traffic. It sees only the stdio
// frames, which pass through the job's OutputFilter before anything is
// stored or surfaced.
package codex

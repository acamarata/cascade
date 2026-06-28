//! JSON Schema definitions for all MCP tools (returned by `tools/list`).

use super::types::McpTool;

// ── Core tool definitions ─────────────────────────────────────────────────────

pub(super) fn cascade_read_tool() -> McpTool {
    McpTool {
        name: "cascade.read".into(),
        description: "Read a cascade tier instruction file (CASCADE.md / CLAUDE.md) from the specified tier. Returns the full file text.".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["tier"],
            "additionalProperties": false,
            "properties": {
                "tier": {
                    "type": "string",
                    "description": "Tier identifier: 'gci', 'asi', 'ppc', 'prc', or 'pac'",
                    "enum": ["gci", "asi", "ppc", "prc", "pac"]
                },
                "project": {
                    "type": "string",
                    "description": "Project name (required for ppc/prc/pac tiers)"
                }
            }
        }),
    }
}

pub(super) fn cascade_search_tool() -> McpTool {
    McpTool {
        name: "cascade.search".into(),
        description: "Hybrid RAG search (FTS5 + dense vector + RRF) across the watched corpus. Returns ranked chunks with citations (file, line, score).".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["query"],
            "additionalProperties": false,
            "properties": {
                "query": {
                    "type": "string",
                    "description": "Natural-language search query",
                    "minLength": 1
                },
                "limit": {
                    "type": "integer",
                    "description": "Maximum results to return (1–20)",
                    "default": 10,
                    "minimum": 1,
                    "maximum": 20
                },
                "project": {
                    "type": "string",
                    "description": "Project name filter (e.g. 'nself')"
                },
                "tier": {
                    "type": "string",
                    "description": "Cascade tier filter (e.g. 'gci', 'prc')"
                },
                "strategy": {
                    "type": "string",
                    "enum": ["hybrid_rrf", "pure_fts", "pure_vec"],
                    "default": "hybrid_rrf",
                    "description": "Retrieval strategy"
                }
            }
        }),
    }
}

pub(super) fn cascade_search_codebase_tool() -> McpTool {
    McpTool {
        name: "cascade.search_codebase".into(),
        description: "Code-aware search using tree-sitter function-level index. Returns matching functions/classes with exact file:line citations.".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["query", "project"],
            "additionalProperties": false,
            "properties": {
                "query": {
                    "type": "string",
                    "minLength": 1
                },
                "project": {
                    "type": "string",
                    "description": "Project name to search within"
                },
                "limit": {
                    "type": "integer",
                    "default": 10,
                    "minimum": 1,
                    "maximum": 20
                },
                "lang": {
                    "type": "string",
                    "description": "Language filter (e.g. 'rust', 'typescript')"
                }
            }
        }),
    }
}

pub(super) fn cascade_inbox_list_tool() -> McpTool {
    McpTool {
        name: "cascade.inbox.list".into(),
        description: "List PCI inbox messages for a project.".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["project"],
            "additionalProperties": false,
            "properties": {
                "project": {
                    "type": "string",
                    "description": "Project name (e.g. 'nself')"
                },
                "unread_only": {
                    "type": "boolean",
                    "default": false
                }
            }
        }),
    }
}

pub(super) fn cascade_inbox_send_tool() -> McpTool {
    McpTool {
        name: "cascade.inbox.send".into(),
        description: "Send a PCI message to a project inbox.".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["target", "subject", "body", "type", "priority"],
            "additionalProperties": false,
            "properties": {
                "target": {
                    "type": "string",
                    "description": "Target project name"
                },
                "subject": {
                    "type": "string"
                },
                "body": {
                    "type": "string"
                },
                "type": {
                    "type": "string",
                    "enum": ["bug", "enhancement", "question", "info"],
                    "description": "Message type"
                },
                "priority": {
                    "type": "string",
                    "enum": ["critical", "high", "medium", "low"]
                }
            }
        }),
    }
}

pub(super) fn cascade_master_lists_tool() -> McpTool {
    McpTool {
        name: "cascade.master_lists".into(),
        description: "Read a project master list (routes, components, tables, endpoints, CLI commands, env vars, etc.).".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["project", "kind"],
            "additionalProperties": false,
            "properties": {
                "project": {
                    "type": "string"
                },
                "kind": {
                    "type": "string",
                    "enum": ["routes", "components", "tables", "endpoints", "cli", "env", "hooks", "utils"],
                    "description": "Master list kind"
                }
            }
        }),
    }
}

pub(super) fn cascade_memory_read_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.read".into(),
        description: "Read a project memory file (decisions, lessons, patterns).".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["project", "file"],
            "additionalProperties": false,
            "properties": {
                "project": {
                    "type": "string"
                },
                "file": {
                    "type": "string",
                    "enum": ["decisions.md", "lessons.md", "patterns.md"]
                }
            }
        }),
    }
}

pub(super) fn cascade_memory_write_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.write".into(),
        description: "Append an entry to a project memory file. Requires an authenticated connection (HMAC token).".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["project", "file", "content"],
            "additionalProperties": false,
            "properties": {
                "project": {
                    "type": "string"
                },
                "file": {
                    "type": "string",
                    "enum": ["decisions.md", "lessons.md", "patterns.md"]
                },
                "content": {
                    "type": "string",
                    "description": "Markdown text to append",
                    "minLength": 1
                }
            }
        }),
    }
}

pub(super) fn cascade_context_slice_tool() -> McpTool {
    McpTool {
        name: "cascade.context_slice".into(),
        description: "Return a token-budgeted, deduplicated, windowed context slice from the \
                       local knowledge base for injection into a harness prompt. Applies \
                       shell-output compression, within-window chunk dedup, and optionally \
                       cross-session dedup."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["query", "budget_tokens"],
            "additionalProperties": false,
            "properties": {
                "query": {
                    "type": "string",
                    "description": "Natural-language query to retrieve context for",
                    "minLength": 1
                },
                "budget_tokens": {
                    "type": "integer",
                    "description": "Maximum token count to include in the returned context slice",
                    "minimum": 256,
                    "maximum": 32768,
                    "default": 4096
                },
                "session_id": {
                    "type": "string",
                    "description": "Opaque harness session ID for cross-session dedup. Omit to disable cross-session dedup."
                },
                "include_shell": {
                    "type": "boolean",
                    "default": false,
                    "description": "If true, include shell-output compression pass on any embedded shell snippets."
                },
                "project": {
                    "type": "string",
                    "description": "Optional project name filter (e.g. 'nself')"
                }
            }
        }),
    }
}

pub(super) fn cascade_provide_harness_context_tool() -> McpTool {
    McpTool {
        name: "cascade.provide_harness_context".into(),
        description: "ONE-CALL harness bootstrap (E-P7-01). A harness calls this once on startup \
                       and receives everything it needs: the resolved 6-tier merged instructions \
                       for the given cwd, the applicable policy set, harness-specific config, \
                       the MCP server coordinates, and the live active-work context (active sprint \
                       tickets + open kanban tasks). No per-tier file reconciliation required \
                       in the harness — cascade owns all merging. (E-P8-07: active_work field)"
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["harness", "cwd"],
            "additionalProperties": false,
            "properties": {
                "harness": {
                    "type": "string",
                    "description": "Harness identifier",
                    "enum": ["claude-code", "opencode", "codex", "cursor", "aider"]
                },
                "cwd": {
                    "type": "string",
                    "description": "Absolute path of the working directory the harness is operating in",
                    "minLength": 1
                }
            }
        }),
    }
}

// ── PBD tool definitions (E-P8-04) ───────────────────────────────────────────

pub(super) fn cascade_get_current_tool() -> McpTool {
    McpTool {
        name: "cascade.get_current".into(),
        description: "Return current.yaml active pointers: active phase/epic/wave/sprint and \
                       active ticket IDs. Bounded to <=200 tokens for session-boot use. \
                       Accepts an optional phases_root override; defaults to CWD walk."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

pub(super) fn cascade_update_ticket_status_tool() -> McpTool {
    McpTool {
        name: "cascade.update_ticket_status".into(),
        description: "Transition a ticket to a new status. Validates the transition against the \
                       TicketStatus enum (planned|queue|active|review|blocked|done|archived) \
                       and writes an event to events.jsonl. Returns the new status on success."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["ticket_id", "status"],
            "additionalProperties": false,
            "properties": {
                "ticket_id": {
                    "type": "string",
                    "description": "Ticket ID (e.g. T-P1-E01-W01-S01-01)",
                    "minLength": 1
                },
                "status": {
                    "type": "string",
                    "enum": ["planned", "queue", "active", "review", "blocked", "done", "archived"],
                    "description": "Target status"
                },
                "note": {
                    "type": "string",
                    "description": "Optional note (required when status=blocked)"
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

pub(super) fn cascade_append_event_tool() -> McpTool {
    McpTool {
        name: "cascade.append_event".into(),
        description: "Append a raw PBD event record to events.jsonl. The record is validated \
                       for required fields (ts, actor, level, id, from, to) before appending. \
                       Use cascade.update_ticket_status for ticket transitions — this tool is \
                       for custom/manual events."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["event"],
            "additionalProperties": false,
            "properties": {
                "event": {
                    "type": "object",
                    "description": "PBD event object: {ts, actor, level, id, from, to, note?}",
                    "required": ["actor", "level", "id", "from", "to"],
                    "properties": {
                        "ts": { "type": "string", "description": "ISO 8601 timestamp (auto-set if omitted)" },
                        "actor": { "type": "string" },
                        "level": {
                            "type": "string",
                            "enum": ["phase", "epic", "wave", "sprint", "ticket", "step"]
                        },
                        "id": { "type": "string" },
                        "from": { "type": "string" },
                        "to": { "type": "string" },
                        "note": { "type": "string" }
                    }
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

pub(super) fn cascade_get_sprint_tool() -> McpTool {
    McpTool {
        name: "cascade.get_sprint".into(),
        description: "Return the sprint YAML for the given sprint ID. Searches the active phase \
                       tree for the sprint. Returns sprint metadata including tickets list."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["sprint_id"],
            "additionalProperties": false,
            "properties": {
                "sprint_id": {
                    "type": "string",
                    "description": "Sprint ID (e.g. S01)",
                    "minLength": 1
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

pub(super) fn cascade_read_phase_status_tool() -> McpTool {
    McpTool {
        name: "cascade.read_phase_status".into(),
        description: "Return a compact status summary for the given phase (or all non-archived \
                       phases if phase_id is omitted). Includes ticket counts per status."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "phase_id": {
                    "type": "string",
                    "description": "Phase ID (e.g. p1). Omit to get all active phases."
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

pub(super) fn cascade_list_tickets_tool() -> McpTool {
    McpTool {
        name: "cascade.list_tickets".into(),
        description: "List tickets across the phase tree with optional filters. Returns ticket \
                       ID, title, status, sprint, and repo. Filters are AND-combined."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "status": {
                    "type": "string",
                    "enum": ["planned", "queue", "active", "review", "blocked", "done", "archived"],
                    "description": "Filter by ticket status"
                },
                "sprint_id": {
                    "type": "string",
                    "description": "Filter to a specific sprint ID"
                },
                "phase_id": {
                    "type": "string",
                    "description": "Filter to a specific phase ID"
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

pub(super) fn cascade_check_routes_tool() -> McpTool {
    McpTool {
        name: "cascade.check_routes".into(),
        description: "Run a check over api-routes.yaml if present in the project. Returns \
                       per-route ok/fail status. HTTP client is injectable for tests (no \
                       network required in test mode)."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "routes_file": {
                    "type": "string",
                    "description": "Absolute path to api-routes.yaml. Defaults to CWD/.claude/docs/api-routes.yaml."
                },
                "base_url": {
                    "type": "string",
                    "description": "Base URL override for all route checks (e.g. http://localhost:3000)"
                },
                "timeout_ms": {
                    "type": "integer",
                    "description": "Per-route timeout in ms (default: 5000)",
                    "default": 5000,
                    "minimum": 100,
                    "maximum": 30000
                }
            }
        }),
    }
}

pub(super) fn cascade_scan_inbox_tool() -> McpTool {
    McpTool {
        name: "cascade.scan_inbox".into(),
        description: "Drain .claude/inbox (or <ai-folder>/inbox) for CI/CD error messages and \
                       PCI messages. Returns summaries of all .md files found: filename, first \
                       line (subject), and size. Does not delete files."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "inbox_path": {
                    "type": "string",
                    "description": "Absolute path to the inbox directory. Defaults to CWD auto-discovery."
                },
                "project": {
                    "type": "string",
                    "description": "Project name for ~/Sites/{project}/.claude/inbox/ resolution."
                }
            }
        }),
    }
}

// ── Security tool definitions ─────────────────────────────────────────────────

pub(super) fn cascade_security_secret_scan_tool() -> McpTool {
    McpTool {
        name: "cascade.security.secret_scan".into(),
        description: "Scan git-tracked files (or a single file) for leaked secrets and \
                       credentials. Returns structured findings with kind, file, line, \
                       redacted preview, and severity. Client-side paths (browser-visible \
                       code) are flagged at high severity even when the pattern baseline \
                       is lower."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "path": {
                    "type": "string",
                    "description": "Absolute path to a file or directory to scan. \
                                    Defaults to the current working directory. \
                                    For directories, only git-tracked files are scanned."
                }
            }
        }),
    }
}

pub(super) fn cascade_security_audit_tool() -> McpTool {
    McpTool {
        name: "cascade.security.audit".into(),
        description: "Run a dependency audit against the project at `path`. \
                       Auto-detects the ecosystem (Cargo → cargo audit, \
                       npm/pnpm → pnpm audit, Python → pip-audit). Returns \
                       advisories with id, package, severity, and title. \
                       Returns tool_available:false when the audit tool is not \
                       installed — never errors the MCP call."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "path": {
                    "type": "string",
                    "description": "Absolute path to the project directory. \
                                    Defaults to the current working directory."
                }
            }
        }),
    }
}

// ── RAG-08 memory tool definitions ────────────────────────────────────────────

pub(super) fn cascade_memory_remember_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.remember".into(),
        description: "Insert an episode or fact into the specified memory namespace. \
                       Namespace must be 'personal' (requires opt_in=true), 'meta', \
                       or 'dev-<project-slug>'. Personal namespace is blocked without opt_in."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["namespace", "content"],
            "additionalProperties": false,
            "properties": {
                "namespace": {
                    "type": "string",
                    "description": "'personal', 'meta', or 'dev-<slug>'"
                },
                "content": {
                    "type": "string",
                    "description": "Text content of the episode to remember",
                    "minLength": 1
                },
                "opt_in": {
                    "type": "boolean",
                    "description": "Required to access 'personal' namespace. Default false.",
                    "default": false
                }
            }
        }),
    }
}

pub(super) fn cascade_memory_recall_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.recall".into(),
        description: "Query memory in the specified namespace. Returns matching episodes \
                       and facts ordered by relevance. Namespace-scoped — no cross-namespace \
                       leakage. Personal namespace requires opt_in=true."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["namespace", "query"],
            "additionalProperties": false,
            "properties": {
                "namespace": {
                    "type": "string",
                    "description": "'personal', 'meta', or 'dev-<slug>'"
                },
                "query": {
                    "type": "string",
                    "description": "Natural language query to search memory",
                    "minLength": 1
                },
                "k": {
                    "type": "integer",
                    "description": "Maximum results to return (1–50, default 10)",
                    "default": 10,
                    "minimum": 1,
                    "maximum": 50
                },
                "opt_in": {
                    "type": "boolean",
                    "description": "Required to access 'personal' namespace. Default false.",
                    "default": false
                }
            }
        }),
    }
}

pub(super) fn cascade_memory_forget_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.forget".into(),
        description: "Archive or delete a memory episode or fact by id within a namespace. \
                       Personal namespace requires opt_in=true. Episodes are hard-deleted; \
                       facts are soft-archived (archived=true)."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["namespace", "id"],
            "additionalProperties": false,
            "properties": {
                "namespace": {
                    "type": "string",
                    "description": "'personal', 'meta', or 'dev-<slug>'"
                },
                "id": {
                    "type": "string",
                    "description": "UUID of the episode or fact to forget"
                },
                "kind": {
                    "type": "string",
                    "enum": ["episode", "fact"],
                    "default": "episode",
                    "description": "Whether to forget an episode or a fact"
                },
                "opt_in": {
                    "type": "boolean",
                    "description": "Required to access 'personal' namespace. Default false.",
                    "default": false
                }
            }
        }),
    }
}

pub(super) fn cascade_memory_search_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.search".into(),
        description: "Semantic search within a memory namespace. Returns episodes and facts \
                       matching the query. Identical to recall but named for discoverability. \
                       Personal namespace requires opt_in=true."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["namespace", "query"],
            "additionalProperties": false,
            "properties": {
                "namespace": {
                    "type": "string",
                    "description": "'personal', 'meta', or 'dev-<slug>'"
                },
                "query": {
                    "type": "string",
                    "description": "Natural language search query",
                    "minLength": 1
                },
                "k": {
                    "type": "integer",
                    "description": "Maximum results (1–50, default 10)",
                    "default": 10,
                    "minimum": 1,
                    "maximum": 50
                },
                "opt_in": {
                    "type": "boolean",
                    "description": "Required to access 'personal' namespace. Default false.",
                    "default": false
                }
            }
        }),
    }
}

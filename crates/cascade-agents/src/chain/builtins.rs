//! First-party built-in ChainFlow examples.
//!
//! Purpose: provide ready-made chains for common use-cases.
//! Inputs: none (constructors).
//! Outputs: ChainFlow values.
//! Constraints: no side effects; pure constructors.

use crate::spec::AgentRole;

use super::types::{ChainFlow, ChainStep};

/// Returns the built-in `research → summarise → draft` chain.
pub fn builtin_research_summarize_draft() -> ChainFlow {
    ChainFlow {
        id: "research-summarize-draft".into(),
        name: "Research → Summarize → Draft".into(),
        description: Some("Research a topic with an agent, summarise the findings with an LLM, then draft a blog post.".into()),
        steps: vec![
            ChainStep::AgentTask {
                role: AgentRole::Researcher,
                goal: "Research the following topic in depth: {{topic}}".into(),
                output: "raw_research".into(),
            },
            ChainStep::Prompt {
                template: "Summarise the following research concisely:\n{{raw_research}}".into(),
                inputs: vec!["raw_research".into()],
                output: "summary".into(),
            },
            ChainStep::Prompt {
                template: "Draft a professional blog post based on this summary:\n{{summary}}".into(),
                inputs: vec!["summary".into()],
                output: "draft".into(),
            },
        ],
    }
}

/// Returns the built-in `triage → branch → respond` chain.
pub fn builtin_triage_branch_respond() -> ChainFlow {
    ChainFlow {
        id: "triage-branch-respond".into(),
        name: "Triage → Branch → Respond".into(),
        description: Some("Triage incoming text, branch on urgency, draft a response accordingly.".into()),
        steps: vec![
            ChainStep::Prompt {
                template: "Classify the urgency of the following: {{input}}\nReply with just 'high' or 'low'.".into(),
                inputs: vec!["input".into()],
                output: "urgency".into(),
            },
            ChainStep::Branch {
                cond: "urgency".into(),
                then_step: Box::new(ChainStep::Prompt {
                    template: "Draft an urgent escalation response for: {{input}}".into(),
                    inputs: vec!["input".into()],
                    output: "response".into(),
                }),
                else_step: Some(Box::new(ChainStep::Prompt {
                    template: "Draft a standard acknowledgement for: {{input}}".into(),
                    inputs: vec!["input".into()],
                    output: "response".into(),
                })),
            },
        ],
    }
}

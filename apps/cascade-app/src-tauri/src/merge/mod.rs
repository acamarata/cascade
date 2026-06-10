// merge — Cascade AI merge engine subsystem.
//
// Purpose: Implements the Phase 4 (MergeContent) stage of the onboarding wizard.
//   Reads legacy instruction content from disk, sends it to the connected AI
//   provider, and persists the approved merge result to ~/.cascade/CASCADE.md.
//
// Module layout:
//   types       — MergeSection, MergeResult, MergeConflict, and related types (T-P3-E03-23)
//   reader      — Reads UTF-8 content from legacy files for AI context (T-P3-E03-24)
//   ai_service  — Calls the connected AI provider and parses MergeResult (T-P3-E03-25)
//   writer      — Writes approved sections to ~/.cascade/CASCADE.md (T-P3-E03-26)
//   conflict    — Detects overlapping or contradicting sections (T-P3-E03-30)
//
// SPORT: MASTER-COMPONENTS.md — merge engine subsystem
// Tasks: T-P3-E03-23 through T-P3-E03-30

pub mod ai_service;
pub mod conflict;
pub mod reader;
pub mod types;
pub mod writer;

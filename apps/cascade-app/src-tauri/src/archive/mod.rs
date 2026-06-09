// archive — Cascade legacy tool archive/restore subsystem.
//
// Purpose: Implements the Phase 7 archive and restore pipeline.
//   Tools can be archived (moved to ~/.cascade/legacy/) and restored to their
//   original paths without data loss.
//
// Module layout:
//   types      — ArchiveManifest, ToolArchive, ArchivedFile, and related types (T-15)
//   engine     — Atomic move-not-copy archive engine + manifest writer (T-16)
//   validation — Pre-flight validation: disk space, permissions, conflict checks (T-18)
//   restore    — Restore engine: manifest-driven move back to original paths (T-19)
//
// SPORT: MASTER-COMPONENTS.md — archive subsystem
// Tasks: T-P3-E03-15 through T-P3-E03-19

pub mod engine;
pub mod restore;
pub mod types;
pub mod validation;

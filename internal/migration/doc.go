// Package migration implements the bulk multi-project instruction-regen
// driver `cascade context sync --project-list <file>` calls
// (P1-E26-W10-S53-T3): reading a project-list file, deduplicating it
// against any already-known project paths, and, for each one, calling
// internal/context's own Discover/MergeTiers/HarnessGenerator pipeline
// directly — this package never re-implements generation, only aggregates
// its result into a drift report and gates whether it gets applied.
package migration

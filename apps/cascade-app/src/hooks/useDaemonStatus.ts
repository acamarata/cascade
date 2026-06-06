/**
 * Purpose: Typed selector hook for daemon connection state. Re-exports the store
 *   selector so consumers get a stable typed surface without importing from store/.
 * Inputs:  None.
 * Outputs: { status, data, lastPing, error, startPolling, stopPolling }
 * Constraints: Delegates entirely to useDaemonStatus from src/store/index.ts.
 * SPORT: MASTER-HOOKS.md — useDaemonStatus, src/hooks/useDaemonStatus.ts
 */

export { useDaemonStatus } from '../store';

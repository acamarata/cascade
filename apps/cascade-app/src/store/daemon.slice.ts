/**
 * Purpose: Daemon connection state slice — tracks connection status, last DaemonStatus
 *   data, polling interval, and provides startPolling/stopPolling actions.
 * Inputs:  ipc.getStatus() from src/lib/ipc/client.ts; called on a 5-second interval.
 * Outputs: DaemonSlice state — status, data, lastPing, error; polling lifecycle actions.
 * Constraints: Interval ref stored outside Zustand state (not serialisable). stopPolling
 *   must be called on component unmount to avoid memory leaks. CascadeIpcError on rejection
 *   sets status to 'error' and stores the error message.
 * SPORT: MASTER-HOOKS.md — useDaemonStatus, src/store/daemon.slice.ts
 */

import type { StateCreator } from 'zustand';

import { ipc } from '../lib/ipc/client';
import type { StatusResult } from '../types/ipc';
import { CascadeIpcError } from '../types/errors';

export type ConnectionStatus = 'connected' | 'connecting' | 'disconnected' | 'error';

export interface DaemonSlice {
  /** Current daemon connection status. */
  status: ConnectionStatus;
  /** Last successful StatusResult from the daemon, or null if never connected. */
  data: StatusResult | null;
  /** Unix ms timestamp of the last successful ping, or null. */
  lastPing: number | null;
  /** Last error message, or null. */
  error: string | null;
  /** Start the 5-second polling interval. Idempotent — does nothing if already polling. */
  startPolling: () => void;
  /** Stop the polling interval. Safe to call if not polling. */
  stopPolling: () => void;
}

// Interval ref lives outside Zustand state — not serialisable.
// Exported for test cleanup only; do not use outside tests.
export let _pollInterval: ReturnType<typeof setInterval> | null = null;

/** Reset the module-level interval ref. For test use only. */
export function _resetPollInterval() {
  if (_pollInterval !== null) {
    clearInterval(_pollInterval);
    _pollInterval = null;
  }
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const createDaemonSlice: StateCreator<any, [['zustand/immer', never]], [], DaemonSlice> = (set) => ({
  status: 'disconnected',
  data: null,
  lastPing: null,
  error: null,

  startPolling: () => {
    // Idempotent guard.
    if (_pollInterval !== null) return;

    // Immediate first poll.
    const poll = () => {
      set((draft: DaemonSlice) => { draft.status = 'connecting'; });
      ipc
        .getStatus()
        .then((result) => {
          set((draft: DaemonSlice) => {
            draft.status = 'connected';
            draft.data = result;
            draft.lastPing = Date.now();
            draft.error = null;
          });
        })
        .catch((err: unknown) => {
          const msg = err instanceof CascadeIpcError ? err.message : String(err);
          set((draft: DaemonSlice) => {
            draft.status = 'error';
            draft.error = msg;
          });
        });
    };

    poll();
    _pollInterval = setInterval(poll, 5_000);
  },

  stopPolling: () => {
    if (_pollInterval !== null) {
      clearInterval(_pollInterval);
      _pollInterval = null;
    }
  },
});

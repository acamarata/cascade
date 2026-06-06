/**
 * Purpose: Bottom status bar — shows daemon connection state, last ping timestamp,
 *   and MCP port when connected.
 * Inputs:  None (reads useDaemonStatus internally).
 * Outputs: <footer role="contentinfo"> landmark at the bottom of the app chrome.
 * Constraints: role="contentinfo" for accessibility. Height h-6.
 *   lastPing may be null before the first successful poll.
 *   data?.mcpPort is optional — only shown when daemon is connected.
 * SPORT: MASTER-COMPONENTS.md — StatusBar
 */

import { useDaemonStatus } from '../../store/index'

export function StatusBar() {
  const { status, lastPing, data } = useDaemonStatus()

  const pingText = lastPing
    ? `Last ping: ${new Date(lastPing).toLocaleTimeString()}`
    : 'No ping yet'

  const mcpText =
    status === 'connected' && data && 'mcpPort' in data && data.mcpPort
      ? `MCP :${data.mcpPort}`
      : null

  return (
    <footer
      role="contentinfo"
      className="flex h-6 shrink-0 items-center gap-4 border-t border-border bg-muted/40 px-4 text-xs text-muted-foreground"
    >
      <span>
        Daemon: <span className="capitalize">{status}</span>
      </span>

      <span aria-label="Last daemon ping">{pingText}</span>

      {mcpText && <span aria-label="MCP server port">{mcpText}</span>}
    </footer>
  )
}

/**
 * Purpose: Audit log format migration check for onboarding wizard.
 * Inputs: Cascade home directory path.
 * Outputs: AuditCheckResult with format classification and rotation capability.
 * Constraints: Uses cascade-audit::verify_chain to detect format-mismatch vs tampering.
 * SPORT: E03-S01-01 — audit log format check step + rotation
 */

import { invoke } from '@tauri-apps/api/core'
import { AuditCheckResult } from '../../features/onboarding/types'

/**
 * Check the audit log format and classify violations.
 *
 * Returns:
 * - auditLogExists: true if ~/.cascade/audit.log exists
 * - formatMismatch: true if ALL records fail self-hash (pre-gate format)
 * - isTampered: true if only some records fail (genuine tampering)
 * - violationCount: number of violations detected
 * - rotated: false initially (true after user chooses to rotate)
 */
export async function checkAuditLogFormat(): Promise<AuditCheckResult> {
  try {
    const result = await invoke<{
      auditLogExists: boolean
      formatMismatch: boolean
      isTampered: boolean
      violationCount: number
    }>('wizard_check_audit_format')

    return {
      ...result,
      rotated: false,
    }
  } catch (err) {
    // If the check fails, treat as no audit log (safe to proceed)
    console.error('Failed to check audit log format:', err)
    return {
      auditLogExists: false,
      formatMismatch: false,
      isTampered: false,
      violationCount: 0,
      rotated: false,
    }
  }
}

/**
 * Rotate the audit log: archive old log to audit.log.pre-p3.<timestamp>
 * and remove the .count sidecar. Next daemon append starts a fresh chain.
 *
 * Returns the archive path on success.
 */
export async function rotateAuditLog(): Promise<string> {
  const archivePath = await invoke<string>('wizard_rotate_audit_log')
  return archivePath
}

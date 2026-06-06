/**
 * Purpose: Skip-to-main-content link — first focusable element in the page;
 *   visible only on focus (keyboard users); links to #main-content landmark.
 * Inputs:  None.
 * Outputs: <a> that is screen-reader/keyboard accessible but visually hidden
 *   until focused. Complies with WCAG 2.1 AA SC 2.4.1 (bypass blocks).
 * Constraints: Must be the first child of AppLayout so it is the first tab stop.
 *   Target element must have id="main-content".
 * SPORT: MASTER-COMPONENTS.md — SkipToMain
 */

export function SkipToMain() {
  return (
    <a
      href="#main-content"
      className={[
        'sr-only focus:not-sr-only',
        'fixed left-2 top-2 z-[9999]',
        'rounded-md bg-background px-4 py-2 text-sm font-medium text-foreground',
        'ring-2 ring-ring focus:outline-none',
      ].join(' ')}
    >
      Skip to main content
    </a>
  )
}

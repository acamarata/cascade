/**
 * Purpose: Onboarding wizard placeholder page — standalone, not wrapped in AppLayout.
 * Inputs:  None.
 * Outputs: <main> with h1 heading for route identification.
 * Constraints: Must remain outside AppLayout (standalone layout per ticket spec).
 * SPORT: MASTER-ROUTES.md — /onboarding
 */

export function OnboardingPage() {
  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <h1 className="text-2xl font-semibold">Onboarding</h1>
    </main>
  );
}

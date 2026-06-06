/**
 * Purpose: App-level React error boundary — catches render errors and shows a recovery UI.
 * Inputs:  children: React.ReactNode; optional onReset callback
 * Outputs: Renders children normally; on error renders a role=alert fallback with reload action.
 * Constraints: Must be a class component — React error boundaries require componentDidCatch.
 *              Uses shadcn Button for the reload action. WCAG 2.1 AA: role=alert + aria-live.
 * SPORT: MASTER-COMPONENTS.md — ErrorBoundary
 */

import React from "react";
import { Button } from "@/components/ui/button";

interface Props {
  children: React.ReactNode;
  /** Optional custom reset handler; defaults to window.location.reload() */
  onReset?: () => void;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

export class ErrorBoundary extends React.Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // Log to console so developers can inspect in DevTools.
    // Error reporting service (Sentry etc.) is post-P3 scope.
    console.error("[ErrorBoundary] Uncaught render error:", error, info.componentStack);
  }

  private handleReset = () => {
    if (this.props.onReset) {
      this.props.onReset();
      this.setState({ hasError: false, error: null });
    } else {
      window.location.reload();
    }
  };

  render() {
    if (!this.state.hasError) {
      return this.props.children;
    }

    return (
      <div
        role="alert"
        aria-live="assertive"
        className="flex min-h-screen flex-col items-center justify-center gap-4 p-8 text-center"
      >
        <h1 className="text-xl font-semibold text-destructive">Something went wrong</h1>
        {this.state.error && (
          <p className="max-w-md text-sm text-muted-foreground">
            {this.state.error.message}
          </p>
        )}
        <Button variant="outline" onClick={this.handleReset}>
          Reload
        </Button>
      </div>
    );
  }
}

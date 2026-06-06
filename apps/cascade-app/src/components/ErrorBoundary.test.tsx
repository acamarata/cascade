// @vitest-environment jsdom
/**
 * Tests for ErrorBoundary — verifies fallback renders when child throws.
 */

import { render, screen } from "@testing-library/react";
import "@testing-library/jest-dom";
import { ErrorBoundary } from "./ErrorBoundary";

// Suppress React's expected error console output during these tests.
const originalConsoleError = console.error;
beforeEach(() => {
  console.error = () => {};
});
afterEach(() => {
  console.error = originalConsoleError;
});

function ThrowingChild({ shouldThrow }: { shouldThrow: boolean }) {
  if (shouldThrow) {
    throw new Error("Test render error");
  }
  return <div>content</div>;
}

test("renders children when no error", () => {
  render(
    <ErrorBoundary>
      <ThrowingChild shouldThrow={false} />
    </ErrorBoundary>,
  );
  expect(screen.getByText("content")).toBeInTheDocument();
});

test("renders role=alert fallback when child throws", () => {
  render(
    <ErrorBoundary>
      <ThrowingChild shouldThrow={true} />
    </ErrorBoundary>,
  );
  expect(screen.getByRole("alert")).toBeInTheDocument();
  expect(screen.getByText("Test render error")).toBeInTheDocument();
});

test("fallback contains a Reload button", () => {
  render(
    <ErrorBoundary>
      <ThrowingChild shouldThrow={true} />
    </ErrorBoundary>,
  );
  expect(screen.getByRole("button", { name: /reload/i })).toBeInTheDocument();
});

test("calls onReset when Reload is clicked", async () => {
  const onReset = vi.fn();
  const { getByRole } = render(
    <ErrorBoundary onReset={onReset}>
      <ThrowingChild shouldThrow={true} />
    </ErrorBoundary>,
  );
  getByRole("button", { name: /reload/i }).click();
  expect(onReset).toHaveBeenCalledTimes(1);
});

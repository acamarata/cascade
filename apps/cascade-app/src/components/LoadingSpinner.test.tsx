// @vitest-environment jsdom
/**
 * Tests for LoadingSpinner / SuspenseFallback.
 */

import { render, screen } from "@testing-library/react";
import "@testing-library/jest-dom";
import { LoadingSpinner, SuspenseFallback } from "./LoadingSpinner";

test("renders with default aria-label=Loading", () => {
  render(<LoadingSpinner />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
});

test("renders with custom label", () => {
  render(<LoadingSpinner label="Please wait" />);
  expect(screen.getByRole("status", { name: "Please wait" })).toBeInTheDocument();
});

test("SuspenseFallback is an alias of LoadingSpinner", () => {
  render(<SuspenseFallback />);
  expect(screen.getByRole("status")).toBeInTheDocument();
});

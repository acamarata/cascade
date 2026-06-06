/**
 * Purpose: Unit tests for CommandPalette component, useCommandPalette hook, and
 *   the CommandRegistry singleton — covering render, keyboard toggle, and item selection.
 * SPORT: MASTER-COMPONENTS.md — CommandPalette
 */

import { fireEvent, render, renderHook, screen } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { globalRegistry } from "../lib/commands/registry";
import { useCommandPalette } from "../hooks/useCommandPalette";
import { CommandPalette } from "./CommandPalette";

// jsdom lacks ResizeObserver (used by Radix Dialog) and matchMedia (used by cmdk).
// Stub both before any tests run.
beforeAll(() => {
  window.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  };

  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
});

// Clear the global registry after each test to prevent inter-test pollution.
afterEach(() => {
  globalRegistry.getAll().forEach((cmd) => globalRegistry.unregister(cmd.id));
});

describe("CommandPalette", () => {
  it("renders nothing into the DOM when isOpen is false", () => {
    const onClose = vi.fn();
    render(<CommandPalette isOpen={false} onClose={onClose} />);
    // Radix Dialog does not mount portal content when closed.
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("toggles open on Ctrl+K keyboard shortcut via useCommandPalette", () => {
    // jsdom navigator.platform is not "mac", so the non-mac (Ctrl+K) branch fires.
    const { result } = renderHook(() => useCommandPalette());

    expect(result.current.isOpen).toBe(false);

    fireEvent.keyDown(document, { key: "k", ctrlKey: true });

    expect(result.current.isOpen).toBe(true);
  });

  it("calls onExecute and onClose when a command item is selected", () => {
    const onExecute = vi.fn();
    const onClose = vi.fn();

    globalRegistry.register({
      id: "test:action",
      label: "Run Test Action",
      group: "Test",
      onExecute,
    });

    render(<CommandPalette isOpen={true} onClose={onClose} />);

    // The item text is rendered as a <span> inside a Command.Item.
    const item = screen.getByText("Run Test Action");
    fireEvent.click(item);

    expect(onExecute).toHaveBeenCalledOnce();
    expect(onClose).toHaveBeenCalledOnce();
  });
});

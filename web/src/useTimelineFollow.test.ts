/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useTimelineFollow } from "./useTimelineFollow";

let currentScrollY = 0;
let currentScrollHeight = 2_000;
let currentInnerHeight = 800;
let prefersReducedMotion = false;
let scrollTo: ReturnType<typeof vi.fn>;

describe("useTimelineFollow", () => {
  beforeEach(() => {
    currentScrollY = 0;
    currentScrollHeight = 2_000;
    currentInnerHeight = 800;
    prefersReducedMotion = false;
    Object.defineProperty(window, "scrollY", {
      configurable: true,
      get: () => currentScrollY,
    });
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      get: () => currentInnerHeight,
    });
    Object.defineProperty(document.documentElement, "scrollHeight", {
      configurable: true,
      get: () => currentScrollHeight,
    });
    scrollTo = vi.fn((options: ScrollToOptions | number, y?: number) => {
      const requestedTop = typeof options === "number" ? (y ?? 0) : options.top;
      currentScrollY = Math.min(
        requestedTop ?? 0,
        currentScrollHeight - currentInnerHeight,
      );
      window.dispatchEvent(new Event("scroll"));
    });
    Object.defineProperty(window, "scrollTo", {
      configurable: true,
      value: scrollTo,
    });
    Object.defineProperty(window, "requestAnimationFrame", {
      configurable: true,
      value: (callback: FrameRequestCallback) => {
        callback(0);
        return 1;
      },
    });
    Object.defineProperty(window, "cancelAnimationFrame", {
      configurable: true,
      value: () => undefined,
    });
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: (query: string): MediaQueryList => ({
        matches: prefersReducedMotion,
        media: query,
        onchange: null,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
        addListener: () => undefined,
        removeListener: () => undefined,
        dispatchEvent: () => true,
      }),
    });
  });

  it("pauses immediately off the bottom and reveals new timeline content", () => {
    const { result, rerender } = renderHook(
      ({ contentVersion }) =>
        useTimelineFollow({ flowRunKey: "flow-1:run-1", contentVersion }),
      { initialProps: { contentVersion: "1" } },
    );
    expect(scrollTo).toHaveBeenLastCalledWith({
      top: currentScrollHeight,
      behavior: "auto",
    });
    scrollTo.mockClear();

    act(() => {
      currentScrollY = currentScrollHeight - currentInnerHeight - 20;
      window.dispatchEvent(new Event("scroll"));
    });
    rerender({ contentVersion: "2" });

    expect(scrollTo).not.toHaveBeenCalled();
    expect(result.current.hasUnseenContent).toBe(true);

    rerender({ contentVersion: "2" });
    expect(scrollTo).not.toHaveBeenCalled();
    expect(result.current.hasUnseenContent).toBe(true);
  });

  it("jumps to the latest content and resumes following", () => {
    const { result, rerender } = renderHook(
      ({ contentVersion }) =>
        useTimelineFollow({ flowRunKey: "flow-1:run-1", contentVersion }),
      { initialProps: { contentVersion: "1" } },
    );
    scrollTo.mockClear();
    act(() => {
      currentScrollY = 500;
      window.dispatchEvent(new Event("scroll"));
    });
    rerender({ contentVersion: "2" });

    act(() => {
      result.current.jumpToLatest();
    });
    expect(scrollTo).toHaveBeenLastCalledWith({
      top: currentScrollHeight,
      behavior: "smooth",
    });
    expect(result.current.hasUnseenContent).toBe(false);
    scrollTo.mockClear();

    rerender({ contentVersion: "3" });
    expect(scrollTo).toHaveBeenLastCalledWith({
      top: currentScrollHeight,
      behavior: "auto",
    });
  });

  it("resumes when the user returns to the bottom and follows resize", () => {
    const { result, rerender } = renderHook(
      ({ contentVersion }) =>
        useTimelineFollow({ flowRunKey: "flow-1:run-1", contentVersion }),
      { initialProps: { contentVersion: "1" } },
    );
    scrollTo.mockClear();
    act(() => {
      currentScrollY = 400;
      window.dispatchEvent(new Event("scroll"));
    });
    rerender({ contentVersion: "2" });
    expect(result.current.hasUnseenContent).toBe(true);

    act(() => {
      currentScrollY = currentScrollHeight - currentInnerHeight;
      window.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.hasUnseenContent).toBe(false);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(scrollTo).toHaveBeenLastCalledWith({
      top: currentScrollHeight,
      behavior: "auto",
    });
  });

  it("does not move a paused viewport on resize", () => {
    const { rerender } = renderHook(
      ({ contentVersion }) =>
        useTimelineFollow({ flowRunKey: "flow-1:run-1", contentVersion }),
      { initialProps: { contentVersion: "1" } },
    );
    scrollTo.mockClear();
    act(() => {
      currentScrollY = 300;
      window.dispatchEvent(new Event("scroll"));
      window.dispatchEvent(new Event("resize"));
    });
    rerender({ contentVersion: "2" });

    expect(scrollTo).not.toHaveBeenCalled();
  });

  it("resets to the bottom when the Flow or run changes", () => {
    const { result, rerender } = renderHook(
      ({ flowRunKey, contentVersion }) =>
        useTimelineFollow({ flowRunKey, contentVersion }),
      {
        initialProps: {
          flowRunKey: "flow-1:run-1",
          contentVersion: "1",
        },
      },
    );
    scrollTo.mockClear();
    act(() => {
      currentScrollY = 300;
      window.dispatchEvent(new Event("scroll"));
    });
    rerender({ flowRunKey: "flow-1:run-1", contentVersion: "2" });
    expect(result.current.hasUnseenContent).toBe(true);

    rerender({ flowRunKey: "flow-2:run-2", contentVersion: "0" });
    expect(scrollTo).toHaveBeenLastCalledWith({
      top: currentScrollHeight,
      behavior: "auto",
    });
    expect(result.current.hasUnseenContent).toBe(false);
  });

  it("uses instant scrolling when reduced motion is requested", () => {
    prefersReducedMotion = true;
    const { result, rerender } = renderHook(
      ({ contentVersion }) =>
        useTimelineFollow({ flowRunKey: "flow-1:run-1", contentVersion }),
      { initialProps: { contentVersion: "1" } },
    );
    act(() => {
      currentScrollY = 300;
      window.dispatchEvent(new Event("scroll"));
    });
    rerender({ contentVersion: "2" });
    scrollTo.mockClear();

    act(() => {
      result.current.jumpToLatest();
    });
    expect(scrollTo).toHaveBeenCalledWith({
      top: currentScrollHeight,
      behavior: "auto",
    });
  });
});

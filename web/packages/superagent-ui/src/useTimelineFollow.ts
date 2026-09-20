/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type RefObject,
} from "react";

const bottomTolerance = 12;

export type ScrollRoot = Window | HTMLElement;

export interface TimelineFollowOptions {
  flowRunKey: string;
  contentVersion: string;
  /** Defaults to `window`. Pass an element for panel scrollers (Studio). */
  scrollRoot?: ScrollRoot | RefObject<HTMLElement | null>;
}

export interface TimelineFollowState {
  hasUnseenContent: boolean;
  jumpToLatest: () => void;
  keepLatestVisible: () => void;
}

export function useTimelineFollow({
  flowRunKey,
  contentVersion,
  scrollRoot,
}: TimelineFollowOptions): TimelineFollowState {
  const isFollowing = useRef(true);
  const isJumping = useRef(false);
  const previousFlowRunKey = useRef<string | null>(null);
  const previousContentVersion = useRef<string | null>(null);
  const resizeFrame = useRef<number | null>(null);
  const [hasUnseenContent, setHasUnseenContent] = useState(false);

  const resolveRoot = useCallback((): ScrollRoot => {
    if (scrollRoot === undefined) return window;
    if ("current" in scrollRoot) return scrollRoot.current ?? window;
    return scrollRoot;
  }, [scrollRoot]);

  const keepLatestVisible = useCallback(() => {
    if (!isFollowing.current) return;
    if (resizeFrame.current !== null)
      window.cancelAnimationFrame(resizeFrame.current);
    resizeFrame.current = window.requestAnimationFrame(() => {
      resizeFrame.current = null;
      scrollToBottom(resolveRoot(), "auto");
    });
  }, [resolveRoot]);

  const jumpToLatest = useCallback(() => {
    isFollowing.current = true;
    setHasUnseenContent(false);
    const prefersReducedMotion =
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    isJumping.current = !prefersReducedMotion;
    scrollToBottom(resolveRoot(), prefersReducedMotion ? "auto" : "smooth");
    if (prefersReducedMotion) isJumping.current = false;
  }, [resolveRoot]);

  useEffect(() => {
    const root = resolveRoot();
    let previousScroll = getScrollTop(root);
    const updateFollowState = () => {
      const currentScroll = getScrollTop(root);
      const didScrollUp = currentScroll < previousScroll;
      previousScroll = currentScroll;
      if (isAtBottom(root)) {
        isFollowing.current = true;
        isJumping.current = false;
        setHasUnseenContent(false);
        return;
      }
      if (!isJumping.current && didScrollUp) isFollowing.current = false;
    };
    const keepBottomVisible = () => {
      if (!isFollowing.current) {
        updateFollowState();
        return;
      }
      keepLatestVisible();
    };
    const cancelSmoothJump = () => {
      isJumping.current = false;
    };
    const target: Window | HTMLElement = root;
    target.addEventListener("scroll", updateFollowState, { passive: true });
    window.addEventListener("resize", keepBottomVisible);
    target.addEventListener("wheel", cancelSmoothJump, { passive: true });
    target.addEventListener("touchstart", cancelSmoothJump, { passive: true });
    target.addEventListener("pointerdown", cancelSmoothJump, {
      passive: true,
    });
    updateFollowState();
    return () => {
      if (resizeFrame.current !== null)
        window.cancelAnimationFrame(resizeFrame.current);
      target.removeEventListener("scroll", updateFollowState);
      window.removeEventListener("resize", keepBottomVisible);
      target.removeEventListener("wheel", cancelSmoothJump);
      target.removeEventListener("touchstart", cancelSmoothJump);
      target.removeEventListener("pointerdown", cancelSmoothJump);
    };
  }, [keepLatestVisible, resolveRoot]);

  useLayoutEffect(() => {
    const didFlowRunChange = previousFlowRunKey.current !== flowRunKey;
    const didContentChange = previousContentVersion.current !== contentVersion;
    previousFlowRunKey.current = flowRunKey;
    previousContentVersion.current = contentVersion;
    const root = resolveRoot();

    if (didFlowRunChange) {
      isFollowing.current = true;
      isJumping.current = false;
      setHasUnseenContent(false);
      scrollToBottom(root, "auto");
      return;
    }
    if (!didContentChange) return;
    if (isFollowing.current) {
      scrollToBottom(root, "auto");
      return;
    }
    setHasUnseenContent(true);
  }, [contentVersion, flowRunKey, resolveRoot]);

  return { hasUnseenContent, jumpToLatest, keepLatestVisible };
}

function getScrollTop(root: ScrollRoot): number {
  return root === window ? window.scrollY : (root as HTMLElement).scrollTop;
}

function isAtBottom(root: ScrollRoot): boolean {
  if (root === window) {
    const distance =
      document.documentElement.scrollHeight -
      window.scrollY -
      window.innerHeight;
    return distance <= bottomTolerance;
  }
  const element = root as HTMLElement;
  return (
    element.scrollHeight - element.scrollTop - element.clientHeight <=
    bottomTolerance
  );
}

function scrollToBottom(root: ScrollRoot, behavior: ScrollBehavior): void {
  if (root === window) {
    window.scrollTo({
      top: document.documentElement.scrollHeight,
      behavior,
    });
    return;
  }
  const element = root as HTMLElement;
  element.scrollTo({ top: element.scrollHeight, behavior });
}

/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";

const bottomTolerance = 12;

interface TimelineFollowOptions {
  flowRunKey: string;
  contentVersion: string;
}

interface TimelineFollowState {
  hasUnseenContent: boolean;
  jumpToLatest: () => void;
  keepLatestVisible: () => void;
}

export function useTimelineFollow({
  flowRunKey,
  contentVersion,
}: TimelineFollowOptions): TimelineFollowState {
  const isFollowing = useRef(true);
  const isJumping = useRef(false);
  const previousFlowRunKey = useRef<string | null>(null);
  const previousContentVersion = useRef<string | null>(null);
  const resizeFrame = useRef<number | null>(null);
  const [hasUnseenContent, setHasUnseenContent] = useState(false);

  const keepLatestVisible = useCallback(() => {
    if (!isFollowing.current) return;
    if (resizeFrame.current !== null)
      window.cancelAnimationFrame(resizeFrame.current);
    resizeFrame.current = window.requestAnimationFrame(() => {
      resizeFrame.current = null;
      scrollToBottom("auto");
    });
  }, []);

  const jumpToLatest = useCallback(() => {
    isFollowing.current = true;
    setHasUnseenContent(false);
    const prefersReducedMotion =
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    isJumping.current = !prefersReducedMotion;
    scrollToBottom(prefersReducedMotion ? "auto" : "smooth");
    if (prefersReducedMotion) isJumping.current = false;
  }, []);

  useEffect(() => {
    let previousScrollY = window.scrollY;
    const updateFollowState = () => {
      const currentScrollY = window.scrollY;
      const didScrollUp = currentScrollY < previousScrollY;
      previousScrollY = currentScrollY;
      if (isAtBottom()) {
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
    window.addEventListener("scroll", updateFollowState, { passive: true });
    window.addEventListener("resize", keepBottomVisible);
    window.addEventListener("wheel", cancelSmoothJump, { passive: true });
    window.addEventListener("touchstart", cancelSmoothJump, { passive: true });
    window.addEventListener("pointerdown", cancelSmoothJump, {
      passive: true,
    });
    updateFollowState();
    return () => {
      if (resizeFrame.current !== null)
        window.cancelAnimationFrame(resizeFrame.current);
      window.removeEventListener("scroll", updateFollowState);
      window.removeEventListener("resize", keepBottomVisible);
      window.removeEventListener("wheel", cancelSmoothJump);
      window.removeEventListener("touchstart", cancelSmoothJump);
      window.removeEventListener("pointerdown", cancelSmoothJump);
    };
  }, [keepLatestVisible]);

  useLayoutEffect(() => {
    const didFlowRunChange = previousFlowRunKey.current !== flowRunKey;
    const didContentChange = previousContentVersion.current !== contentVersion;
    previousFlowRunKey.current = flowRunKey;
    previousContentVersion.current = contentVersion;

    if (didFlowRunChange) {
      isFollowing.current = true;
      isJumping.current = false;
      setHasUnseenContent(false);
      scrollToBottom("auto");
      return;
    }
    if (!didContentChange) return;
    if (isFollowing.current) {
      scrollToBottom("auto");
      return;
    }
    setHasUnseenContent(true);
  }, [contentVersion, flowRunKey]);

  return { hasUnseenContent, jumpToLatest, keepLatestVisible };
}

function isAtBottom(): boolean {
  const distance =
    document.documentElement.scrollHeight - window.scrollY - window.innerHeight;
  return distance <= bottomTolerance;
}

function scrollToBottom(behavior: ScrollBehavior): void {
  window.scrollTo({
    top: document.documentElement.scrollHeight,
    behavior,
  });
}

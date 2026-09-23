/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import type { ReactNode } from "react";

import {
  buildConversationTimeline,
  type ConversationTimelineEntry,
  type TimelineActivityEntry,
  type TimelineConsumedUserEntry,
  type TimelineLiveTextEntry,
  type TimelineSequencedMessage,
} from "./conversationTimeline.js";

export interface ConversationTimelineProps {
  messages: readonly TimelineSequencedMessage[];
  consumedUserMessages?: readonly TimelineConsumedUserEntry[];
  reasoning?: readonly TimelineLiveTextEntry[];
  activities?: readonly TimelineActivityEntry[];
  assistant?: TimelineLiveTextEntry | null;
  renderMessage: (entry: TimelineSequencedMessage) => ReactNode;
  renderActivity: (entry: TimelineActivityEntry) => ReactNode;
  renderReasoning: (entry: TimelineLiveTextEntry) => ReactNode;
  renderAssistant: (entry: TimelineLiveTextEntry) => ReactNode;
  renderConsumedUser?: (entry: TimelineConsumedUserEntry) => ReactNode;
  className?: string;
  "aria-label"?: string;
}

export function ConversationTimeline({
  messages,
  consumedUserMessages = [],
  reasoning = [],
  activities = [],
  assistant = null,
  renderMessage,
  renderActivity,
  renderReasoning,
  renderAssistant,
  renderConsumedUser,
  className = "sa-conversation-timeline",
  "aria-label": ariaLabel,
}: ConversationTimelineProps) {
  const timeline = buildConversationTimeline(
    messages,
    consumedUserMessages,
    reasoning,
    activities,
    assistant,
  );
  return (
    <section
      className={className}
      {...(ariaLabel !== undefined ? { "aria-label": ariaLabel } : {})}
    >
      {timeline.map((entry) =>
        renderTimelineEntry(entry, {
          renderMessage,
          renderActivity,
          renderReasoning,
          renderAssistant,
          renderConsumedUser,
        }),
      )}
    </section>
  );
}

function renderTimelineEntry(
  entry: ConversationTimelineEntry,
  renderers: {
    renderMessage: (entry: TimelineSequencedMessage) => ReactNode;
    renderActivity: (entry: TimelineActivityEntry) => ReactNode;
    renderReasoning: (entry: TimelineLiveTextEntry) => ReactNode;
    renderAssistant: (entry: TimelineLiveTextEntry) => ReactNode;
    renderConsumedUser?: (entry: TimelineConsumedUserEntry) => ReactNode;
  },
): ReactNode {
  switch (entry.kind) {
    case "message":
      return (
        <div key={`message:${String(entry.value.sequence)}`}>
          {renderers.renderMessage(entry.value)}
        </div>
      );
    case "activity":
      return (
        <div key={`activity:${entry.value.resumeToken}`}>
          {renderers.renderActivity(entry.value)}
        </div>
      );
    case "reasoning":
      return (
        <div key={`reasoning:${entry.value.source}`}>
          {renderers.renderReasoning(entry.value)}
        </div>
      );
    case "assistant":
      return (
        <div key={`assistant:${entry.value.source}`}>
          {renderers.renderAssistant(entry.value)}
        </div>
      );
    case "consumed-user":
      return (
        <div key={`consumed-user:${entry.value.messageId}`}>
          {renderers.renderConsumedUser?.(entry.value) ?? null}
        </div>
      );
  }
}

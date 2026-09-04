/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  EventKind,
  MessageRole,
  type Sequence,
  type SequencedMessage,
} from "./api/generated";
import type { ActivityEntry, ReasoningEntry } from "./conversation-state";

export type ConversationTimelineEntry =
  | { kind: "message"; value: SequencedMessage }
  | { kind: "reasoning"; value: ReasoningEntry };

interface ModelWindow {
  startedAt: number | null;
  finishedAt: number | null;
}

export function buildConversationTimeline(
  messages: readonly SequencedMessage[],
  reasoning: readonly ReasoningEntry[],
  activities: readonly ActivityEntry[],
): ConversationTimelineEntry[] {
  const explicitSequences = modelMessageSequences(activities);
  const modelWindows = completedModelWindows(activities);
  const loadedSequences = new Set(messages.map(({ sequence }) => sequence));
  const reasoningBySequence = new Map<Sequence, ReasoningEntry[]>();
  const unanchoredReasoning: ReasoningEntry[] = [];

  for (const entry of reasoning) {
    const sequence =
      explicitSequences.get(entry.source) ??
      inferMessageSequence(entry.source, messages, modelWindows);
    if (sequence === undefined || !loadedSequences.has(sequence)) {
      unanchoredReasoning.push(entry);
      continue;
    }
    const entries = reasoningBySequence.get(sequence) ?? [];
    entries.push(entry);
    reasoningBySequence.set(sequence, entries);
  }

  const timeline: ConversationTimelineEntry[] = [];
  for (const message of messages) {
    const entries = reasoningBySequence.get(message.sequence) ?? [];
    for (const entry of [...entries].sort(compareReasoning)) {
      timeline.push({ kind: "reasoning", value: entry });
    }
    timeline.push({ kind: "message", value: message });
  }
  for (const entry of [...unanchoredReasoning].sort(compareReasoning)) {
    timeline.push({ kind: "reasoning", value: entry });
  }
  return timeline;
}

function modelMessageSequences(
  activities: readonly ActivityEntry[],
): ReadonlyMap<string, Sequence> {
  const result = new Map<string, Sequence>();
  for (const activity of activities) {
    if (activity.value.messageSequence !== null) {
      result.set(activity.source, activity.value.messageSequence);
    }
  }
  return result;
}

function completedModelWindows(
  activities: readonly ActivityEntry[],
): ReadonlyMap<string, ModelWindow> {
  const result = new Map<string, ModelWindow>();
  for (const activity of activities) {
    const timestamp = parseTimestamp(activity.createdAt);
    if (timestamp === null) continue;
    const window = result.get(activity.source) ?? {
      startedAt: null,
      finishedAt: null,
    };
    if (activity.value.kind === EventKind.MODEL_STARTED) {
      window.startedAt =
        window.startedAt === null
          ? timestamp
          : Math.min(window.startedAt, timestamp);
    } else if (
      activity.value.kind === EventKind.MODEL_COMPLETED ||
      activity.value.kind === EventKind.MODEL_FAILED
    ) {
      window.finishedAt =
        window.finishedAt === null
          ? timestamp
          : Math.max(window.finishedAt, timestamp);
    }
    result.set(activity.source, window);
  }
  return result;
}

function inferMessageSequence(
  source: string,
  messages: readonly SequencedMessage[],
  modelWindows: ReadonlyMap<string, ModelWindow>,
): Sequence | undefined {
  const window = modelWindows.get(source);
  if (window === undefined) return undefined;
  const { startedAt, finishedAt } = window;
  if (startedAt === null || finishedAt === null) {
    return undefined;
  }
  const candidates = messages.filter(({ message }) => {
    if (message.role !== MessageRole.ASSISTANT) return false;
    const timestamp = parseTimestamp(message.createdAt);
    return (
      timestamp !== null && timestamp >= startedAt && timestamp <= finishedAt
    );
  });
  return candidates.length === 1 ? candidates[0]?.sequence : undefined;
}

function compareReasoning(left: ReasoningEntry, right: ReasoningEntry): number {
  const leftTimestamp = parseTimestamp(left.createdAt);
  const rightTimestamp = parseTimestamp(right.createdAt);
  if (leftTimestamp !== null && rightTimestamp !== null) {
    const difference = leftTimestamp - rightTimestamp;
    if (difference !== 0) return difference;
  }
  return left.source.localeCompare(right.source);
}

function parseTimestamp(value: string): number | null {
  const timestamp = Date.parse(value);
  return Number.isNaN(timestamp) ? null : timestamp;
}

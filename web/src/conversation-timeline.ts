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
import type { AssistantEntry } from "./conversation-state";

export type ConversationTimelineEntry =
  | { kind: "message"; value: SequencedMessage }
  | { kind: "reasoning"; value: ReasoningEntry }
  | { kind: "activity"; value: ActivityEntry }
  | { kind: "assistant"; value: AssistantEntry };

interface ModelWindow {
  startedAt: number | null;
  finishedAt: number | null;
}

export function buildConversationTimeline(
  messages: readonly SequencedMessage[],
  reasoning: readonly ReasoningEntry[],
  activities: readonly ActivityEntry[],
  assistant: AssistantEntry | null,
): ConversationTimelineEntry[] {
  const explicitSequences = modelMessageSequences(activities);
  const modelWindows = completedModelWindows(activities);
  const entries: ConversationTimelineEntry[] = [
    ...messages.map((value) => ({ kind: "message" as const, value })),
    ...reasoning.map((value) => ({ kind: "reasoning" as const, value })),
    ...activities.map((value) => ({ kind: "activity" as const, value })),
  ];
  if (assistant !== null) entries.push({ kind: "assistant", value: assistant });
  return entries.sort((left, right) =>
    compareTimelineEntries(
      left,
      right,
      messages,
      explicitSequences,
      modelWindows,
    ),
  );
}

function compareTimelineEntries(
  left: ConversationTimelineEntry,
  right: ConversationTimelineEntry,
  messages: readonly SequencedMessage[],
  explicitSequences: ReadonlyMap<string, Sequence>,
  modelWindows: ReadonlyMap<string, ModelWindow>,
): number {
  const leftTimestamp = parseTimestamp(entryCreatedAt(left));
  const rightTimestamp = parseTimestamp(entryCreatedAt(right));
  if (leftTimestamp !== null && rightTimestamp !== null) {
    const difference = leftTimestamp - rightTimestamp;
    if (difference !== 0) return difference;
  } else if (leftTimestamp !== null) {
    return -1;
  } else if (rightTimestamp !== null) {
    return 1;
  }
  const causalOrder = compareReasoningToAssistant(
    left,
    right,
    messages,
    explicitSequences,
    modelWindows,
  );
  if (causalOrder !== 0) return causalOrder;
  const typeDifference = entryRank(left) - entryRank(right);
  return typeDifference !== 0
    ? typeDifference
    : entryIdentity(left).localeCompare(entryIdentity(right));
}

function compareReasoningToAssistant(
  left: ConversationTimelineEntry,
  right: ConversationTimelineEntry,
  messages: readonly SequencedMessage[],
  explicitSequences: ReadonlyMap<string, Sequence>,
  modelWindows: ReadonlyMap<string, ModelWindow>,
): number {
  if (left.kind === "reasoning" && right.kind === "message") {
    return reasoningSequence(
      left.value,
      messages,
      explicitSequences,
      modelWindows,
    ) === right.value.sequence
      ? -1
      : 0;
  }
  if (right.kind === "reasoning" && left.kind === "message") {
    return reasoningSequence(
      right.value,
      messages,
      explicitSequences,
      modelWindows,
    ) === left.value.sequence
      ? 1
      : 0;
  }
  if (left.kind === "reasoning" && right.kind === "assistant") return -1;
  if (right.kind === "reasoning" && left.kind === "assistant") return 1;
  return 0;
}

function reasoningSequence(
  entry: ReasoningEntry,
  messages: readonly SequencedMessage[],
  explicitSequences: ReadonlyMap<string, Sequence>,
  modelWindows: ReadonlyMap<string, ModelWindow>,
): Sequence | undefined {
  return (
    explicitSequences.get(entry.source) ??
    inferMessageSequence(entry.source, messages, modelWindows)
  );
}

function entryCreatedAt(entry: ConversationTimelineEntry): string {
  return entry.kind === "message"
    ? entry.value.message.createdAt
    : entry.value.createdAt;
}

function entryRank(entry: ConversationTimelineEntry): number {
  switch (entry.kind) {
    case "message":
      return entry.value.message.role === MessageRole.ASSISTANT ? 3 : 0;
    case "activity":
      return 1;
    case "reasoning":
      return 2;
    case "assistant":
      return 3;
  }
}

function entryIdentity(entry: ConversationTimelineEntry): string {
  switch (entry.kind) {
    case "message":
      return `message:${String(entry.value.sequence).padStart(16, "0")}`;
    case "activity":
      return `activity:${entry.value.resumeToken}`;
    case "reasoning":
      return `reasoning:${entry.value.source}`;
    case "assistant":
      return `assistant:${entry.value.source}`;
  }
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

function parseTimestamp(value: string): number | null {
  const timestamp = Date.parse(value);
  return Number.isNaN(timestamp) ? null : timestamp;
}

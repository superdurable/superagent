/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

export type TimelineMessageRole = "system" | "user" | "assistant" | "tool";

export interface TimelineToolCall {
  id: string;
  name: string;
  argumentsJson: string;
}

export interface TimelineMessage {
  role: TimelineMessageRole;
  content: string;
  toolCalls: readonly TimelineToolCall[];
  toolCallId: string | null;
  toolName: string | null;
  createdAt: string;
}

export interface TimelineSequencedMessage {
  sequence: number;
  message: TimelineMessage;
}

export interface TimelineConsumedUserEntry {
  messageId: string;
  value: { content: string; planMode?: boolean };
  createdAt: string;
  consumedAfterSequence: number;
}

export interface TimelineLiveTextEntry {
  source: string;
  createdAt: string;
  value: string;
  isComplete: boolean;
}

export interface TimelineActivityEvent {
  kind: string;
  message: string;
  callId?: string | null;
  toolName?: string | null;
  messageSequence: number | null;
}

export interface TimelineActivityEntry {
  resumeToken: string;
  source: string;
  createdAt: string;
  value: TimelineActivityEvent;
}

export type ConversationTimelineEntry =
  | { kind: "message"; value: TimelineSequencedMessage }
  | { kind: "consumed-user"; value: TimelineConsumedUserEntry }
  | { kind: "reasoning"; value: TimelineLiveTextEntry }
  | { kind: "activity"; value: TimelineActivityEntry }
  | { kind: "assistant"; value: TimelineLiveTextEntry };

interface ModelWindow {
  startedAt: number | null;
  finishedAt: number | null;
}

const MODEL_STARTED = "model_started";
const MODEL_COMPLETED = "model_completed";
const MODEL_FAILED = "model_failed";

export function buildConversationTimeline(
  messages: readonly TimelineSequencedMessage[],
  consumedUserMessages: readonly TimelineConsumedUserEntry[],
  reasoning: readonly TimelineLiveTextEntry[],
  activities: readonly TimelineActivityEntry[],
  assistant: TimelineLiveTextEntry | null,
): ConversationTimelineEntry[] {
  const explicitSequences = modelMessageSequences(activities);
  const modelWindows = completedModelWindows(activities);
  const entries: ConversationTimelineEntry[] = [
    ...messages.map((value) => ({ kind: "message" as const, value })),
    ...consumedUserMessages.map((value) => ({
      kind: "consumed-user" as const,
      value,
    })),
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
  messages: readonly TimelineSequencedMessage[],
  explicitSequences: ReadonlyMap<string, number>,
  modelWindows: ReadonlyMap<string, ModelWindow>,
): number {
  const inputOrder = compareConsumedInputToDurableHistory(left, right);
  if (inputOrder !== null) return inputOrder;
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

function compareConsumedInputToDurableHistory(
  left: ConversationTimelineEntry,
  right: ConversationTimelineEntry,
): number | null {
  if (left.kind === "message" && right.kind === "consumed-user") {
    return left.value.sequence <= right.value.consumedAfterSequence ? -1 : 1;
  }
  if (left.kind === "consumed-user" && right.kind === "message") {
    return right.value.sequence <= left.value.consumedAfterSequence ? 1 : -1;
  }
  if (left.kind === "consumed-user" && right.kind === "consumed-user") {
    return left.value.consumedAfterSequence - right.value.consumedAfterSequence;
  }
  return null;
}

function compareReasoningToAssistant(
  left: ConversationTimelineEntry,
  right: ConversationTimelineEntry,
  messages: readonly TimelineSequencedMessage[],
  explicitSequences: ReadonlyMap<string, number>,
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
  entry: TimelineLiveTextEntry,
  messages: readonly TimelineSequencedMessage[],
  explicitSequences: ReadonlyMap<string, number>,
  modelWindows: ReadonlyMap<string, ModelWindow>,
): number | undefined {
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
      return entry.value.message.role === "assistant" ? 3 : 0;
    case "consumed-user":
      return 0;
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
    case "consumed-user":
      return `consumed-user:${entry.value.messageId}`;
    case "activity":
      return `activity:${entry.value.resumeToken}`;
    case "reasoning":
      return `reasoning:${entry.value.source}`;
    case "assistant":
      return `assistant:${entry.value.source}`;
  }
}

function modelMessageSequences(
  activities: readonly TimelineActivityEntry[],
): ReadonlyMap<string, number> {
  const result = new Map<string, number>();
  for (const activity of activities) {
    if (activity.value.messageSequence !== null) {
      result.set(activity.source, activity.value.messageSequence);
    }
  }
  return result;
}

function completedModelWindows(
  activities: readonly TimelineActivityEntry[],
): ReadonlyMap<string, ModelWindow> {
  const result = new Map<string, ModelWindow>();
  for (const activity of activities) {
    const timestamp = parseTimestamp(activity.createdAt);
    if (timestamp === null) continue;
    const window = result.get(activity.source) ?? {
      startedAt: null,
      finishedAt: null,
    };
    if (activity.value.kind === MODEL_STARTED) {
      window.startedAt =
        window.startedAt === null
          ? timestamp
          : Math.min(window.startedAt, timestamp);
    } else if (
      activity.value.kind === MODEL_COMPLETED ||
      activity.value.kind === MODEL_FAILED
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
  messages: readonly TimelineSequencedMessage[],
  modelWindows: ReadonlyMap<string, ModelWindow>,
): number | undefined {
  const window = modelWindows.get(source);
  if (window === undefined) return undefined;
  const { startedAt, finishedAt } = window;
  if (startedAt === null || finishedAt === null) {
    return undefined;
  }
  const candidates = messages.filter(({ message }) => {
    if (message.role !== "assistant") return false;
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

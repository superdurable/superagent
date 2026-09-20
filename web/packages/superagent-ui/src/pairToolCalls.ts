/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import type {
  TimelineSequencedMessage,
  TimelineToolCall,
} from "./conversationTimeline.js";

export interface ToolCallResultView {
  content: string;
  toolName: string | null;
  sequence: number;
  createdAt: string;
}

export interface ToolCallPair {
  call: TimelineToolCall;
  result: ToolCallResultView | null;
  assistantSequence: number;
}

export function indexToolResultsByCallId(
  messages: readonly TimelineSequencedMessage[],
): ReadonlyMap<string, ToolCallResultView> {
  const results = new Map<string, ToolCallResultView>();
  for (const { sequence, message } of messages) {
    if (message.role !== "tool" || message.toolCallId === null) continue;
    results.set(message.toolCallId, {
      content: message.content,
      toolName: message.toolName,
      sequence,
      createdAt: message.createdAt,
    });
  }
  return results;
}

export function pairToolCallsById(
  messages: readonly TimelineSequencedMessage[],
): {
  pairs: ToolCallPair[];
  pairedToolCallIds: ReadonlySet<string>;
  orphanToolMessages: TimelineSequencedMessage[];
} {
  const resultsByCallId = indexToolResultsByCallId(messages);
  const pairedToolCallIds = new Set<string>();
  const pairs: ToolCallPair[] = [];
  for (const { sequence, message } of messages) {
    if (message.role !== "assistant") continue;
    for (const call of message.toolCalls) {
      const result = resultsByCallId.get(call.id) ?? null;
      if (result !== null) pairedToolCallIds.add(call.id);
      pairs.push({
        call,
        result,
        assistantSequence: sequence,
      });
    }
  }
  const orphanToolMessages = messages.filter(
    ({ message }) =>
      message.role === "tool" &&
      (message.toolCallId === null ||
        !pairedToolCallIds.has(message.toolCallId)),
  );
  return { pairs, pairedToolCallIds, orphanToolMessages };
}

export function isPairedToolResultMessage(
  message: TimelineSequencedMessage["message"],
  pairedToolCallIds: ReadonlySet<string>,
): boolean {
  return (
    message.role === "tool" &&
    message.toolCallId !== null &&
    pairedToolCallIds.has(message.toolCallId)
  );
}

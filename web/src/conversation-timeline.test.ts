/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from "vitest";

import {
  EventKind,
  MessageRole,
  type AgentEvent,
  type SequencedMessage,
} from "./api/generated";
import type {
  ActivityEntry,
  AssistantEntry,
  ReasoningEntry,
} from "./conversation-state";
import { buildConversationTimeline } from "./conversation-timeline";

describe("buildConversationTimeline", () => {
  it("orders messages, activities, reasoning, and live assistant by time", () => {
    const timeline = buildConversationTimeline(
      messages(),
      [reasoning("model-2", "2026-09-03T00:02:30Z")],
      [
        activity("model-2", "2026-09-03T00:02:00Z", EventKind.MODEL_STARTED, 4),
        activity(
          "model-1",
          "2026-09-03T00:01:30Z",
          EventKind.MODEL_COMPLETED,
          2,
        ),
      ],
      assistant("model-live", "2026-09-03T00:03:30Z"),
    );

    expect(timeline.map(timelineIdentity)).toEqual([
      "message:1",
      "message:2",
      "activity:model-1:model_completed",
      "activity:model-2:model_started",
      "reasoning:model-2",
      "message:3",
      "assistant:model-live",
      "message:4",
    ]);
  });

  it("places anchored reasoning before its assistant when timestamps tie", () => {
    const timeline = buildConversationTimeline(
      messages(),
      [reasoning("model-1", "2026-09-03T00:01:00Z")],
      [
        activity(
          "model-1",
          "2026-09-03T00:01:01Z",
          EventKind.MODEL_COMPLETED,
          2,
        ),
      ],
      null,
    );

    expect(timeline.map(timelineIdentity)).toEqual([
      "message:1",
      "reasoning:model-1",
      "message:2",
      "activity:model-1:model_completed",
      "message:3",
      "message:4",
    ]);
  });

  it("uses an explicit sequence when invalid timestamps need a tie-break", () => {
    const timeline = buildConversationTimeline(
      [
        message(1, MessageRole.USER, "2026-09-03T00:00:00Z"),
        message(2, MessageRole.ASSISTANT, "invalid"),
      ],
      [reasoning("retained-model", "invalid")],
      [
        activity(
          "retained-model",
          "2026-09-03T00:02:00Z",
          EventKind.MODEL_COMPLETED,
          2,
        ),
      ],
      null,
    );

    expect(timeline.map(timelineIdentity)).toEqual([
      "message:1",
      "activity:retained-model:model_completed",
      "reasoning:retained-model",
      "message:2",
    ]);
  });

  it("keeps every distinct Activity resume token", () => {
    const first = activity(
      "tool-call",
      "2026-09-03T00:01:00Z",
      EventKind.TOOL_PROGRESS,
      null,
    );
    const second = { ...first, resumeToken: "second-token" };
    const timeline = buildConversationTimeline([], [], [first, second], null);

    expect(timeline).toHaveLength(2);
    expect(timeline.map(timelineIdentity)).toEqual([
      "activity:tool-call:tool_progress",
      "activity:tool-call:tool_progress",
    ]);
  });
});

function messages(): SequencedMessage[] {
  return [
    message(1, MessageRole.USER, "2026-09-03T00:00:00Z"),
    message(2, MessageRole.ASSISTANT, "2026-09-03T00:01:00Z"),
    message(3, MessageRole.USER, "2026-09-03T00:03:00Z"),
    message(4, MessageRole.ASSISTANT, "2026-09-03T00:04:00Z"),
  ];
}

function message(
  sequence: number,
  role: MessageRole,
  createdAt: string,
): SequencedMessage {
  return {
    sequence,
    message: {
      messageId: `message-${String(sequence)}`,
      role,
      content: `message ${String(sequence)}`,
      toolCalls: [],
      toolCallId: null,
      toolName: null,
      createdAt,
    },
  };
}

function reasoning(source: string, createdAt: string): ReasoningEntry {
  return { source, createdAt, value: `${source} summary`, isComplete: true };
}

function assistant(source: string, createdAt: string): AssistantEntry {
  return { source, createdAt, value: "live reply", isComplete: false };
}

function activity(
  source: string,
  createdAt: string,
  kind: AgentEvent["kind"],
  messageSequence: number | null,
): ActivityEntry {
  return {
    resumeToken: `${source}:${createdAt}:${kind}`,
    source,
    createdAt,
    value: {
      kind,
      message: kind,
      callId: null,
      toolName: null,
      messageSequence,
    },
  };
}

function timelineIdentity(
  entry: ReturnType<typeof buildConversationTimeline>[number],
): string {
  switch (entry.kind) {
    case "message":
      return `message:${String(entry.value.sequence)}`;
    case "reasoning":
      return `reasoning:${entry.value.source}`;
    case "activity":
      return `activity:${entry.value.source}:${entry.value.value.kind}`;
    case "assistant":
      return `assistant:${entry.value.source}`;
  }
}

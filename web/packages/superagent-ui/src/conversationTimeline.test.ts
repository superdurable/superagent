/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from "vitest";

import {
  buildConversationTimeline,
  type TimelineActivityEntry,
  type TimelineLiveTextEntry,
  type TimelineSequencedMessage,
} from "./conversationTimeline";

describe("buildConversationTimeline", () => {
  it("orders messages, activities, reasoning, and live assistant by time", () => {
    const timeline = buildConversationTimeline(
      messages(),
      [
        {
          messageId: "consumed-1",
          value: { content: "follow up", planMode: false },
          createdAt: "2026-09-03T00:02:15Z",
          consumedAfterSequence: 2,
        },
      ],
      [reasoning("model-2", "2026-09-03T00:02:30Z")],
      [
        activity("model-2", "2026-09-03T00:02:00Z", "model_started", 4),
        activity("model-1", "2026-09-03T00:01:30Z", "model_completed", 2),
      ],
      assistant("model-live", "2026-09-03T00:03:30Z"),
    );

    expect(timeline.map(timelineIdentity)).toEqual([
      "message:1",
      "message:2",
      "activity:model-1:model_completed",
      "activity:model-2:model_started",
      "consumed-user:consumed-1",
      "reasoning:model-2",
      "message:3",
      "assistant:model-live",
      "message:4",
    ]);
  });

  it("places anchored reasoning before its assistant when timestamps tie", () => {
    const timeline = buildConversationTimeline(
      messages(),
      [],
      [reasoning("model-1", "2026-09-03T00:01:00Z")],
      [activity("model-1", "2026-09-03T00:01:01Z", "model_completed", 2)],
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
});

function messages(): TimelineSequencedMessage[] {
  return [
    message(1, "user", "2026-09-03T00:00:00Z"),
    message(2, "assistant", "2026-09-03T00:01:00Z"),
    message(3, "user", "2026-09-03T00:03:00Z"),
    message(4, "assistant", "2026-09-03T00:04:00Z"),
  ];
}

function message(
  sequence: number,
  role: "user" | "assistant",
  createdAt: string,
): TimelineSequencedMessage {
  return {
    sequence,
    message: {
      role,
      content: `message ${String(sequence)}`,
      toolCalls: [],
      toolCallId: null,
      toolName: null,
      createdAt,
    },
  };
}

function reasoning(source: string, createdAt: string): TimelineLiveTextEntry {
  return { source, createdAt, value: `${source} summary`, isComplete: true };
}

function assistant(source: string, createdAt: string): TimelineLiveTextEntry {
  return { source, createdAt, value: "live reply", isComplete: false };
}

function activity(
  source: string,
  createdAt: string,
  kind: string,
  messageSequence: number | null,
): TimelineActivityEntry {
  return {
    resumeToken: `${source}:${createdAt}:${kind}`,
    source,
    createdAt,
    value: { kind, message: kind, messageSequence },
  };
}

function timelineIdentity(
  entry: ReturnType<typeof buildConversationTimeline>[number],
): string {
  switch (entry.kind) {
    case "message":
      return `message:${String(entry.value.sequence)}`;
    case "consumed-user":
      return `consumed-user:${entry.value.messageId}`;
    case "reasoning":
      return `reasoning:${entry.value.source}`;
    case "activity":
      return `activity:${entry.value.source}:${entry.value.value.kind}`;
    case "assistant":
      return `assistant:${entry.value.source}`;
  }
}

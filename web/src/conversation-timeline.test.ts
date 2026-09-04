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
import type { ActivityEntry, ReasoningEntry } from "./conversation-state";
import { buildConversationTimeline } from "./conversation-timeline";

describe("buildConversationTimeline", () => {
  it("places reasoning before its durable assistant sequence", () => {
    const timeline = buildConversationTimeline(
      messages(),
      [reasoning("model-1", "2026-09-03T00:09:00Z")],
      [
        activity(
          "model-1",
          "2026-09-03T00:01:01Z",
          EventKind.MODEL_COMPLETED,
          2,
        ),
      ],
    );

    expect(timeline.map(timelineIdentity)).toEqual([
      "message:1",
      "reasoning:model-1",
      "message:2",
      "message:3",
      "message:4",
    ]);
  });

  it("infers one assistant inside a completed model window", () => {
    const timeline = buildConversationTimeline(
      messages(),
      [reasoning("retained-model", "2026-09-03T00:01:30Z")],
      [
        activity(
          "retained-model",
          "2026-09-03T00:02:00Z",
          EventKind.MODEL_COMPLETED,
          null,
        ),
        activity(
          "retained-model",
          "2026-09-03T00:00:30Z",
          EventKind.MODEL_STARTED,
          null,
        ),
      ],
    );

    expect(timeline.map(timelineIdentity)).toEqual([
      "message:1",
      "reasoning:retained-model",
      "message:2",
      "message:3",
      "message:4",
    ]);
  });

  it("leaves an ambiguous summary with live output", () => {
    const timeline = buildConversationTimeline(
      messages(),
      [reasoning("ambiguous-model", "2026-09-03T00:02:30Z")],
      [
        activity(
          "ambiguous-model",
          "2026-09-03T00:05:00Z",
          EventKind.MODEL_COMPLETED,
          null,
        ),
        activity(
          "ambiguous-model",
          "2026-09-03T00:00:30Z",
          EventKind.MODEL_STARTED,
          null,
        ),
      ],
    );

    expect(timeline.map(timelineIdentity)).toEqual([
      "message:1",
      "message:2",
      "message:3",
      "message:4",
      "reasoning:ambiguous-model",
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
  return entry.kind === "message"
    ? `message:${String(entry.value.sequence)}`
    : `reasoning:${entry.value.source}`;
}

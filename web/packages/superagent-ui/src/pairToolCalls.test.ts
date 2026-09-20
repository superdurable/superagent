/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from "vitest";

import type { TimelineSequencedMessage } from "./conversationTimeline";
import {
  isPairedToolResultMessage,
  pairToolCallsById,
} from "./pairToolCalls";

describe("pairToolCallsById", () => {
  it("pairs assistant tool calls with tool results by call id", () => {
    const messages: TimelineSequencedMessage[] = [
      {
        sequence: 1,
        message: {
          role: "assistant",
          content: "",
          createdAt: "2026-09-03T00:00:00Z",
          toolCallId: null,
          toolName: null,
          toolCalls: [
            {
              id: "call-1",
              name: "apply_patch",
              argumentsJson: '{"patch":"x"}',
            },
          ],
        },
      },
      {
        sequence: 2,
        message: {
          role: "tool",
          content: '{"changed_paths":["a.ts"]}',
          createdAt: "2026-09-03T00:00:01Z",
          toolCallId: "call-1",
          toolName: "apply_patch",
          toolCalls: [],
        },
      },
    ];
    const paired = pairToolCallsById(messages);
    expect(paired.pairs).toHaveLength(1);
    expect(paired.pairs[0]?.result?.content).toContain("changed_paths");
    expect(paired.orphanToolMessages).toHaveLength(0);
    expect(
      isPairedToolResultMessage(
        messages[1]!.message,
        paired.pairedToolCallIds,
      ),
    ).toBe(true);
  });

  it("keeps pending calls and orphan tool messages", () => {
    const messages: TimelineSequencedMessage[] = [
      {
        sequence: 1,
        message: {
          role: "assistant",
          content: "",
          createdAt: "2026-09-03T00:00:00Z",
          toolCallId: null,
          toolName: null,
          toolCalls: [
            {
              id: "pending",
              name: "exec_short_command",
              argumentsJson: '{"argv":["true"]}',
            },
          ],
        },
      },
      {
        sequence: 2,
        message: {
          role: "tool",
          content: "orphan",
          createdAt: "2026-09-03T00:00:01Z",
          toolCallId: "missing",
          toolName: "other",
          toolCalls: [],
        },
      },
    ];
    const paired = pairToolCallsById(messages);
    expect(paired.pairs[0]?.result).toBeNull();
    expect(paired.orphanToolMessages).toHaveLength(1);
  });
});

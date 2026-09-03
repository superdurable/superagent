/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from "vitest";

import { AgentStatus, MessageRole, type AgentSnapshot } from "./api/generated";
import {
  conversationReducer,
  initialConversationState,
} from "./conversation-state";

describe("conversationReducer", () => {
  it("replaces every durable view from one Snapshot action", () => {
    const state = conversationReducer(initialConversationState(), {
      type: "snapshot-loaded",
      snapshot: snapshot("run-1", "queued-1", "first"),
    });
    const next = conversationReducer(state, {
      type: "snapshot-loaded",
      snapshot: snapshot("run-2", "queued-2", "second"),
    });

    expect(next).toMatchObject({
      kind: "ready",
      snapshot: {
        runId: "run-2",
        history: { messages: [{ message: { content: "second" } }] },
        description: { pendingQueuedMessageCount: 1 },
        queued: [{ messageId: "queued-2" }],
      },
    });
  });

  it("optimistically removes a queued message and ignores stale completion", () => {
    const ready = conversationReducer(initialConversationState(), {
      type: "snapshot-loaded",
      snapshot: snapshot("run-1", "queued-1", "hello"),
    });
    if (ready.kind !== "ready") throw new Error("expected ready state");
    const queuedMessage = ready.snapshot.queued[0];
    if (queuedMessage === undefined) throw new Error("expected queued message");
    const command = {
      kind: "queue" as const,
      action: "edit" as const,
      message: queuedMessage,
    };
    const pending = conversationReducer(ready, {
      type: "command-started",
      id: 2,
      command,
    });
    const stale = conversationReducer(pending, {
      type: "command-succeeded",
      id: 1,
    });

    expect(pending).toMatchObject({
      kind: "ready",
      composer: "hello",
      snapshot: {
        queued: [],
        description: { pendingQueuedMessageCount: 0 },
      },
      pendingCommand: { id: 2 },
    });
    expect(stale).toBe(pending);
  });

  it("restarts subscriptions only after reconnect reconciliation", () => {
    const ready = conversationReducer(initialConversationState(), {
      type: "snapshot-loaded",
      snapshot: snapshot("run-1", "queued-1", "hello"),
    });
    const disconnected = conversationReducer(ready, {
      type: "stream-failed",
      message: "disconnected",
      isTerminal: false,
    });
    const reconciled = conversationReducer(disconnected, {
      type: "snapshot-loaded",
      snapshot: snapshot("run-1", "queued-1", "hello"),
    });

    expect(disconnected).toMatchObject({
      kind: "ready",
      connection: "reconnecting",
      subscriptionGeneration: 0,
    });
    expect(reconciled).toMatchObject({
      kind: "ready",
      connection: "live",
      subscriptionGeneration: 1,
    });
  });
});

function snapshot(
  runId: string,
  messageId: string,
  content: string,
): AgentSnapshot {
  return {
    runId,
    history: {
      messages: [
        {
          sequence: 1,
          message: {
            role: MessageRole.USER,
            content,
            toolCalls: [],
            toolCallId: null,
            toolName: null,
            createdAt: "2026-09-03T00:00:00Z",
          },
        },
      ],
      nextBeforeSequence: null,
    },
    description: {
      status: AgentStatus.WAITING_FOR_MESSAGE,
      model: "mock/reliable",
      systemPrompt: "Be helpful.",
      firstRetainedSequence: 1,
      lastSequence: 1,
      summarizedThroughSequence: 0,
      pendingApproval: null,
      pendingTimer: null,
      pendingUserInput: null,
      plan: null,
      isPlanExecutionRequested: false,
      pendingQueuedMessageCount: 1,
      pendingSteeredMessageCount: 0,
      availableMcpServers: [],
      availableTools: [],
    },
    queued: [
      {
        messageId,
        value: { content, planMode: false },
      },
    ],
    steered: [],
  };
}

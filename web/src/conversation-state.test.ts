/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from "vitest";

import {
  AgentStatus,
  EventKind,
  FlowErrorType,
  FlowStatus,
  MessageRole,
  type AgentSnapshot,
} from "./api/generated";
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

  it("renders a terminal Snapshot without inventing active Agent state", () => {
    const state = conversationReducer(initialConversationState(), {
      type: "snapshot-loaded",
      snapshot: {
        runId: "run-terminal",
        flowStatus: FlowStatus.FAILED,
        errorType: FlowErrorType.WORKER_METHOD,
        errorMessage: "worker stopped",
        history: { messages: [], nextBeforeSequence: null },
        description: null,
        queued: [],
        steered: [],
      },
    });

    expect(state).toMatchObject({
      kind: "ready",
      lifecycle: "terminal",
      connection: "terminal",
      snapshot: { runId: "run-terminal", description: null },
      error: "worker stopped",
    });
  });

  it("keeps reasoning summaries separate by model invocation source", () => {
    let state = conversationReducer(initialConversationState(), {
      type: "snapshot-loaded",
      snapshot: snapshot("run-1", "queued-1", "hello"),
    });
    state = conversationReducer(state, {
      type: "stream-update",
      update: {
        kind: "reasoning",
        source: "model-call-1",
        createdAt: "2026-09-03T00:00:01Z",
        value: "first ",
      },
    });
    state = conversationReducer(state, {
      type: "stream-update",
      update: {
        kind: "reasoning",
        source: "model-call-2",
        createdAt: "2026-09-03T00:00:02Z",
        value: "second",
      },
    });
    state = conversationReducer(state, {
      type: "stream-update",
      update: {
        kind: "activity",
        resumeToken: "activity-1",
        source: "model-call-1",
        createdAt: "2026-09-03T00:00:03Z",
        value: {
          kind: EventKind.MODEL_COMPLETED,
          message: "complete",
          callId: null,
          toolName: null,
          messageSequence: null,
        },
      },
    });

    expect(state).toMatchObject({
      kind: "ready",
      reasoning: [
        { source: "model-call-1", value: "first ", isComplete: true },
        { source: "model-call-2", value: "second", isComplete: false },
      ],
      activities: [{ source: "model-call-1" }],
    });
  });

  it("keeps late Stream batches complete until Snapshot reconciliation", () => {
    let state = conversationReducer(initialConversationState(), {
      type: "snapshot-loaded",
      snapshot: snapshotWithStatus(AgentStatus.CALLING_MODEL),
    });
    state = conversationReducer(state, {
      type: "stream-update",
      update: {
        kind: "assistant",
        source: "model-call-1",
        createdAt: "2026-09-03T00:00:01Z",
        value: "answer ",
      },
    });
    state = conversationReducer(state, {
      type: "stream-update",
      update: {
        kind: "activity",
        resumeToken: "activity-1",
        source: "model-call-1",
        createdAt: "2026-09-03T00:00:02Z",
        value: {
          kind: EventKind.MODEL_COMPLETED,
          message: "complete",
          callId: null,
          toolName: null,
          messageSequence: null,
        },
      },
    });
    state = conversationReducer(state, {
      type: "stream-update",
      update: {
        kind: "assistant",
        source: "model-call-1",
        createdAt: "2026-09-03T00:00:03Z",
        value: "tail",
      },
    });
    state = conversationReducer(state, {
      type: "stream-update",
      update: {
        kind: "reasoning",
        source: "model-call-1",
        createdAt: "2026-09-03T00:00:04Z",
        value: "late summary",
      },
    });

    expect(state).toMatchObject({
      assistant: {
        source: "model-call-1",
        value: "answer tail",
        isComplete: true,
      },
      reasoning: [
        {
          source: "model-call-1",
          value: "late summary",
          isComplete: true,
        },
      ],
    });

    state = conversationReducer(state, {
      type: "snapshot-loaded",
      snapshot: snapshotWithStatus(AgentStatus.CALLING_MODEL),
    });
    expect(state).toMatchObject({ assistant: { value: "answer tail" } });

    state = conversationReducer(state, {
      type: "snapshot-loaded",
      snapshot: snapshotWithStatus(AgentStatus.WAITING_FOR_MESSAGE),
    });
    expect(state).toMatchObject({ assistant: null });

    state = conversationReducer(state, {
      type: "stream-update",
      update: {
        kind: "assistant",
        source: "model-call-1",
        createdAt: "2026-09-03T00:00:05Z",
        value: "replayed tail",
      },
    });
    expect(state).toMatchObject({ assistant: null });
  });

  it("shows a submitting queue item and restores the composer on failure", () => {
    let state = conversationReducer(initialConversationState(), {
      type: "snapshot-loaded",
      snapshot: snapshot("run-1", "queued-1", "existing"),
    });
    if (state.kind !== "ready" || state.lifecycle !== "active") {
      throw new Error("expected active state");
    }
    state = conversationReducer(state, {
      type: "composer-changed",
      value: "new work",
    });
    state = conversationReducer(state, {
      type: "plan-mode-changed",
      value: true,
    });
    state = conversationReducer(state, {
      type: "command-started",
      id: 7,
      command: {
        kind: "send",
        value: { content: "new work", planMode: true },
        submittedAfterSequence: 1,
        knownMessageIDs: ["queued-1"],
      },
    });

    expect(state).toMatchObject({
      composer: "",
      isPlanMode: false,
      optimisticSubmission: {
        localID: "submitting-7",
        value: { content: "new work", planMode: true },
      },
    });

    state = conversationReducer(state, {
      type: "command-failed",
      id: 7,
      message: "unavailable",
    });
    expect(state).toMatchObject({
      composer: "new work",
      isPlanMode: true,
      optimisticSubmission: null,
      error: "unavailable",
    });
  });

  it("replaces a submitting queue item with the stable Snapshot message", () => {
    let state = conversationReducer(initialConversationState(), {
      type: "snapshot-loaded",
      snapshot: snapshot("run-1", "queued-1", "existing"),
    });
    state = conversationReducer(state, {
      type: "command-started",
      id: 8,
      command: {
        kind: "send",
        value: { content: "new work", planMode: false },
        submittedAfterSequence: 1,
        knownMessageIDs: ["queued-1"],
      },
    });
    state = conversationReducer(state, { type: "command-succeeded", id: 8 });
    const reconciled = snapshot("run-1", "queued-2", "new work");
    state = conversationReducer(state, {
      type: "snapshot-loaded",
      snapshot: reconciled,
    });

    expect(state).toMatchObject({
      optimisticSubmission: null,
      snapshot: { queued: [{ messageId: "queued-2" }] },
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
    flowStatus: FlowStatus.RUNNING,
    errorType: null,
    errorMessage: null,
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

function snapshotWithStatus(status: AgentStatus): AgentSnapshot {
  const value = snapshot("run-1", "queued-1", "hello");
  if (value.description === null) throw new Error("expected active Snapshot");
  return {
    ...value,
    description: { ...value.description, status },
  };
}

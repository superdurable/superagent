/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import type {
  AgentEvent,
  AgentSnapshot,
  MessageId,
  PendingUserMessage,
} from "./api/generated";

export type ConnectionState = "live" | "reconnecting" | "stale" | "terminal";

export type QueueCommandAction = "delete" | "steer" | "edit";

export type Command =
  | { kind: "send" }
  | { kind: "approve" }
  | { kind: "execute-plan" }
  | {
      kind: "queue";
      action: QueueCommandAction;
      message: PendingUserMessage;
    };

export type LiveUpdate =
  | { kind: "assistant"; value: string }
  | { kind: "reasoning"; value: string }
  | { kind: "activity"; value: AgentEvent };

interface HistoryRequest {
  id: number;
  beforeSequence: number;
}

export interface ReadyConversationState {
  kind: "ready";
  snapshot: AgentSnapshot;
  connection: ConnectionState;
  snapshotRequest: number;
  subscriptionGeneration: number;
  historyRequest: HistoryRequest | null;
  pendingCommand: { id: number; command: Command } | null;
  composer: string;
  isPlanMode: boolean;
  assistantText: string;
  reasoningText: string;
  activities: AgentEvent[];
  error: string | null;
}

export type ConversationState =
  | { kind: "loading"; snapshotRequest: number }
  | { kind: "failed"; snapshotRequest: number; message: string }
  | ReadyConversationState;

export type ConversationAction =
  | { type: "snapshot-loaded"; snapshot: AgentSnapshot }
  | { type: "snapshot-failed"; message: string; isTerminal: boolean }
  | {
      type: "request-snapshot";
      connection: "live" | "reconnecting" | "stale";
    }
  | { type: "older-requested"; id: number; beforeSequence: number }
  | {
      type: "older-loaded";
      id: number;
      snapshot: AgentSnapshot;
    }
  | { type: "older-failed"; id: number; message: string }
  | { type: "stream-update"; update: LiveUpdate }
  | { type: "stream-failed"; message: string; isTerminal: boolean }
  | { type: "composer-changed"; value: string }
  | { type: "plan-mode-changed"; value: boolean }
  | { type: "command-started"; id: number; command: Command }
  | { type: "command-succeeded"; id: number }
  | { type: "command-failed"; id: number; message: string };

export function initialConversationState(): ConversationState {
  return { kind: "loading", snapshotRequest: 0 };
}

export function conversationReducer(
  state: ConversationState,
  action: ConversationAction,
): ConversationState {
  switch (action.type) {
    case "snapshot-loaded":
      if (state.kind !== "ready") return readyState(action.snapshot);
      return {
        ...state,
        snapshot: action.snapshot,
        connection: "live",
        subscriptionGeneration:
          state.connection === "reconnecting"
            ? state.subscriptionGeneration + 1
            : state.subscriptionGeneration,
        assistantText: "",
        reasoningText: "",
        error: null,
      };
    case "snapshot-failed":
      if (state.kind !== "ready") {
        return {
          kind: "failed",
          snapshotRequest: state.snapshotRequest,
          message: action.message,
        };
      }
      return {
        ...state,
        connection: action.isTerminal ? "terminal" : "stale",
        error: action.message,
      };
    case "request-snapshot":
      if (state.kind === "ready") {
        return {
          ...state,
          connection: action.connection,
          snapshotRequest: state.snapshotRequest + 1,
        };
      }
      return { kind: "loading", snapshotRequest: state.snapshotRequest + 1 };
    case "older-requested":
      return state.kind === "ready"
        ? {
            ...state,
            historyRequest: {
              id: action.id,
              beforeSequence: action.beforeSequence,
            },
          }
        : state;
    case "older-loaded":
      if (state.kind !== "ready" || state.historyRequest?.id !== action.id) {
        return state;
      }
      return {
        ...state,
        snapshot: {
          ...action.snapshot,
          history: {
            messages: [
              ...action.snapshot.history.messages,
              ...state.snapshot.history.messages,
            ],
            nextBeforeSequence: action.snapshot.history.nextBeforeSequence,
          },
        },
        historyRequest: null,
        error: null,
      };
    case "older-failed":
      if (state.kind !== "ready" || state.historyRequest?.id !== action.id) {
        return state;
      }
      return { ...state, historyRequest: null, error: action.message };
    case "stream-update":
      if (state.kind !== "ready") return state;
      return applyLiveUpdate(state, action.update);
    case "stream-failed":
      if (state.kind !== "ready") return state;
      return {
        ...state,
        connection: action.isTerminal ? "terminal" : "reconnecting",
        snapshotRequest: action.isTerminal
          ? state.snapshotRequest
          : state.snapshotRequest + 1,
        error: action.message,
      };
    case "composer-changed":
      return state.kind === "ready"
        ? { ...state, composer: action.value }
        : state;
    case "plan-mode-changed":
      return state.kind === "ready"
        ? { ...state, isPlanMode: action.value }
        : state;
    case "command-started":
      if (state.kind !== "ready" || state.pendingCommand !== null) return state;
      return beginCommand(state, action.id, action.command);
    case "command-succeeded":
      if (state.kind !== "ready" || state.pendingCommand?.id !== action.id) {
        return state;
      }
      return {
        ...state,
        composer:
          state.pendingCommand.command.kind === "send" ? "" : state.composer,
        isPlanMode:
          state.pendingCommand.command.kind === "send"
            ? false
            : state.isPlanMode,
        pendingCommand: null,
        snapshotRequest: state.snapshotRequest + 1,
        error: null,
      };
    case "command-failed":
      if (state.kind !== "ready" || state.pendingCommand?.id !== action.id) {
        return state;
      }
      return {
        ...state,
        pendingCommand: null,
        snapshotRequest: state.snapshotRequest + 1,
        error: action.message,
      };
  }
}

function readyState(snapshot: AgentSnapshot): ReadyConversationState {
  return {
    kind: "ready",
    snapshot,
    connection: "live",
    snapshotRequest: 0,
    subscriptionGeneration: 0,
    historyRequest: null,
    pendingCommand: null,
    composer: "",
    isPlanMode: false,
    assistantText: "",
    reasoningText: "",
    activities: [],
    error: null,
  };
}

function beginCommand(
  state: ReadyConversationState,
  id: number,
  command: Command,
): ReadyConversationState {
  if (command.kind !== "queue") {
    return { ...state, pendingCommand: { id, command }, error: null };
  }
  const queued = state.snapshot.queued.filter(
    (message) => message.messageId !== command.message.messageId,
  );
  return {
    ...state,
    snapshot: {
      ...state.snapshot,
      queued,
      description: {
        ...state.snapshot.description,
        pendingQueuedMessageCount: queued.length,
      },
    },
    composer:
      command.action === "edit"
        ? command.message.value.content
        : state.composer,
    isPlanMode:
      command.action === "edit"
        ? command.message.value.planMode
        : state.isPlanMode,
    pendingCommand: { id, command },
    error: null,
  };
}

function applyLiveUpdate(
  state: ReadyConversationState,
  update: LiveUpdate,
): ReadyConversationState {
  switch (update.kind) {
    case "assistant":
      return {
        ...state,
        assistantText: state.assistantText + update.value,
      };
    case "reasoning":
      return {
        ...state,
        reasoningText: state.reasoningText + update.value,
      };
    case "activity":
      return {
        ...state,
        activities: [update.value, ...state.activities].slice(0, 100),
      };
  }
}

export function pendingQueueMessageID(
  state: ConversationState,
): MessageId | null {
  if (state.kind !== "ready") return null;
  const command = state.pendingCommand?.command;
  return command?.kind === "queue" ? command.message.messageId : null;
}

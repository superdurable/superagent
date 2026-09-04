/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  AgentStatus,
  EventKind,
  type AgentDescription,
  type AgentEvent,
  type AgentSnapshot,
  type MessageId,
  type PendingUserMessage,
  type ResumeToken,
  type Sequence,
  type UserMessage,
} from "./api/generated";

export type ActiveConnectionState = "live" | "reconnecting" | "stale";
export type ConnectionState = ActiveConnectionState | "terminal";
export type QueueCommandAction = "delete" | "steer" | "edit";

export interface SendCommand {
  kind: "send";
  value: UserMessage;
  submittedAfterSequence: Sequence;
  knownMessageIDs: readonly MessageId[];
}

export type Command =
  | SendCommand
  | { kind: "approve" }
  | { kind: "execute-plan" }
  | {
      kind: "queue";
      action: QueueCommandAction;
      message: PendingUserMessage;
    };

interface LiveText {
  source: string;
  createdAt: string;
  value: string;
}

interface AssistantEntry extends LiveText {
  isComplete: boolean;
}

export interface ReasoningEntry extends LiveText {
  isComplete: boolean;
}

export interface ActivityEntry {
  resumeToken: ResumeToken;
  source: string;
  createdAt: string;
  value: AgentEvent;
}

export type LiveUpdate =
  | ({ kind: "assistant" } & LiveText)
  | ({ kind: "reasoning" } & LiveText)
  | ({ kind: "activity" } & ActivityEntry);

interface HistoryRequest {
  id: number;
  beforeSequence: Sequence;
}

interface OptimisticSubmission {
  localID: string;
  value: UserMessage;
  submittedAfterSequence: Sequence;
  knownMessageIDs: readonly MessageId[];
}

type ActiveSnapshot = AgentSnapshot & { description: AgentDescription };
type TerminalSnapshot = AgentSnapshot & { description: null };

interface ReadyConversationBase {
  kind: "ready";
  snapshotRequest: number;
  subscriptionGeneration: number;
  historyRequest: HistoryRequest | null;
  pendingCommand: { id: number; command: Command } | null;
  optimisticSubmission: OptimisticSubmission | null;
  composer: string;
  isPlanMode: boolean;
  assistant: AssistantEntry | null;
  reasoning: ReasoningEntry[];
  activities: ActivityEntry[];
  error: string | null;
}

export interface ActiveConversationState extends ReadyConversationBase {
  lifecycle: "active";
  snapshot: ActiveSnapshot;
  connection: ActiveConnectionState;
}

export interface TerminalConversationState extends ReadyConversationBase {
  lifecycle: "terminal";
  snapshot: TerminalSnapshot;
  connection: "terminal";
}

export type ReadyConversationState =
  ActiveConversationState | TerminalConversationState;

export type ConversationState =
  | { kind: "loading"; snapshotRequest: number }
  | { kind: "failed"; snapshotRequest: number; message: string }
  | ReadyConversationState;

export type ConversationAction =
  | { type: "snapshot-loaded"; snapshot: AgentSnapshot }
  | { type: "snapshot-failed"; message: string }
  | { type: "request-snapshot"; connection: ActiveConnectionState }
  | { type: "older-requested"; id: number; beforeSequence: Sequence }
  | { type: "older-loaded"; id: number; snapshot: AgentSnapshot }
  | { type: "older-failed"; id: number; message: string }
  | { type: "stream-update"; update: LiveUpdate }
  | { type: "stream-failed"; message: string }
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
      return reconcileSnapshot(state, action.snapshot);
    case "snapshot-failed":
      if (state.kind !== "ready") {
        return {
          kind: "failed",
          snapshotRequest: state.snapshotRequest,
          message: action.message,
        };
      }
      if (state.lifecycle === "terminal") return state;
      return { ...state, connection: "stale", error: action.message };
    case "request-snapshot":
      if (state.kind === "ready") {
        if (state.lifecycle === "terminal") return state;
        return {
          ...state,
          connection: action.connection,
          snapshotRequest: state.snapshotRequest + 1,
        };
      }
      return { kind: "loading", snapshotRequest: state.snapshotRequest + 1 };
    case "older-requested":
      return state.kind === "ready" && state.lifecycle === "active"
        ? {
            ...state,
            historyRequest: {
              id: action.id,
              beforeSequence: action.beforeSequence,
            },
          }
        : state;
    case "older-loaded":
      return mergeOlderHistory(state, action.id, action.snapshot);
    case "older-failed":
      if (
        state.kind !== "ready" ||
        state.lifecycle === "terminal" ||
        state.historyRequest?.id !== action.id
      ) {
        return state;
      }
      return { ...state, historyRequest: null, error: action.message };
    case "stream-update":
      return state.kind === "ready" && state.lifecycle === "active"
        ? applyLiveUpdate(state, action.update)
        : state;
    case "stream-failed":
      if (state.kind !== "ready" || state.lifecycle === "terminal") {
        return state;
      }
      return {
        ...state,
        connection: "reconnecting",
        snapshotRequest: state.snapshotRequest + 1,
        error: action.message,
      };
    case "composer-changed":
      return state.kind === "ready" && state.lifecycle === "active"
        ? { ...state, composer: action.value }
        : state;
    case "plan-mode-changed":
      return state.kind === "ready" && state.lifecycle === "active"
        ? { ...state, isPlanMode: action.value }
        : state;
    case "command-started":
      if (
        state.kind !== "ready" ||
        state.lifecycle === "terminal" ||
        state.pendingCommand !== null
      ) {
        return state;
      }
      return beginCommand(state, action.id, action.command);
    case "command-succeeded":
      if (
        state.kind !== "ready" ||
        state.lifecycle === "terminal" ||
        state.pendingCommand?.id !== action.id
      ) {
        return state;
      }
      return {
        ...state,
        pendingCommand: null,
        snapshotRequest: state.snapshotRequest + 1,
        error: null,
      };
    case "command-failed":
      return failCommand(state, action.id, action.message);
  }
}

function reconcileSnapshot(
  state: ConversationState,
  snapshot: AgentSnapshot,
): ReadyConversationState {
  if (snapshot.description === null) {
    return terminalState({ ...snapshot, description: null }, state);
  }
  const activeSnapshot = { ...snapshot, description: snapshot.description };
  const previous =
    state.kind === "ready" && state.lifecycle === "active" ? state : null;
  const hasDurableProgress =
    previous !== null &&
    snapshot.description.lastSequence >
      previous.snapshot.description.lastSequence;
  const hasCommittedAssistant =
    previous?.assistant?.isComplete === true &&
    snapshot.description.status !== AgentStatus.CALLING_MODEL;
  return {
    kind: "ready",
    lifecycle: "active",
    snapshot: activeSnapshot,
    connection: "live",
    snapshotRequest: state.snapshotRequest,
    subscriptionGeneration:
      previous?.connection === "reconnecting"
        ? previous.subscriptionGeneration + 1
        : (previous?.subscriptionGeneration ?? 0),
    historyRequest: null,
    pendingCommand: previous?.pendingCommand ?? null,
    optimisticSubmission: reconcileOptimisticSubmission(
      previous?.optimisticSubmission ?? null,
      activeSnapshot,
    ),
    composer: previous?.composer ?? "",
    isPlanMode: previous?.isPlanMode ?? false,
    assistant:
      hasDurableProgress || hasCommittedAssistant
        ? null
        : (previous?.assistant ?? null),
    reasoning: hasDurableProgress
      ? completeReasoning(previous.reasoning)
      : (previous?.reasoning ?? []),
    activities: previous?.activities ?? [],
    error: null,
  };
}

function terminalState(
  snapshot: AgentSnapshot & { description: null },
  previous: ConversationState,
): TerminalConversationState {
  const priorReady = previous.kind === "ready" ? previous : null;
  return {
    kind: "ready",
    lifecycle: "terminal",
    snapshot,
    connection: "terminal",
    snapshotRequest: previous.snapshotRequest,
    subscriptionGeneration: priorReady?.subscriptionGeneration ?? 0,
    historyRequest: null,
    pendingCommand: null,
    optimisticSubmission: null,
    composer: priorReady?.composer ?? "",
    isPlanMode: priorReady?.isPlanMode ?? false,
    assistant: null,
    reasoning: completeReasoning(priorReady?.reasoning ?? []),
    activities: priorReady?.activities ?? [],
    error: snapshot.errorMessage,
  };
}

function mergeOlderHistory(
  state: ConversationState,
  requestID: number,
  snapshot: AgentSnapshot,
): ConversationState {
  if (
    state.kind !== "ready" ||
    state.lifecycle === "terminal" ||
    state.historyRequest?.id !== requestID ||
    snapshot.description === null
  ) {
    return state;
  }
  return {
    ...state,
    snapshot: {
      ...snapshot,
      description: snapshot.description,
      history: {
        messages: [
          ...snapshot.history.messages,
          ...state.snapshot.history.messages,
        ],
        nextBeforeSequence: snapshot.history.nextBeforeSequence,
      },
    },
    historyRequest: null,
    error: null,
  };
}

function beginCommand(
  state: ActiveConversationState,
  id: number,
  command: Command,
): ActiveConversationState {
  if (command.kind === "send") {
    return {
      ...state,
      composer: "",
      isPlanMode: false,
      optimisticSubmission: {
        localID: `submitting-${String(id)}`,
        value: command.value,
        submittedAfterSequence: command.submittedAfterSequence,
        knownMessageIDs: command.knownMessageIDs,
      },
      pendingCommand: { id, command },
      error: null,
    };
  }
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

function failCommand(
  state: ConversationState,
  id: number,
  message: string,
): ConversationState {
  if (
    state.kind !== "ready" ||
    state.lifecycle === "terminal" ||
    state.pendingCommand?.id !== id
  ) {
    return state;
  }
  const command = state.pendingCommand.command;
  return {
    ...state,
    composer: command.kind === "send" ? command.value.content : state.composer,
    isPlanMode:
      command.kind === "send" ? command.value.planMode : state.isPlanMode,
    optimisticSubmission:
      command.kind === "send" ? null : state.optimisticSubmission,
    pendingCommand: null,
    snapshotRequest: state.snapshotRequest + 1,
    error: message,
  };
}

function reconcileOptimisticSubmission(
  submission: OptimisticSubmission | null,
  snapshot: AgentSnapshot & { description: AgentDescription },
): OptimisticSubmission | null {
  if (submission === null) return null;
  const knownIDs = new Set(submission.knownMessageIDs);
  const isNowQueued = [...snapshot.queued, ...snapshot.steered].some(
    (message) =>
      !knownIDs.has(message.messageId) &&
      sameUserMessage(message.value, submission.value),
  );
  const isNowDurable = snapshot.history.messages.some(
    ({ sequence, message }) =>
      sequence > submission.submittedAfterSequence &&
      message.role === "user" &&
      message.content === submission.value.content,
  );
  return isNowQueued || isNowDurable ? null : submission;
}

function sameUserMessage(left: UserMessage, right: UserMessage): boolean {
  return left.content === right.content && left.planMode === right.planMode;
}

function applyLiveUpdate(
  state: ActiveConversationState,
  update: LiveUpdate,
): ActiveConversationState {
  switch (update.kind) {
    case "assistant": {
      const isComplete = isModelFinishedForSource(
        state.activities,
        update.source,
      );
      if (
        isComplete &&
        state.snapshot.description.status === AgentStatus.WAITING_FOR_MESSAGE
      ) {
        return state;
      }
      return {
        ...state,
        assistant: appendAssistant(state.assistant, update, isComplete),
      };
    }
    case "reasoning":
      return {
        ...state,
        reasoning: appendReasoning(
          state.reasoning,
          update,
          isModelFinishedForSource(state.activities, update.source),
        ),
      };
    case "activity":
      return {
        ...state,
        assistant: isModelFinished(update.value.kind)
          ? state.snapshot.description.status ===
            AgentStatus.WAITING_FOR_MESSAGE
            ? null
            : completeAssistantSource(state.assistant, update.source)
          : state.assistant,
        reasoning: isModelFinished(update.value.kind)
          ? completeReasoningSource(state.reasoning, update.source)
          : state.reasoning,
        activities: [update, ...state.activities].slice(0, 100),
      };
  }
}

function appendAssistant(
  current: AssistantEntry | null,
  update: LiveText,
  isComplete: boolean,
): AssistantEntry {
  if (current?.source !== update.source) return { ...update, isComplete };
  return {
    ...current,
    value: current.value + update.value,
    isComplete: current.isComplete || isComplete,
  };
}

function appendReasoning(
  entries: ReasoningEntry[],
  update: LiveText,
  isComplete: boolean,
): ReasoningEntry[] {
  const index = entries.findIndex((entry) => entry.source === update.source);
  if (index < 0) {
    return [...entries, { ...update, isComplete }].slice(-20);
  }
  return entries.map((entry, entryIndex) =>
    entryIndex === index
      ? {
          ...entry,
          value: entry.value + update.value,
          isComplete: entry.isComplete || isComplete,
        }
      : entry,
  );
}

function completeAssistantSource(
  entry: AssistantEntry | null,
  source: string,
): AssistantEntry | null {
  return entry?.source === source ? { ...entry, isComplete: true } : entry;
}

function completeReasoning(entries: ReasoningEntry[]): ReasoningEntry[] {
  return entries.map((entry) => ({ ...entry, isComplete: true }));
}

function completeReasoningSource(
  entries: ReasoningEntry[],
  source: string,
): ReasoningEntry[] {
  return entries.map((entry) =>
    entry.source === source ? { ...entry, isComplete: true } : entry,
  );
}

function isModelFinished(kind: AgentEvent["kind"]): boolean {
  return kind === EventKind.MODEL_COMPLETED || kind === EventKind.MODEL_FAILED;
}

function isModelFinishedForSource(
  activities: ActivityEntry[],
  source: string,
): boolean {
  return activities.some(
    (entry) => entry.source === source && isModelFinished(entry.value.kind),
  );
}

export function pendingQueueMessageID(
  state: ConversationState,
): MessageId | null {
  if (state.kind !== "ready") return null;
  const command = state.pendingCommand?.command;
  return command?.kind === "queue" ? command.message.messageId : null;
}

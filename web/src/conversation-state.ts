/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  AgentStatus,
  EventKind,
  MessageRole,
  type TaskStatus,
  type AgentEvent,
  type AgentSnapshot,
  type CallId,
  type HistoryPage,
  type MessageId,
  type PendingUserMessage,
  type ResumeToken,
  type Sequence,
  type UserMessage,
} from "./api/generated";

export type ActiveConnectionState = "live" | "reconnecting" | "stale";
export type ConnectionState = ActiveConnectionState;
export type ReconciliationState = "open" | "syncing" | "stale";
export type QueueCommandAction = "delete" | "steer" | "edit";

export interface SendCommand {
  kind: "send";
  value: UserMessage;
  submittedAfterSequence: Sequence;
  knownMessageIDs: readonly MessageId[];
}

export interface AnswerCommand {
  kind: "answer";
  callID: CallId;
  value: UserMessage;
  submittedAfterSequence: Sequence;
  knownMessageIDs: readonly MessageId[];
}

export type Command =
  | SendCommand
  | AnswerCommand
  | { kind: "approve" }
  | { kind: "recover" }
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

export interface AssistantEntry extends LiveText {
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

export interface ConsumedUserEntry {
  messageId: MessageId;
  value: UserMessage;
  createdAt: string;
  consumedAfterSequence: Sequence;
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
  phase: "submitting" | "queued";
}

interface PendingAnsweredUserInput {
  callID: CallId;
  value: UserMessage;
  submittedAfterSequence: Sequence;
}

interface PlanTaskProgress {
  index: number;
  status: TaskStatus;
}

interface PlanProgressHint {
  baseRevision: number;
  revision: number;
  tasks: PlanTaskProgress[];
}

interface ReadyConversationBase {
  kind: "ready";
  subscriptionGeneration: number;
  historyRequest: HistoryRequest | null;
  pendingCommand: { id: number; command: Command } | null;
  pendingAnsweredUserInput: PendingAnsweredUserInput | null;
  optimisticSubmissions: OptimisticSubmission[];
  composer: string;
  isPlanMode: boolean;
  assistant: AssistantEntry | null;
  reasoning: ReasoningEntry[];
  activities: ActivityEntry[];
  consumedUserMessages: ConsumedUserEntry[];
  planProgress: PlanProgressHint | null;
  isWaitingForInput: boolean;
  reconciliation: ReconciliationState;
  commandError: string | null;
  error: string | null;
}

export interface ActiveConversationState extends ReadyConversationBase {
  snapshot: AgentSnapshot;
  connection: ActiveConnectionState;
}

export type ReadyConversationState = ActiveConversationState;

export type ConversationState =
  | { kind: "loading" }
  | { kind: "failed"; message: string }
  | ReadyConversationState;

export type ConversationAction =
  | { type: "snapshot-loaded"; snapshot: AgentSnapshot }
  | { type: "snapshot-failed"; message: string }
  | {
      type: "snapshot-requested";
      blocking: boolean;
      connection: ActiveConnectionState;
    }
  | { type: "older-requested"; id: number; beforeSequence: Sequence }
  | { type: "older-loaded"; id: number; page: HistoryPage }
  | { type: "older-failed"; id: number; message: string }
  | { type: "stream-update"; update: LiveUpdate }
  | { type: "stream-recovered"; updates: LiveUpdate[] }
  | { type: "stream-failed"; message: string }
  | { type: "composer-changed"; value: string }
  | { type: "plan-mode-changed"; value: boolean }
  | { type: "command-started"; id: number; command: Command }
  | { type: "command-succeeded"; id: number }
  | { type: "command-failed"; id: number; message: string };

export function initialConversationState(): ConversationState {
  return { kind: "loading" };
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
          message: action.message,
        };
      }
      return {
        ...state,
        connection: "stale",
        reconciliation: "stale",
        error: action.message,
      };
    case "snapshot-requested":
      if (state.kind === "ready") {
        return {
          ...state,
          connection: action.connection,
          reconciliation: action.blocking ? "syncing" : state.reconciliation,
        };
      }
      return { kind: "loading" };
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
      return mergeOlderHistory(state, action.id, action.page);
    case "older-failed":
      if (state.kind !== "ready" || state.historyRequest?.id !== action.id) {
        return state;
      }
      return { ...state, historyRequest: null, error: action.message };
    case "stream-update":
      return state.kind === "ready"
        ? applyLiveUpdate(state, action.update)
        : state;
    case "stream-recovered":
      return action.updates.reduce<ConversationState>(
        (current, update) =>
          current.kind === "ready" ? applyLiveUpdate(current, update) : current,
        state,
      );
    case "stream-failed":
      if (state.kind !== "ready") {
        return state;
      }
      return {
        ...state,
        connection: "reconnecting",
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
      if (state.kind !== "ready" || state.pendingCommand !== null) {
        return state;
      }
      return beginCommand(state, action.id, action.command);
    case "command-succeeded":
      if (state.kind !== "ready" || state.pendingCommand?.id !== action.id) {
        return state;
      }
      return completeCommand(state, action.id);
    case "command-failed":
      return failCommand(state, action.id, action.message);
  }
}

function reconcileSnapshot(
  state: ConversationState,
  snapshot: AgentSnapshot,
): ReadyConversationState {
  const previous = state.kind === "ready" ? state : null;
  const previousRun =
    previous?.snapshot.runId === snapshot.runId ? previous : null;
  const history =
    previousRun !== null
      ? reconcileHistory(
          previousRun.snapshot.history,
          snapshot.history,
          snapshot.description.firstRetainedSequence,
        )
      : snapshot.history;
  const previousPendingAnsweredUserInput =
    previousRun?.pendingAnsweredUserInput ?? null;
  const pendingAnsweredUserInput =
    previousPendingAnsweredUserInput !== null &&
    history.messages.some(
      ({ sequence, message }) =>
        sequence > previousPendingAnsweredUserInput.submittedAfterSequence &&
        message.role === MessageRole.USER &&
        message.content === previousPendingAnsweredUserInput.value.content,
    )
      ? null
      : previousPendingAnsweredUserInput;
  const consumedUserMessages = reconcileConsumedUserMessages(
    previousRun?.consumedUserMessages ?? [],
    history,
  );
  const consumedMessageIDs = new Set(
    consumedUserMessages.map((message) => message.messageId),
  );
  const queued = snapshot.queued.filter(
    (message) => !consumedMessageIDs.has(message.messageId),
  );
  const steered = snapshot.steered.filter(
    (message) => !consumedMessageIDs.has(message.messageId),
  );
  const activeSnapshot = {
    ...snapshot,
    description: {
      ...snapshot.description,
      pendingUserInput:
        pendingAnsweredUserInput?.callID ===
        snapshot.description.pendingUserInput?.callId
          ? null
          : snapshot.description.pendingUserInput,
      pendingQueuedMessageCount: queued.length,
      pendingSteeredMessageCount: steered.length,
    },
    history,
    queued,
    steered,
  };
  const hasDurableProgress =
    previousRun !== null &&
    snapshot.description.lastSequence >
      previousRun.snapshot.description.lastSequence;
  const hasCommittedAssistant =
    previousRun?.assistant?.isComplete === true &&
    snapshot.description.status !== AgentStatus.CALLING_MODEL;
  return {
    kind: "ready",
    snapshot: activeSnapshot,
    connection: "live",
    subscriptionGeneration:
      previousRun?.connection === "reconnecting"
        ? previousRun.subscriptionGeneration + 1
        : (previousRun?.subscriptionGeneration ?? 0),
    historyRequest: null,
    pendingCommand: previousRun?.pendingCommand ?? null,
    pendingAnsweredUserInput,
    optimisticSubmissions: reconcileOptimisticSubmissions(
      previousRun?.optimisticSubmissions ?? [],
      activeSnapshot,
    ),
    composer: previousRun?.composer ?? "",
    isPlanMode: previousRun?.isPlanMode ?? false,
    assistant:
      hasDurableProgress || hasCommittedAssistant
        ? null
        : (previousRun?.assistant ?? null),
    reasoning: hasDurableProgress
      ? completeReasoning(previousRun.reasoning)
      : (previousRun?.reasoning ?? []),
    activities: previousRun?.activities ?? [],
    consumedUserMessages,
    planProgress: null,
    isWaitingForInput:
      snapshot.description.status === AgentStatus.WAITING_FOR_MESSAGE &&
      snapshot.description.pendingQueuedMessageCount === 0 &&
      snapshot.description.pendingSteeredMessageCount === 0 &&
      !snapshot.description.isPlanExecutionRequested,
    reconciliation: "open",
    commandError: previousRun?.commandError ?? null,
    error: previousRun?.commandError ?? null,
  };
}

function mergeOlderHistory(
  state: ConversationState,
  requestID: number,
  page: HistoryPage,
): ConversationState {
  if (state.kind !== "ready" || state.historyRequest?.id !== requestID) {
    return state;
  }
  return {
    ...state,
    snapshot: {
      ...state.snapshot,
      history: {
        messages: mergeMessages(page.messages, state.snapshot.history.messages),
        nextBeforeSequence: page.nextBeforeSequence,
      },
    },
    historyRequest: null,
    error: state.commandError,
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
      optimisticSubmissions: [
        ...state.optimisticSubmissions,
        {
          localID: `submitting-${String(id)}`,
          value: command.value,
          submittedAfterSequence: command.submittedAfterSequence,
          knownMessageIDs: command.knownMessageIDs,
          phase: "submitting",
        },
      ],
      pendingCommand: { id, command },
      commandError: null,
      error: null,
    };
  }
  if (command.kind !== "queue") {
    return {
      ...state,
      pendingCommand: { id, command },
      commandError: null,
      error: null,
    };
  }
  const queued = state.snapshot.queued.filter(
    (message) => message.messageId !== command.message.messageId,
  );
  const steered =
    command.action === "steer"
      ? [...state.snapshot.steered, command.message]
      : state.snapshot.steered;
  return {
    ...state,
    snapshot: {
      ...state.snapshot,
      queued,
      steered,
      description: {
        ...state.snapshot.description,
        pendingQueuedMessageCount: queued.length,
        pendingSteeredMessageCount: steered.length,
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
    commandError: null,
    error: null,
  };
}

function completeCommand(
  state: ActiveConversationState,
  id: number,
): ActiveConversationState {
  if (state.pendingCommand?.id !== id) return state;
  const command = state.pendingCommand.command;
  if (command.kind === "send" || command.kind === "answer") {
    const isAnswer = command.kind === "answer";
    return {
      ...state,
      pendingCommand: null,
      pendingAnsweredUserInput: isAnswer
        ? {
            callID: command.callID,
            value: command.value,
            submittedAfterSequence: command.submittedAfterSequence,
          }
        : state.pendingAnsweredUserInput,
      snapshot: {
        ...state.snapshot,
        description: {
          ...state.snapshot.description,
          pendingUserInput: isAnswer
            ? null
            : state.snapshot.description.pendingUserInput,
        },
      },
      optimisticSubmissions:
        command.kind === "send"
          ? state.optimisticSubmissions.map((submission) =>
              submission.localID === `submitting-${String(id)}`
                ? { ...submission, phase: "queued" }
                : submission,
            )
          : state.optimisticSubmissions,
      error: null,
    };
  }
  if (command.kind === "approve") {
    return {
      ...state,
      pendingCommand: null,
      snapshot: {
        ...state.snapshot,
        description: {
          ...state.snapshot.description,
          pendingApproval: null,
        },
      },
      error: null,
    };
  }
  if (command.kind === "recover") {
    return {
      ...state,
      pendingCommand: null,
      snapshot: {
        ...state.snapshot,
        description: {
          ...state.snapshot.description,
          pendingToolRecovery: null,
        },
      },
      error: null,
    };
  }
  if (command.kind === "execute-plan") {
    return {
      ...state,
      pendingCommand: null,
      snapshot: {
        ...state.snapshot,
        description: {
          ...state.snapshot.description,
          isPlanExecutionRequested: true,
        },
      },
      error: null,
    };
  }
  return { ...state, pendingCommand: null, error: null };
}

function mergeMessages(
  older: AgentSnapshot["history"]["messages"],
  current: AgentSnapshot["history"]["messages"],
): AgentSnapshot["history"]["messages"] {
  const messages = new Map<number, (typeof current)[number]>();
  for (const message of [...older, ...current]) {
    messages.set(message.sequence, message);
  }
  return [...messages.values()].sort(
    (left, right) => left.sequence - right.sequence,
  );
}

function reconcileHistory(
  previous: HistoryPage,
  current: HistoryPage,
  firstRetainedSequence: Sequence,
): HistoryPage {
  const messages = mergeMessages(previous.messages, current.messages).filter(
    (message) => message.sequence >= firstRetainedSequence,
  );
  const firstSequence = messages[0]?.sequence;
  return {
    messages,
    nextBeforeSequence:
      firstSequence !== undefined && firstSequence > firstRetainedSequence
        ? firstSequence
        : null,
  };
}

function failCommand(
  state: ConversationState,
  id: number,
  message: string,
): ConversationState {
  if (state.kind !== "ready" || state.pendingCommand?.id !== id) {
    return state;
  }
  const command = state.pendingCommand.command;
  return {
    ...state,
    composer: command.kind === "send" ? command.value.content : state.composer,
    isPlanMode:
      command.kind === "send" ? command.value.planMode : state.isPlanMode,
    optimisticSubmissions:
      command.kind === "send"
        ? state.optimisticSubmissions.filter(
            (submission) => submission.localID !== `submitting-${String(id)}`,
          )
        : state.optimisticSubmissions,
    pendingCommand: null,
    commandError: message,
    error: message,
  };
}

function reconcileOptimisticSubmissions(
  submissions: OptimisticSubmission[],
  snapshot: AgentSnapshot,
): OptimisticSubmission[] {
  const claimed = new Set<string>();
  return submissions.filter((submission) => {
    const knownIDs = new Set(submission.knownMessageIDs);
    const queued = [...snapshot.queued, ...snapshot.steered].find(
      (message) =>
        !knownIDs.has(message.messageId) &&
        !claimed.has(`queue:${message.messageId}`) &&
        sameUserMessage(message.value, submission.value),
    );
    if (queued !== undefined) {
      claimed.add(`queue:${queued.messageId}`);
      return false;
    }
    const durable = snapshot.history.messages.find(
      ({ sequence, message }) =>
        sequence > submission.submittedAfterSequence &&
        !claimed.has(`history:${String(sequence)}`) &&
        message.role === MessageRole.USER &&
        message.content === submission.value.content,
    );
    if (durable !== undefined) {
      claimed.add(`history:${String(durable.sequence)}`);
      return false;
    }
    return true;
  });
}

function reconcileConsumedUserMessages(
  messages: ConsumedUserEntry[],
  history: HistoryPage,
): ConsumedUserEntry[] {
  const claimedSequences = new Set<Sequence>();
  return messages.filter((consumed) => {
    const durable = history.messages.find(
      ({ sequence, message }) =>
        sequence > consumed.consumedAfterSequence &&
        !claimedSequences.has(sequence) &&
        message.role === MessageRole.USER &&
        message.content === consumed.value.content,
    );
    if (durable === undefined) return true;
    claimedSequences.add(durable.sequence);
    return false;
  });
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
      if (update.value.kind === EventKind.SNAPSHOT_REQUIRED) {
        return state;
      }
      if (hasActivity(state.activities, update)) {
        return state;
      }
      return applyAnsweredUserInput(
        applyInputConsumption(
          {
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
            activities: [...state.activities, update],
            planProgress: applyPlanTaskUpdate(state, update.value),
          },
          update,
        ),
        update,
      );
  }
}

function hasActivity(
  activities: readonly ActivityEntry[],
  update: ActivityEntry,
): boolean {
  return activities.some(
    (activity) =>
      activity.resumeToken === update.resumeToken ||
      (update.value.kind === EventKind.USER_INPUT_ANSWERED &&
        activity.value.kind === update.value.kind &&
        activity.value.callId === update.value.callId &&
        activity.value.messageSequence === update.value.messageSequence),
  );
}

function applyAnsweredUserInput(
  state: ActiveConversationState,
  update: ActivityEntry,
): ActiveConversationState {
  const event = update.value;
  const pending = state.pendingAnsweredUserInput;
  if (
    event.kind !== EventKind.USER_INPUT_ANSWERED ||
    event.callId === null ||
    event.messageSequence === null ||
    pending?.callID !== event.callId
  ) {
    return state;
  }
  const projectedMessage = {
    sequence: event.messageSequence,
    message: {
      role: MessageRole.USER,
      content: pending.value.content,
      toolCalls: [],
      toolCallId: null,
      toolName: null,
      createdAt: update.createdAt,
    },
  };
  return {
    ...state,
    pendingAnsweredUserInput: null,
    isWaitingForInput: false,
    snapshot: {
      ...state.snapshot,
      history: {
        ...state.snapshot.history,
        messages: mergeMessages(state.snapshot.history.messages, [
          projectedMessage,
        ]),
      },
    },
  };
}

function applyInputConsumption(
  state: ActiveConversationState,
  update: ActivityEntry,
): ActiveConversationState {
  const event = update.value;
  if (
    event.kind !== EventKind.INPUT_CONSUMED ||
    event.inputConsumption === null
  ) {
    return state;
  }
  const queuedIDs = new Set(event.inputConsumption.queuedMessageIds);
  const steeredIDs = new Set(event.inputConsumption.steeredMessageIds);
  const consumedIDs = new Set([...queuedIDs, ...steeredIDs]);
  const pendingByID = new Map(
    [...state.snapshot.queued, ...state.snapshot.steered].map((message) => [
      message.messageId,
      message,
    ]),
  );
  const projectedIDs = new Set(
    state.consumedUserMessages.map((message) => message.messageId),
  );
  const consumedAfterSequence = state.activities.reduce(
    (latestSequence, activity) =>
      activity.value.messageSequence === null
        ? latestSequence
        : Math.max(latestSequence, activity.value.messageSequence),
    state.snapshot.description.lastSequence,
  );
  const newlyConsumedUserMessages = [...consumedIDs]
    .filter((messageID) => !projectedIDs.has(messageID))
    .flatMap((messageID): ConsumedUserEntry[] => {
      const pending = pendingByID.get(messageID);
      return pending === undefined
        ? []
        : [
            {
              messageId: messageID,
              value: pending.value,
              createdAt: update.createdAt,
              consumedAfterSequence,
            },
          ];
    });
  const queued = state.snapshot.queued.filter(
    (message) => !consumedIDs.has(message.messageId),
  );
  const steered = state.snapshot.steered.filter(
    (message) => !consumedIDs.has(message.messageId),
  );
  const consumedPlanRevision = event.inputConsumption.planExecutionRevision;
  const didConsumePlanRequest =
    consumedPlanRevision !== null &&
    state.snapshot.description.isPlanExecutionRequested &&
    state.snapshot.description.plan?.revision === consumedPlanRevision;
  const didConsumeVisibleInput =
    queued.length !== state.snapshot.queued.length ||
    steered.length !== state.snapshot.steered.length ||
    didConsumePlanRequest;
  return {
    ...state,
    isWaitingForInput: didConsumeVisibleInput ? false : state.isWaitingForInput,
    consumedUserMessages: [
      ...state.consumedUserMessages,
      ...newlyConsumedUserMessages,
    ],
    snapshot: {
      ...state.snapshot,
      queued,
      steered,
      description: {
        ...state.snapshot.description,
        pendingQueuedMessageCount: queued.length,
        pendingSteeredMessageCount: steered.length,
        isPlanExecutionRequested: didConsumePlanRequest
          ? false
          : state.snapshot.description.isPlanExecutionRequested,
      },
    },
  };
}

function applyPlanTaskUpdate(
  state: ActiveConversationState,
  event: AgentEvent,
): PlanProgressHint | null {
  if (
    event.kind !== EventKind.PLAN_TASK_UPDATED ||
    event.planBaseRevision == null ||
    event.planRevision == null ||
    event.planTaskIndex == null ||
    event.planTaskStatus == null
  ) {
    return state.planProgress;
  }
  const plan = state.snapshot.description.plan;
  if (plan?.revision !== event.planBaseRevision) {
    return state.planProgress;
  }
  if (event.planTaskIndex < 0 || event.planTaskIndex >= plan.tasks.length) {
    return state.planProgress;
  }
  const current = state.planProgress;
  const tasks =
    current?.baseRevision === event.planBaseRevision &&
    current.revision === event.planRevision
      ? current.tasks
      : [];
  return {
    baseRevision: event.planBaseRevision,
    revision: event.planRevision,
    tasks: [
      ...tasks.filter(({ index }) => index !== event.planTaskIndex),
      { index: event.planTaskIndex, status: event.planTaskStatus },
    ],
  };
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

export function displayedPlanTaskStatus(
  state: ActiveConversationState,
  index: number,
): TaskStatus | undefined {
  const plan = state.snapshot.description.plan;
  if (plan === null) return undefined;
  const live = state.planProgress?.tasks.find((task) => task.index === index);
  return live?.status ?? plan.tasks[index]?.status;
}

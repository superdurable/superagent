/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import type {
  TimelineActivityEntry,
  TimelineConsumedUserEntry,
  TimelineLiveTextEntry,
  TimelineSequencedMessage,
  TimelineToolCall,
} from "./conversationTimeline.js";

export type ConversationOperationStatus =
  "running" | "waiting" | "complete" | "failed";
export type ConversationStreamState = "streaming" | "complete" | "disconnected";

export interface ConversationPendingWait {
  kind: "approval" | "question" | "timer" | "recovery";
  startedAt: string | null;
  callId?: string | null;
}

export interface ConversationModelWorkItem {
  key: string;
  messageSequence: number | null;
  status: ConversationOperationStatus;
  reasoning: TimelineLiveTextEntry | null;
  durationMs: number | null;
  interval: readonly [number, number] | null;
}

export interface ConversationToolWorkItem {
  call: TimelineToolCall;
  result: TimelineSequencedMessage | null;
  status: ConversationOperationStatus;
  durationMs: number | null;
  progressCount: number;
  attemptCount: number | null;
  lastActivity: string | null;
  interval: readonly [number, number] | null;
}

export interface ConversationToolGroup {
  key: string;
  name: string;
  calls: ConversationToolWorkItem[];
}

export interface ConversationTurn {
  key: string;
  messages: TimelineSequencedMessage[];
  consumedUserMessages: TimelineConsumedUserEntry[];
  models: ConversationModelWorkItem[];
  tools: ConversationToolGroup[];
  activities: TimelineActivityEntry[];
  durationMs: number | null;
  workDurationMs: number | null;
  waitDurationMs: number | null;
}

export interface ConversationPresentation {
  turns: ConversationTurn[];
  earlierActivity: (TimelineActivityEntry | TimelineLiveTextEntry)[];
}

export interface ConversationPresentationInput {
  messages: readonly TimelineSequencedMessage[];
  consumedUserMessages?: readonly TimelineConsumedUserEntry[];
  reasoning?: readonly TimelineLiveTextEntry[];
  activities?: readonly TimelineActivityEntry[];
  assistant?: TimelineLiveTextEntry | null;
  pendingWaits?: readonly ConversationPendingWait[];
  isModelRunning?: boolean;
}

const MODEL_TERMINAL = new Set(["model_completed", "model_failed"]);
const MODEL_LIFECYCLE = new Set(["model_started", ...MODEL_TERMINAL]);

function timestamp(value: string | null | undefined): number | null {
  const parsed = Date.parse(value ?? "");
  return Number.isFinite(parsed) ? parsed : null;
}

export function formatDuration(durationMs: number | null): string | null {
  if (durationMs === null || !Number.isFinite(durationMs) || durationMs < 0)
    return null;
  const seconds = Math.floor(durationMs / 1_000);
  if (seconds < 1) return "<1s";
  if (seconds < 60) return `${String(seconds)}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60)
    return `${String(minutes)}m ${String(seconds % 60).padStart(2, "0")}s`;
  return `${String(Math.floor(minutes / 60))}h ${String(minutes % 60).padStart(2, "0")}m`;
}

export function unionDuration(
  intervals: readonly (readonly [number, number] | null)[],
): number | null {
  const valid = intervals
    .filter(
      (value): value is readonly [number, number] =>
        value !== null && value[1] >= value[0],
    )
    .map(([start, end]) => [start, end] as [number, number])
    .sort((left, right) => left[0] - right[0]);
  if (valid.length === 0) return null;
  const first = valid[0];
  if (first === undefined) return null;
  let total = 0;
  let [start, end] = first;
  for (const [nextStart, nextEnd] of valid.slice(1)) {
    if (nextStart <= end) end = Math.max(end, nextEnd);
    else {
      total += end - start;
      start = nextStart;
      end = nextEnd;
    }
  }
  return total + end - start;
}

function canonicalize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([key, child]) => [key, canonicalize(child)]),
    );
  }
  return value;
}

export function toolCallFingerprint(call: TimelineToolCall): string {
  try {
    return `${call.name}:${JSON.stringify(canonicalize(JSON.parse(call.argumentsJson)))}`;
  } catch {
    return `${call.name}:${call.argumentsJson}`;
  }
}

function messageInterval(
  entry: TimelineSequencedMessage,
): readonly [number, number] | null {
  const start = timestamp(entry.message.startedAt);
  const end = timestamp(entry.message.createdAt);
  return start === null || end === null || end < start ? null : [start, end];
}

function groupDurableTurns(messages: readonly TimelineSequencedMessage[]) {
  const turns: ConversationTurn[] = [];
  const sequenceToTurn = new Map<number, ConversationTurn>();
  let current: ConversationTurn | null = null;
  for (const entry of messages) {
    if (entry.message.role === "user" || current === null) {
      current = {
        key: `turn-${String(entry.sequence)}`,
        messages: [],
        consumedUserMessages: [],
        models: [],
        tools: [],
        activities: [],
        durationMs: null,
        workDurationMs: null,
        waitDurationMs: null,
      };
      turns.push(current);
    }
    current.messages.push(entry);
    sequenceToTurn.set(entry.sequence, current);
  }
  return { turns, sequenceToTurn };
}

export function buildConversationPresentation(
  input: ConversationPresentationInput,
  nowMs = Date.now(),
): ConversationPresentation {
  const messages = [...input.messages].sort(
    (left, right) => left.sequence - right.sequence,
  );
  const activities = [...(input.activities ?? [])].sort(
    (left, right) =>
      (timestamp(left.createdAt) ?? 0) - (timestamp(right.createdAt) ?? 0),
  );
  const { turns, sequenceToTurn } = groupDurableTurns(messages);
  const earlierActivity: (TimelineActivityEntry | TimelineLiveTextEntry)[] = [];
  const resultByCall = new Map<string, TimelineSequencedMessage>();
  const callToTurn = new Map<string, ConversationTurn>();
  for (const turn of turns) {
    for (const entry of turn.messages) {
      if (entry.message.role === "tool" && entry.message.toolCallId)
        resultByCall.set(entry.message.toolCallId, entry);
      for (const call of entry.message.toolCalls) {
        callToTurn.set(call.id, turn);
      }
    }
  }

  const activityByCall = new Map<string, TimelineActivityEntry[]>();
  const activityBySource = new Map<string, TimelineActivityEntry[]>();
  for (const activity of activities) {
    if (activity.value.callId) {
      const values = activityByCall.get(activity.value.callId) ?? [];
      values.push(activity);
      activityByCall.set(activity.value.callId, values);
    }
    if (MODEL_LIFECYCLE.has(activity.value.kind)) {
      const values = activityBySource.get(activity.source) ?? [];
      values.push(activity);
      activityBySource.set(activity.source, values);
    }
    const turn = activity.value.callId
      ? callToTurn.get(activity.value.callId)
      : activity.value.messageSequence === null
        ? undefined
        : sequenceToTurn.get(activity.value.messageSequence);
    if (turn) turn.activities.push(activity);
    else if (!MODEL_LIFECYCLE.has(activity.value.kind))
      earlierActivity.push(activity);
  }

  for (const consumed of input.consumedUserMessages ?? []) {
    let target =
      turns.find(
        (turn) => Number(turn.key.slice(5)) > consumed.consumedAfterSequence,
      ) ?? turns.at(-1);
    if (target === undefined) {
      target = {
        key: `consumed-${consumed.messageId}`,
        messages: [],
        consumedUserMessages: [],
        models: [],
        tools: [],
        activities: [],
        durationMs: null,
        workDurationMs: null,
        waitDurationMs: null,
      };
      turns.push(target);
    }
    target.consumedUserMessages.push(consumed);
  }

  const reasoningBySource = new Map(
    (input.reasoning ?? []).map((entry) => [entry.source, entry]),
  );
  const usedReasoning = new Set<string>();
  const usedModelSources = new Set<string>();
  const pendingWaitByCall = new Map(
    (input.pendingWaits ?? []).flatMap((wait) =>
      wait.callId ? [[wait.callId, wait] as const] : [],
    ),
  );
  for (const turn of turns) {
    const groups = new Map<string, ConversationToolGroup>();
    for (const entry of turn.messages) {
      if (entry.message.role === "assistant") {
        const interval = messageInterval(entry);
        const modelActivities = activities.filter(
          (event) =>
            event.value.messageSequence === entry.sequence &&
            MODEL_LIFECYCLE.has(event.value.kind),
        );
        const source = modelActivities[0]?.source;
        if (source) usedModelSources.add(source);
        const reasoning = source
          ? (reasoningBySource.get(source) ?? null)
          : null;
        if (reasoning) usedReasoning.add(reasoning.source);
        const failed = modelActivities.some(
          (event) => event.value.kind === "model_failed",
        );
        const terminal = modelActivities.some((event) =>
          MODEL_TERMINAL.has(event.value.kind),
        );
        const activityStart = timestamp(
          modelActivities.find((event) => event.value.kind === "model_started")
            ?.createdAt,
        );
        const running = Boolean(
          input.isModelRunning && !terminal && activityStart !== null,
        );
        const effectiveInterval =
          running && activityStart !== null && nowMs >= activityStart
            ? ([activityStart, nowMs] as const)
            : interval;
        turn.models.push({
          key: `model-${String(entry.sequence)}`,
          messageSequence: entry.sequence,
          status: failed ? "failed" : running ? "running" : "complete",
          reasoning,
          durationMs: effectiveInterval
            ? effectiveInterval[1] - effectiveInterval[0]
            : null,
          interval: effectiveInterval,
        });
      }
      for (const call of entry.message.toolCalls) {
        if (call.name === "request_user_input") continue;
        const result = resultByCall.get(call.id) ?? null;
        const callActivities = activityByCall.get(call.id) ?? [];
        const interval = result ? messageInterval(result) : null;
        const lastKind = callActivities.at(-1)?.value.kind;
        const failed =
          lastKind === "tool_failed" ||
          (result?.message.content.includes('"isError":true') ?? false);
        const waiting = !result && pendingWaitByCall.has(call.id);
        const activityStart = timestamp(
          callActivities.find((event) => event.value.kind === "model_tool_call")
            ?.createdAt,
        );
        const activityEnd = timestamp(
          [...callActivities]
            .reverse()
            .find(
              (event) =>
                event.value.kind === "tool_completed" ||
                event.value.kind === "tool_failed",
            )?.createdAt,
        );
        const liveInterval =
          !result && !waiting && activityStart !== null
            ? ([activityStart, activityEnd ?? nowMs] as const)
            : null;
        const effectiveInterval =
          interval ??
          (liveInterval && liveInterval[1] >= liveInterval[0]
            ? liveInterval
            : null);
        const item: ConversationToolWorkItem = {
          call,
          result,
          status: failed
            ? "failed"
            : result
              ? "complete"
              : waiting
                ? "waiting"
                : "running",
          durationMs: effectiveInterval
            ? effectiveInterval[1] - effectiveInterval[0]
            : null,
          progressCount: callActivities.filter(
            (event) => event.value.kind === "tool_progress",
          ).length,
          attemptCount: callActivities.reduce<number | null>(
            (maximum, event) =>
              event.value.attempt == null
                ? maximum
                : Math.max(maximum ?? 0, event.value.attempt),
            null,
          ),
          lastActivity: callActivities.at(-1)?.value.message ?? null,
          interval: effectiveInterval,
        };
        const key = toolCallFingerprint(call);
        const group = groups.get(key) ?? { key, name: call.name, calls: [] };
        group.calls.push(item);
        groups.set(key, group);
      }
    }
    turn.tools = [...groups.values()];
  }

  for (const [source, modelActivities] of activityBySource) {
    if (usedModelSources.has(source)) continue;
    const anchored = modelActivities.find(
      (event) => event.value.messageSequence !== null,
    );
    const terminal = [...modelActivities]
      .reverse()
      .find((event) => MODEL_TERMINAL.has(event.value.kind));
    const messageSequence = anchored?.value.messageSequence;
    const lastMessageSequence = messages.at(-1)?.sequence;
    const turn =
      anchored?.value.messageSequence == null
        ? undefined
        : (sequenceToTurn.get(anchored.value.messageSequence) ??
          (lastMessageSequence !== undefined &&
          messageSequence === lastMessageSequence + 1
            ? turns.at(-1)
            : undefined));
    if (!turn) {
      earlierActivity.push(...modelActivities);
      const reasoning = reasoningBySource.get(source);
      if (reasoning) {
        earlierActivity.push(reasoning);
        usedReasoning.add(source);
      }
      continue;
    }
    const start = timestamp(
      modelActivities.find((event) => event.value.kind === "model_started")
        ?.createdAt,
    );
    const end = timestamp(terminal?.createdAt);
    const effectiveEnd = end ?? nowMs;
    const interval =
      start === null || effectiveEnd < start
        ? null
        : ([start, effectiveEnd] as const);
    const reasoning = reasoningBySource.get(source) ?? null;
    if (reasoning) usedReasoning.add(source);
    turn.models.push({
      key: `model-${source}`,
      messageSequence: null,
      status:
        terminal?.value.kind === "model_failed"
          ? "failed"
          : terminal
            ? "complete"
            : "running",
      reasoning,
      durationMs: interval ? interval[1] - interval[0] : null,
      interval,
    });
  }

  for (const reasoning of input.reasoning ?? [])
    if (!usedReasoning.has(reasoning.source)) earlierActivity.push(reasoning);

  for (const turn of turns) {
    const callIds = new Set(
      turn.messages.flatMap((entry) =>
        entry.message.toolCalls.map((call) => call.id),
      ),
    );
    turn.messages = turn.messages.filter(
      (entry) =>
        entry.message.role !== "tool" &&
        !(
          entry.message.role === "assistant" &&
          !entry.message.content &&
          entry.message.toolCalls.length > 0
        ),
    );
    const dates = turn.messages
      .map((entry) => timestamp(entry.message.createdAt))
      .filter((value): value is number => value !== null);
    const start = dates[0] ?? null;
    const waits = (input.pendingWaits ?? []).filter(
      (wait) => !wait.callId || callIds.has(wait.callId),
    );
    const running =
      turn.models.some((model) => model.status === "running") ||
      turn.tools.some((group) =>
        group.calls.some(
          (call) => call.status === "running" || call.status === "waiting",
        ),
      ) ||
      waits.length > 0;
    const end = running ? nowMs : dates.length > 0 ? Math.max(...dates) : null;
    turn.durationMs =
      start === null || end === null || end < start ? null : end - start;
    turn.workDurationMs = unionDuration([
      ...turn.models.map((model) => model.interval),
      ...turn.tools.flatMap((group) =>
        group.calls.map((call) => call.interval),
      ),
    ]);
    const waitDurations = waits.map((wait) => {
      const waitStart = timestamp(wait.startedAt);
      return waitStart === null || nowMs < waitStart ? 0 : nowMs - waitStart;
    });
    const waitPairs = [
      ["tool_approval_requested", "tool_approval_resolved"],
      ["user_input_requested", "user_input_answered", "user_input_cancelled"],
      ["timer_started", "timer_resolved"],
      ["tool_recovery_required", "tool_recovery_resolved"],
    ] as const;
    for (const [startedKind, ...resolvedKinds] of waitPairs) {
      const starts = turn.activities.filter(
        (event) => event.value.kind === startedKind,
      );
      for (const started of starts) {
        const startedAt = timestamp(started.createdAt);
        const resolved = turn.activities.find(
          (event) =>
            resolvedKinds.includes(event.value.kind as never) &&
            timestamp(event.createdAt) !== null &&
            (timestamp(event.createdAt) ?? 0) >=
              (startedAt ?? Number.POSITIVE_INFINITY) &&
            (started.value.callId === null ||
              event.value.callId === started.value.callId),
        );
        const resolvedAt = timestamp(resolved?.createdAt);
        if (
          startedAt !== null &&
          resolvedAt !== null &&
          resolvedAt >= startedAt
        )
          waitDurations.push(resolvedAt - startedAt);
      }
    }
    turn.waitDurationMs =
      waitDurations.length === 0
        ? null
        : waitDurations.reduce((sum, value) => sum + value, 0);
  }

  return { turns, earlierActivity };
}

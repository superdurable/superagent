/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

export interface SequencedMessageLike<TMessage> {
  sequence: number;
  message: TMessage;
}

export function mergeSequencedMessages<TMessage>(
  current: readonly SequencedMessageLike<TMessage>[],
  incoming: readonly SequencedMessageLike<TMessage>[],
  reset = false,
): SequencedMessageLike<TMessage>[] {
  const bySequence = new Map(
    (reset ? [] : current).map((message) => [message.sequence, message]),
  );
  for (const message of incoming) bySequence.set(message.sequence, message);
  return [...bySequence.values()].sort(
    (left, right) => left.sequence - right.sequence,
  );
}

export interface ActivityEventLike {
  resumeToken: string;
}

export function mergeActivityEvent<TEvent extends ActivityEventLike>(
  current: readonly TEvent[],
  incoming: TEvent,
  maximum: number,
  shouldDisplay: boolean,
): TEvent[] {
  if (
    !shouldDisplay ||
    current.some((event) => event.resumeToken === incoming.resumeToken)
  ) {
    return [...current];
  }
  return [...current, incoming].slice(-maximum);
}

export interface LiveTextLike {
  source: string;
  createdAt: string;
  value: string;
  isComplete: boolean;
}

export function appendLiveText<TEntry extends LiveTextLike>(
  current: readonly TEntry[],
  incoming: TEntry,
): TEntry[] {
  const index = current.findIndex((entry) => entry.source === incoming.source);
  if (index < 0) return [...current, incoming];
  const next = [...current];
  const existing = next[index];
  if (existing === undefined) return [...current, incoming];
  next[index] = {
    ...existing,
    ...incoming,
    value: existing.isComplete
      ? incoming.value
      : `${existing.value}${incoming.value}`,
    isComplete: existing.isComplete || incoming.isComplete,
  };
  return next;
}

export function completeLiveText<TEntry extends LiveTextLike>(
  current: readonly TEntry[],
  source: string,
): TEntry[] {
  return current.map((entry) =>
    entry.source === source ? { ...entry, isComplete: true } : entry,
  );
}

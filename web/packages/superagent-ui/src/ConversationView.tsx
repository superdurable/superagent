/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { MarkdownContent } from "./MarkdownContent.js";
import {
  buildConversationPresentation,
  formatDuration,
  type ConversationPendingWait,
  type ConversationStreamState,
  type ConversationToolWorkItem,
  type ConversationTurn,
} from "./conversationPresentation.js";
import type {
  TimelineActivityEntry,
  TimelineConsumedUserEntry,
  TimelineLiveTextEntry,
  TimelineSequencedMessage,
} from "./conversationTimeline.js";

export interface ConversationViewProps {
  messages: readonly TimelineSequencedMessage[];
  consumedUserMessages?: readonly TimelineConsumedUserEntry[];
  reasoning?: readonly TimelineLiveTextEntry[];
  activities?: readonly TimelineActivityEntry[];
  assistant?: TimelineLiveTextEntry | null;
  pendingWaits?: readonly ConversationPendingWait[];
  isModelRunning?: boolean;
  streamState?: ConversationStreamState;
  renderMessage?: (entry: TimelineSequencedMessage) => ReactNode;
  renderToolCall?: (item: ConversationToolWorkItem) => ReactNode;
  className?: string;
}

function Duration({
  value,
  prefix,
}: {
  value: number | null;
  prefix?: string;
}) {
  const label = formatDuration(value);
  return label === null ? null : (
    <span aria-hidden="true" className="sa-duration">
      {prefix}
      {label}
    </span>
  );
}

function Status({ value }: { value: string }) {
  return (
    <span className={`sa-operation-status sa-operation-status--${value}`}>
      {value}
    </span>
  );
}

function DefaultMessage({ entry }: { entry: TimelineSequencedMessage }) {
  if (entry.message.role === "user")
    return <div className="sa-conversation-user">{entry.message.content}</div>;
  if (!entry.message.content) return null;
  return (
    <div className="sa-conversation-assistant">
      <MarkdownContent value={entry.message.content} />
    </div>
  );
}

function DefaultTool({ item }: { item: ConversationToolWorkItem }) {
  return (
    <details className="sa-work-item">
      <summary>
        <code>{item.call.name}</code>
        {item.attemptCount !== null && item.attemptCount > 1 ? (
          <span>{item.attemptCount} attempts</span>
        ) : null}
        <Status value={item.status} />
        <Duration value={item.durationMs} />
      </summary>
      <pre>{item.call.argumentsJson}</pre>
      {item.result ? <pre>{item.result.message.content}</pre> : null}
      {item.lastActivity ? <p>{item.lastActivity}</p> : null}
    </details>
  );
}

function ToolWorkItem({
  item,
  renderToolCall,
}: {
  item: ConversationToolWorkItem;
  renderToolCall?: (item: ConversationToolWorkItem) => ReactNode;
}) {
  if (renderToolCall === undefined) return <DefaultTool item={item} />;
  return (
    <div className="sa-work-item sa-work-tool-item">
      <div className="sa-work-item__heading">
        <code>{item.call.name}</code>
        {item.attemptCount !== null && item.attemptCount > 1 ? (
          <span>{item.attemptCount} attempts</span>
        ) : null}
        <Status value={item.status} />
        <Duration value={item.durationMs} />
      </div>
      {renderToolCall(item)}
      {item.lastActivity ? <p>{item.lastActivity}</p> : null}
    </div>
  );
}

function WorkLog({
  turn,
  renderToolCall,
}: {
  turn: ConversationTurn;
  renderToolCall?: (item: ConversationToolWorkItem) => ReactNode;
}) {
  const operationCount =
    turn.models.length +
    turn.tools.reduce((count, group) => count + group.calls.length, 0);
  if (operationCount === 0) return null;
  const failed =
    turn.models.some((item) => item.status === "failed") ||
    turn.tools.some((group) =>
      group.calls.some((item) => item.status === "failed"),
    );
  const running =
    turn.models.some((item) => item.status === "running") ||
    turn.tools.some((group) =>
      group.calls.some(
        (item) => item.status === "running" || item.status === "waiting",
      ),
    );
  return (
    <details className={`sa-work-log${failed ? " sa-work-log--failed" : ""}`}>
      <summary>
        {running ? (
          <span aria-hidden="true" className="sa-running-indicator" />
        ) : null}
        <strong>Work log</strong>
        <span>
          {operationCount} operation{operationCount === 1 ? "" : "s"}
        </span>
        {failed ? <Status value="failed" /> : null}
        <span className="sa-work-log__durations">
          <Duration value={turn.waitDurationMs} prefix="wait " />
          <Duration value={turn.workDurationMs} prefix="work " />
          <Duration value={turn.durationMs} prefix="turn " />
        </span>
      </summary>
      <div className="sa-work-log__body">
        {turn.models.map((model) => (
          <div className="sa-work-item" key={model.key}>
            <div className="sa-work-item__heading">
              <strong>Model</strong>
              <Status value={model.status} />
              <Duration value={model.durationMs} />
            </div>
            {model.reasoning?.value ? (
              <details open={!model.reasoning.isComplete}>
                <summary>
                  Reasoning ·{" "}
                  {model.reasoning.isComplete ? "complete" : "streaming"}
                </summary>
                <MarkdownContent value={model.reasoning.value} />
              </details>
            ) : null}
          </div>
        ))}
        {turn.tools.map((group) =>
          group.calls.length === 1 && group.calls[0] ? (
            <div key={group.key}>
              <ToolWorkItem
                item={group.calls[0]}
                renderToolCall={renderToolCall}
              />
              {group.calls[0].progressCount > 0 ? (
                <p className="sa-progress-count">
                  {group.calls[0].progressCount} progress events aggregated
                </p>
              ) : null}
            </div>
          ) : (
            <details className="sa-work-repeat" key={group.key}>
              <summary>
                <code>{group.name}</code>
                <span>Repeated ×{group.calls.length}</span>
              </summary>
              <div>
                {group.calls.map((item) => (
                  <div key={item.call.id}>
                    <ToolWorkItem item={item} renderToolCall={renderToolCall} />
                  </div>
                ))}
              </div>
            </details>
          ),
        )}
      </div>
    </details>
  );
}

export function ConversationView({
  messages,
  consumedUserMessages = [],
  reasoning = [],
  activities = [],
  assistant = null,
  pendingWaits = [],
  isModelRunning = false,
  streamState = "complete",
  renderMessage,
  renderToolCall,
  className = "sa-conversation-view",
}: ConversationViewProps) {
  const isLive =
    isModelRunning ||
    pendingWaits.length > 0 ||
    (assistant !== null && !assistant.isComplete) ||
    messages.some((entry) =>
      entry.message.toolCalls.some(
        (call) =>
          !messages.some(
            (candidate) => candidate.message.toolCallId === call.id,
          ),
      ),
    );
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (!isLive) return;
    const interval = window.setInterval(() => {
      setNowMs(Date.now());
    }, 1_000);
    return () => {
      window.clearInterval(interval);
    };
  }, [isLive]);
  const presentation = useMemo(
    () =>
      buildConversationPresentation(
        {
          messages,
          consumedUserMessages,
          reasoning,
          activities,
          assistant,
          pendingWaits,
          isModelRunning,
        },
        nowMs,
      ),
    [
      activities,
      assistant,
      consumedUserMessages,
      isModelRunning,
      messages,
      nowMs,
      pendingWaits,
      reasoning,
    ],
  );

  return (
    <section className={className} aria-label="Conversation">
      {presentation.earlierActivity.length > 0 ? (
        <details className="sa-earlier-activity">
          <summary>
            Earlier activity · {presentation.earlierActivity.length} recovered
            events
          </summary>
          <ol>
            {presentation.earlierActivity.map((entry, index) => (
              <li
                key={
                  ("resumeToken" in entry ? entry.resumeToken : entry.source) +
                  String(index)
                }
              >
                {typeof entry.value === "string"
                  ? entry.value
                  : entry.value.message}
              </li>
            ))}
          </ol>
        </details>
      ) : null}
      {presentation.turns.map((turn) => (
        <section
          className="sa-conversation-turn"
          aria-label="Conversation turn"
          key={turn.key}
        >
          {turn.consumedUserMessages.map((entry) => (
            <div className="sa-conversation-user" key={entry.messageId}>
              {entry.value.content}
            </div>
          ))}
          {turn.messages.map((entry) => (
            <div key={entry.sequence}>
              {renderMessage?.(entry) ?? <DefaultMessage entry={entry} />}
            </div>
          ))}
          <WorkLog turn={turn} renderToolCall={renderToolCall} />
        </section>
      ))}
      {assistant?.value ? (
        <div className="sa-live-assistant">
          <span>Assistant · {streamState}</span>
          <MarkdownContent value={assistant.value} />
        </div>
      ) : null}
      {streamState === "disconnected" ? (
        <div className="sa-stream-status" role="status">
          Live agent updates disconnected
        </div>
      ) : null}
    </section>
  );
}

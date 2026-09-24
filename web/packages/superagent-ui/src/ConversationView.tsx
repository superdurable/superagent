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
  type ConversationQuestionItem,
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
  isExecutionLive?: boolean;
  streamState?: ConversationStreamState;
  renderMessage?: (entry: TimelineSequencedMessage) => ReactNode;
  renderToolCall?: (item: ConversationToolWorkItem) => ReactNode;
  renderQuestion?: (item: ConversationQuestionItem) => ReactNode;
  className?: string;
}

function DefaultQuestion({ item }: { item: ConversationQuestionItem }) {
  return (
    <article className="sa-question-card" data-status={item.status}>
      <div className="sa-question-card__heading">
        <strong>Assistant requested input</strong>
        <Status value={item.status} />
      </div>
      {item.isMalformed
        ? null
        : item.questions.map((question) => (
            <section key={question.id}>
              <strong>{question.header}</strong>
              <p>{question.question}</p>
              {question.options.length > 0 ? (
                <ul>
                  {question.options.map((option) => (
                    <li key={option.label}>
                      <strong>{option.label}</strong>
                      {option.description ? ` — ${option.description}` : ""}
                    </li>
                  ))}
                </ul>
              ) : null}
            </section>
          ))}
    </article>
  );
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

function messageTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat(undefined, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      }).format(date);
}

function MessageFrame({
  messageRole,
  createdAt,
  children,
}: {
  messageRole: "user" | "assistant";
  createdAt: string;
  children: ReactNode;
}) {
  return (
    <article
      aria-label={`${messageRole === "user" ? "User" : "Assistant"} message`}
      className={`sa-conversation-message sa-conversation-message--${messageRole} message-bubble ${messageRole}`}
    >
      <div className="sa-conversation-message__content">{children}</div>
      <time className="sa-message-timestamp" dateTime={createdAt}>
        {messageTime(createdAt)}
      </time>
    </article>
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
  const [isOpen, setIsOpen] = useState(false);
  const operationCount = turn.operations.reduce(
    (count, operation) =>
      count + (operation.kind === "model" ? 1 : operation.group.calls.length),
    0,
  );
  if (operationCount === 0) return null;
  const statuses = [
    ...turn.models.map((item) => item.status),
    ...turn.tools.flatMap((group) => group.calls.map((item) => item.status)),
  ];
  const succeededCount = statuses.filter(
    (status) => status === "complete",
  ).length;
  const failedCount = statuses.filter((status) => status === "failed").length;
  const activeCount = statuses.filter(
    (status) => status === "running" || status === "waiting",
  ).length;
  const unknownCount = statuses.filter((status) => status === "unknown").length;
  const failed = failedCount > 0;
  const running = activeCount > 0;
  return (
    <details
      className={`sa-work-log${failed ? " sa-work-log--failed" : ""}`}
      onToggle={(event) => {
        setIsOpen(event.currentTarget.open);
      }}
    >
      <summary>
        {running ? (
          <span aria-hidden="true" className="sa-running-indicator" />
        ) : null}
        <strong>Work log</strong>
        <span>
          {operationCount} operation{operationCount === 1 ? "" : "s"}
        </span>
        <span className="sa-work-log__summary-count">
          {succeededCount} succeeded
        </span>
        <span
          className={`sa-work-log__summary-count${failed ? " sa-work-log__summary-count--failed" : ""}`}
        >
          {failedCount} failed
        </span>
        {activeCount > 0 ? (
          <span className="sa-work-log__summary-count">
            {activeCount} active
          </span>
        ) : null}
        {unknownCount > 0 ? (
          <span className="sa-work-log__summary-count">
            {unknownCount} unknown
          </span>
        ) : null}
        <span className="sa-work-log__durations">
          <Duration value={turn.waitDurationMs} prefix="wait " />
          <Duration value={turn.workDurationMs} prefix="work " />
          <Duration value={turn.durationMs} prefix="turn " />
        </span>
      </summary>
      {isOpen ? (
        <div className="sa-work-log__body">
          {turn.operations.map((operation) => {
            if (operation.kind === "model") {
              const model = operation.item;
              return (
                <div
                  className="sa-work-item sa-model-work-item"
                  key={operation.key}
                >
                  <div className="sa-work-item__heading">
                    <strong>{model.summary}</strong>
                    {model.attemptCount !== null && model.attemptCount > 1 ? (
                      <span>{model.attemptCount} attempts</span>
                    ) : null}
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
              );
            }
            const group = operation.group;
            const repeatLabel =
              group.repeatIndex !== null && group.repeatTotal !== null
                ? `Repeat ${String(group.repeatIndex)}/${String(group.repeatTotal)}`
                : null;
            return group.calls.length === 1 && group.calls[0] ? (
              <div key={operation.key}>
                {repeatLabel ? (
                  <span className="sa-repeat-label">{repeatLabel}</span>
                ) : null}
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
              <details className="sa-work-repeat" key={operation.key}>
                <summary>
                  <code>{group.name}</code>
                  <span>Repeated ×{group.calls.length}</span>
                </summary>
                <div>
                  {group.calls.map((item) => (
                    <div key={item.call.id}>
                      <ToolWorkItem
                        item={item}
                        renderToolCall={renderToolCall}
                      />
                    </div>
                  ))}
                </div>
              </details>
            );
          })}
        </div>
      ) : null}
    </details>
  );
}

function HistoricalActivity({
  entries,
}: {
  entries: readonly (TimelineActivityEntry | TimelineLiveTextEntry)[];
}) {
  if (entries.length === 0) return null;
  return (
    <details className="sa-earlier-activity">
      <summary>
        Historical activity · {entries.length} event
        {entries.length === 1 ? "" : "s"}
      </summary>
      <ol>
        {entries.map((entry, index) => (
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
  isExecutionLive = true,
  streamState = "complete",
  renderMessage,
  renderToolCall,
  renderQuestion,
  className = "sa-conversation-view",
}: ConversationViewProps) {
  const isLive =
    isExecutionLive &&
    (isModelRunning ||
      pendingWaits.length > 0 ||
      (assistant !== null && !assistant.isComplete) ||
      messages.some((entry) =>
        entry.message.toolCalls.some(
          (call) =>
            !messages.some(
              (candidate) => candidate.message.toolCallId === call.id,
            ),
        ),
      ));
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
          isExecutionLive,
        },
        nowMs,
      ),
    [
      activities,
      assistant,
      consumedUserMessages,
      isModelRunning,
      isExecutionLive,
      messages,
      nowMs,
      pendingWaits,
      reasoning,
    ],
  );

  return (
    <section className={className} aria-label="Conversation">
      <HistoricalActivity entries={presentation.earlierActivity} />
      {presentation.turns.map((turn) => (
        <section
          className="sa-conversation-turn"
          aria-label="Conversation turn"
          key={turn.key}
        >
          {turn.messages.map((entry) => {
            if (
              entry.message.role !== "user" &&
              entry.message.role !== "assistant"
            )
              return null;
            const rendered = renderMessage?.(entry) ?? (
              <DefaultMessage entry={entry} />
            );
            return (
              <MessageFrame
                messageRole={entry.message.role}
                createdAt={entry.message.createdAt}
                key={entry.sequence}
              >
                {rendered}
              </MessageFrame>
            );
          })}
          {turn.consumedUserMessages.map((entry) => (
            <MessageFrame
              messageRole="user"
              createdAt={entry.createdAt}
              key={entry.messageId}
            >
              <div className="sa-conversation-user">{entry.value.content}</div>
            </MessageFrame>
          ))}
          <HistoricalActivity entries={turn.historicalActivities} />
          <WorkLog turn={turn} renderToolCall={renderToolCall} />
          {turn.questions.map((question) => (
            <div key={question.key}>
              {renderQuestion?.(question) ?? (
                <DefaultQuestion item={question} />
              )}
            </div>
          ))}
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
          Live updates interrupted — reconnecting
        </div>
      ) : null}
    </section>
  );
}

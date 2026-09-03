/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import type { KeyboardEvent } from "react";

import {
  PlanStatus,
  TaskStatus,
  type CallId,
  type FlowId,
  type MessageRole,
  type PendingUserMessage,
} from "./api/generated";
import {
  pendingQueueMessageID,
  type ConnectionState,
  type QueueCommandAction,
  type ReadyConversationState,
} from "./conversation-state";

interface ConversationViewProps {
  flowId: FlowId;
  state: ReadyConversationState;
  onRetrySnapshot: () => void;
  onLoadOlder: (beforeSequence: number) => void;
  onComposerChange: (value: string) => void;
  onPlanModeChange: (value: boolean) => void;
  onSubmit: () => void;
  onExecutePlan: (revision: number) => void;
  onApproveTool: (callId: CallId, approved: boolean) => void;
  onMutateQueue: (
    message: PendingUserMessage,
    action: QueueCommandAction,
  ) => void;
  onStartAnother: () => void;
}

export function ConversationView({
  flowId,
  state,
  onRetrySnapshot,
  onLoadOlder,
  onComposerChange,
  onPlanModeChange,
  onSubmit,
  onExecutePlan,
  onApproveTool,
  onMutateQueue,
  onStartAnother,
}: ConversationViewProps) {
  const { snapshot } = state;
  const description = snapshot.description;
  const isTerminal = state.connection === "terminal";
  const isBusy = state.pendingCommand !== null || isTerminal;
  const pendingMessageID = pendingQueueMessageID(state);
  const handleComposerKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (
      event.key === "Enter" &&
      (event.metaKey || event.ctrlKey || event.altKey)
    ) {
      event.preventDefault();
      onSubmit();
    }
  };

  return (
    <main className="conversation-shell">
      <header className="conversation-header">
        <div>
          <p className="eyebrow">Durable AI runtime</p>
          <h1>Superagent</h1>
          <p className="flow-identity">
            Flow <code>{flowId}</code> · Run <code>{snapshot.runId}</code>
          </p>
        </div>
        <div className="status-stack">
          <span className={`connection-pill ${state.connection}`}>
            {connectionLabel(state.connection)}
          </span>
          <strong>{statusLabel(description.status)}</strong>
          <small>{description.model}</small>
        </div>
      </header>

      {state.error !== null && (
        <div className="error conversation-error" role="alert">
          <span>{state.error}</span>
          {!isTerminal && (
            <button
              type="button"
              className="text-button"
              onClick={onRetrySnapshot}
            >
              Reconcile now
            </button>
          )}
        </div>
      )}

      <section className="conversation-grid">
        <div className="conversation-main">
          <section className="messages-card" aria-label="Conversation history">
            {snapshot.history.nextBeforeSequence !== null && (
              <button
                type="button"
                className="secondary load-older"
                disabled={state.historyRequest !== null}
                onClick={() => {
                  const beforeSequence = snapshot.history.nextBeforeSequence;
                  if (beforeSequence !== null) onLoadOlder(beforeSequence);
                }}
              >
                {state.historyRequest === null
                  ? "Load older messages"
                  : "Loading history…"}
              </button>
            )}
            {snapshot.history.messages.length === 0 && (
              <div className="empty-state">
                <h2>Start the conversation</h2>
                <p>Your messages and durable Agent replies will appear here.</p>
              </div>
            )}
            {snapshot.history.messages.map(({ sequence, message }) => (
              <article
                className={`message-bubble ${message.role}`}
                key={sequence}
              >
                <div className="message-meta">
                  <strong>{messageRoleLabel(message.role)}</strong>
                  <time dateTime={message.createdAt}>
                    {formatTime(message.createdAt)}
                  </time>
                </div>
                {message.content !== "" && <p>{message.content}</p>}
                {message.toolCalls.map((call) => (
                  <details key={call.id} className="tool-call">
                    <summary>Tool request · {call.name}</summary>
                    <pre>{call.argumentsJson}</pre>
                  </details>
                ))}
              </article>
            ))}
            {state.reasoningText !== "" && (
              <details className="reasoning-card" open>
                <summary>Reasoning summary</summary>
                <p>{state.reasoningText}</p>
              </details>
            )}
            {state.assistantText !== "" && (
              <article className="message-bubble assistant live-message">
                <div className="message-meta">
                  <strong>Assistant</strong>
                  <span>Streaming</span>
                </div>
                <p>{state.assistantText}</p>
              </article>
            )}
          </section>

          {description.plan !== null && (
            <section className="plan-card">
              <div className="section-heading">
                <div>
                  <p className="eyebrow">
                    Plan revision {description.plan.revision}
                  </p>
                  <h2>{statusLabel(description.plan.status)}</h2>
                </div>
                {description.plan.status !== PlanStatus.COMPLETED && (
                  <button
                    type="button"
                    disabled={isBusy || description.isPlanExecutionRequested}
                    onClick={() => {
                      const revision = description.plan?.revision;
                      if (revision !== undefined) onExecutePlan(revision);
                    }}
                  >
                    {description.isPlanExecutionRequested
                      ? "Execution requested"
                      : description.plan.status === PlanStatus.DRAFT
                        ? "Execute plan"
                        : "Continue plan"}
                  </button>
                )}
              </div>
              <ol className="plan-tasks">
                {description.plan.tasks.map((task, index) => (
                  <li
                    className={task.status}
                    key={`${String(index)}:${task.content}`}
                  >
                    <span aria-hidden="true">{taskIcon(task.status)}</span>
                    <div>
                      <strong>{statusLabel(task.status)}</strong>
                      <p>{task.content}</p>
                    </div>
                  </li>
                ))}
              </ol>
            </section>
          )}
        </div>

        <aside className="conversation-sidebar">
          {description.pendingApproval !== null && (
            <section className="side-card approval-card">
              <p className="eyebrow">Approval required</p>
              <h2>{description.pendingApproval.toolName}</h2>
              <pre>{description.pendingApproval.argumentsJson}</pre>
              <div className="button-row">
                <button
                  type="button"
                  disabled={isBusy}
                  onClick={() => {
                    const callId = description.pendingApproval?.callId;
                    if (callId !== undefined) onApproveTool(callId, true);
                  }}
                >
                  Approve
                </button>
                <button
                  type="button"
                  className="danger-button"
                  disabled={isBusy}
                  onClick={() => {
                    const callId = description.pendingApproval?.callId;
                    if (callId !== undefined) onApproveTool(callId, false);
                  }}
                >
                  Reject
                </button>
              </div>
            </section>
          )}

          {description.pendingTimer !== null && (
            <section className="side-card timer-card">
              <p className="eyebrow">Durable timer</p>
              <h2>{description.pendingTimer.durationSeconds}s</h2>
              <p>{description.pendingTimer.reason}</p>
              <small>Steering interrupts this wait at a safe boundary.</small>
            </section>
          )}

          {(snapshot.queued.length > 0 || snapshot.steered.length > 0) && (
            <section className="side-card queue-card">
              <div className="section-heading compact">
                <div>
                  <p className="eyebrow">Message queue</p>
                  <h2>
                    {String(snapshot.queued.length)} queued ·{" "}
                    {String(snapshot.steered.length)} steering
                  </h2>
                </div>
              </div>
              {snapshot.steered.map((message) => (
                <div className="queue-message steered" key={message.messageId}>
                  <strong>Steering</strong>
                  <p>{message.value.content}</p>
                </div>
              ))}
              {snapshot.queued.map((message) => (
                <div className="queue-message" key={message.messageId}>
                  <strong>{message.value.planMode ? "Plan" : "Queued"}</strong>
                  <p>{message.value.content}</p>
                  <div className="queue-actions">
                    {(["steer", "edit", "delete"] as const).map((action) => (
                      <button
                        type="button"
                        className="text-button"
                        disabled={isBusy}
                        key={action}
                        onClick={() => {
                          onMutateQueue(message, action);
                        }}
                      >
                        {pendingMessageID === message.messageId
                          ? "Updating…"
                          : statusLabel(action)}
                      </button>
                    ))}
                  </div>
                </div>
              ))}
            </section>
          )}

          {state.activities.length > 0 && (
            <section className="side-card activity-card">
              <p className="eyebrow">Live activity</p>
              <ul>
                {state.activities.slice(0, 8).map((activity, index) => (
                  <li key={`${activity.kind}:${String(index)}`}>
                    <strong>{statusLabel(activity.kind)}</strong>
                    <span>{activity.message}</span>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </aside>
      </section>

      <section className="composer-card" aria-label="Message composer">
        {description.pendingUserInput !== null && (
          <div className="pending-input">
            <p className="eyebrow">Agent needs your input</p>
            <strong>{description.pendingUserInput.prompt}</strong>
            {description.pendingUserInput.choices.length > 0 && (
              <div className="choice-row">
                {description.pendingUserInput.choices.map((choice) => (
                  <button
                    type="button"
                    className="secondary"
                    disabled={isBusy}
                    key={choice}
                    onClick={() => {
                      onComposerChange(choice);
                    }}
                  >
                    {choice}
                  </button>
                ))}
              </div>
            )}
          </div>
        )}
        {description.pendingUserInput === null && (
          <label className="plan-mode">
            <input
              type="checkbox"
              checked={state.isPlanMode}
              disabled={isBusy}
              onChange={(event) => {
                onPlanModeChange(event.target.checked);
              }}
            />
            Plan mode
          </label>
        )}
        <div className="composer-row">
          <textarea
            aria-label={
              description.pendingUserInput === null
                ? "Message"
                : "Answer Agent question"
            }
            value={state.composer}
            disabled={isTerminal}
            placeholder={
              isTerminal
                ? "This Flow is no longer active."
                : description.pendingUserInput === null
                  ? state.isPlanMode
                    ? "Describe what you want the Agent to plan…"
                    : "Message the Agent…"
                  : "Type your answer…"
            }
            rows={3}
            onChange={(event) => {
              onComposerChange(event.target.value);
            }}
            onKeyDown={handleComposerKeyDown}
          />
          <button
            type="button"
            disabled={isBusy || state.composer.trim() === ""}
            onClick={onSubmit}
          >
            {state.pendingCommand?.command.kind === "send"
              ? "Sending…"
              : description.pendingUserInput === null
                ? state.isPlanMode
                  ? "Create plan"
                  : "Send"
                : "Submit answer"}
          </button>
        </div>
        <div className="composer-footer">
          <small>⌘/Ctrl/Alt + Enter sends · Enter adds a new line</small>
          <button
            type="button"
            className="text-button"
            onClick={onStartAnother}
          >
            Start another agent
          </button>
        </div>
      </section>
    </main>
  );
}

function connectionLabel(connection: ConnectionState): string {
  switch (connection) {
    case "live":
      return "Live";
    case "reconnecting":
      return "Reconnecting";
    case "stale":
      return "Stale";
    case "terminal":
      return "Terminal";
  }
}

function statusLabel(value: string): string {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function messageRoleLabel(role: MessageRole): string {
  return role === "tool" ? "Tool result" : statusLabel(role);
}

function taskIcon(status: TaskStatus): string {
  switch (status) {
    case TaskStatus.COMPLETED:
      return "✓";
    case TaskStatus.IN_PROGRESS:
      return "●";
    case TaskStatus.PENDING:
      return "○";
  }
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat(undefined, {
        hour: "numeric",
        minute: "2-digit",
      }).format(date);
}

/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  lazy,
  Suspense,
  useEffect,
  useLayoutEffect,
  useRef,
  type KeyboardEvent,
  type SyntheticEvent,
} from "react";

import {
  MessageRole,
  PlanStatus,
  TaskStatus,
  type CallId,
  type FlowId,
  type PendingUserMessage,
  type ToolName,
} from "./api/generated";
import {
  pendingQueueMessageID,
  type ConnectionState,
  type QueueCommandAction,
  type ActiveConversationState,
} from "./conversation-state";
import { buildConversationTimeline } from "./conversation-timeline";

const MarkdownContent = lazy(() => import("./MarkdownContent"));

interface ConversationViewProps {
  flowId: FlowId;
  builtInTools: readonly ToolName[];
  state: ActiveConversationState;
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
  builtInTools,
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
  const { shellRef, composerRef } = useComposerClearance();
  const { snapshot } = state;
  const description = snapshot.description;
  const isBusy = state.pendingCommand !== null;
  const pendingMessageID = pendingQueueMessageID(state);
  const builtInToolNames = new Set(builtInTools);
  const timeline = buildConversationTimeline(
    snapshot.history.messages,
    state.reasoning,
    state.activities,
  );
  const liveContentVersion =
    state.activities.length +
    (state.assistant?.value.length ?? 0) +
    state.reasoning.reduce((total, entry) => total + entry.value.length, 0);
  useAutoScroll(description.lastSequence, liveContentVersion);
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
    <main className="conversation-shell" ref={shellRef}>
      <header className="conversation-header">
        <div>
          <p className="eyebrow">Durable AI runtime</p>
          <h1>SuperAgent</h1>
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
          <button
            type="button"
            className="text-button"
            onClick={onRetrySnapshot}
          >
            Reconcile now
          </button>
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
            {timeline.map((entry) => {
              if (entry.kind === "reasoning") {
                return (
                  <details
                    className="reasoning-card"
                    key={`reasoning:${entry.value.source}`}
                    open={!entry.value.isComplete}
                    onToggle={revealOpenedDetails}
                  >
                    <summary>
                      Reasoning summary · {formatTime(entry.value.createdAt)} ·{" "}
                      {entry.value.isComplete ? "Complete" : "Streaming"}
                    </summary>
                    <RichText value={entry.value.value} />
                  </details>
                );
              }
              const { sequence, message } = entry.value;
              const visibleToolCalls = message.toolCalls.filter(
                (call) => !builtInToolNames.has(call.name),
              );
              if (
                (message.role === MessageRole.TOOL &&
                  message.toolName !== null &&
                  builtInToolNames.has(message.toolName)) ||
                (message.content === "" && visibleToolCalls.length === 0)
              ) {
                return null;
              }
              return (
                <article
                  className={`message-bubble ${message.role}`}
                  key={`message:${String(sequence)}`}
                >
                  <div className="message-meta">
                    <strong>{messageRoleLabel(message.role)}</strong>
                    <time dateTime={message.createdAt}>
                      {formatTime(message.createdAt)}
                    </time>
                  </div>
                  {message.content !== "" &&
                    (message.role === MessageRole.ASSISTANT ? (
                      <RichText value={message.content} />
                    ) : (
                      <p>{message.content}</p>
                    ))}
                  {visibleToolCalls.map((call) => (
                    <details
                      key={call.id}
                      className="tool-call"
                      onToggle={revealOpenedDetails}
                    >
                      <summary>Tool request · {call.name}</summary>
                      <pre>{call.argumentsJson}</pre>
                    </details>
                  ))}
                </article>
              );
            })}
            {state.assistant !== null && (
              <article className="message-bubble assistant live-message">
                <div className="message-meta">
                  <strong>Assistant</strong>
                  <span>
                    {formatTime(state.assistant.createdAt)} ·{" "}
                    {state.assistant.isComplete ? "Finalizing" : "Streaming"}
                  </span>
                </div>
                <RichText value={state.assistant.value} />
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

          {(snapshot.queued.length > 0 ||
            snapshot.steered.length > 0 ||
            state.optimisticSubmission !== null) && (
            <section className="side-card queue-card">
              <div className="section-heading compact">
                <div>
                  <p className="eyebrow">Message queue</p>
                  <h2>
                    {String(
                      snapshot.queued.length +
                        (state.optimisticSubmission === null ? 0 : 1),
                    )}{" "}
                    queued · {String(snapshot.steered.length)} steering
                  </h2>
                </div>
              </div>
              {snapshot.steered.map((message) => (
                <div className="queue-message steered" key={message.messageId}>
                  <strong>Steering</strong>
                  <p>{message.value.content}</p>
                </div>
              ))}
              {state.optimisticSubmission !== null && (
                <div
                  className="queue-message submitting"
                  key={state.optimisticSubmission.localID}
                >
                  <strong>Submitting…</strong>
                  <p>{state.optimisticSubmission.value.content}</p>
                </div>
              )}
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
                {state.activities.slice(0, 8).map((entry) => (
                  <li key={entry.resumeToken}>
                    <strong>{statusLabel(entry.value.kind)}</strong>
                    <span>{entry.value.message}</span>
                    <time dateTime={entry.createdAt}>
                      {formatTime(entry.createdAt)}
                    </time>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </aside>
      </section>

      <section
        className="composer-card"
        aria-label="Message composer"
        ref={composerRef}
      >
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
            disabled={isBusy}
            placeholder={
              description.pendingUserInput === null
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

function useComposerClearance(): {
  shellRef: React.RefObject<HTMLElement>;
  composerRef: React.RefObject<HTMLElement>;
} {
  const shellRef = useRef<HTMLElement>(null);
  const composerRef = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const shell = shellRef.current;
    const composer = composerRef.current;
    if (shell === null || composer === null) return;
    const update = () => {
      shell.style.setProperty(
        "--composer-height",
        `${String(Math.ceil(composer.getBoundingClientRect().height))}px`,
      );
    };
    update();
    if (typeof ResizeObserver === "undefined") {
      return () => {
        shell.style.removeProperty("--composer-height");
      };
    }
    const observer = new ResizeObserver(update);
    observer.observe(composer);
    return () => {
      observer.disconnect();
      shell.style.removeProperty("--composer-height");
    };
  }, []);
  return { shellRef, composerRef };
}

function useAutoScroll(lastSequence: number, liveContentVersion: number) {
  const shouldStickToBottom = useRef(true);
  const scrollToBottom = () => {
    window.scrollTo({
      top: document.documentElement.scrollHeight,
      behavior: "auto",
    });
  };
  useEffect(() => {
    const update = () => {
      const distance =
        document.documentElement.scrollHeight -
        window.scrollY -
        window.innerHeight;
      shouldStickToBottom.current = distance <= 160;
    };
    const keepBottomVisible = () => {
      if (!shouldStickToBottom.current) return;
      window.requestAnimationFrame(scrollToBottom);
    };
    window.addEventListener("scroll", update, { passive: true });
    window.addEventListener("resize", keepBottomVisible);
    update();
    return () => {
      window.removeEventListener("scroll", update);
      window.removeEventListener("resize", keepBottomVisible);
    };
  }, []);
  useLayoutEffect(() => {
    if (!shouldStickToBottom.current) return;
    scrollToBottom();
  }, [lastSequence, liveContentVersion]);
}

function revealOpenedDetails(event: SyntheticEvent<HTMLDetailsElement>) {
  const details = event.currentTarget;
  if (!details.open) return;
  window.requestAnimationFrame(() => {
    details.scrollIntoView({ block: "nearest" });
  });
}

function RichText({ value }: { value: string }) {
  return (
    <Suspense fallback={<p className="markdown-fallback">{value}</p>}>
      <MarkdownContent value={value} />
    </Suspense>
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

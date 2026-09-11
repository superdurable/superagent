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
  useState,
  type SyntheticEvent,
} from "react";
import {
  ConversationComposer,
  PendingMessageQueue,
  PendingQuestionBatch,
  type PendingMessageQueueItem,
  type PendingQuestion,
  type PendingQuestionAnswer,
} from "@superdurable/superagent-ui";

import {
  AgentInteractionStatus,
  AgentStatus,
  EventKind,
  MessageRole,
  PlanStatus,
  TaskStatus,
  type AgentEvent,
  type CallId,
  type FlowId,
  type PendingUserMessage,
  type PendingUserInput,
  type ToolName,
  type UserInputAnswer,
} from "./api/generated";
import {
  displayedPlanTaskStatus,
  pendingQueueMessageID,
  type ConnectionState,
  type QueueCommandAction,
  type ActiveConversationState,
} from "./conversation-state";
import { buildConversationTimeline } from "./conversation-timeline";
import { useTimelineFollow } from "./useTimelineFollow";

const MarkdownContent = lazy(async () => {
  const module = await import("@superdurable/superagent-ui");
  return { default: module.MarkdownContent };
});

interface ConversationViewProps {
  flowId: FlowId;
  builtInTools: readonly ToolName[];
  state: ActiveConversationState;
  onRetrySnapshot: () => void;
  onLoadOlder: (beforeSequence: number) => void;
  onComposerChange: (value: string) => void;
  onPlanModeChange: (value: boolean) => void;
  onSubmit: () => void;
  onSubmitAnswers: (callId: CallId, answers: UserInputAnswer[]) => void;
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
  onSubmitAnswers,
  onExecutePlan,
  onApproveTool,
  onMutateQueue,
  onStartAnother,
}: ConversationViewProps) {
  const { shellRef, composerRef } = useComposerClearance();
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const shouldFocusAfterQueueMutation = useRef(false);
  const { snapshot } = state;
  const description = snapshot.description;
  const isBusy = state.pendingCommand !== null;
  const areMutationsDisabled = isBusy || state.reconciliation !== "open";
  const pendingMessageID = pendingQueueMessageID(state);
  const builtInToolNames = new Set(builtInTools);
  const hasSidebar =
    description.pendingApproval !== null ||
    description.pendingTimer !== null ||
    description.plan !== null;
  const timeline = buildConversationTimeline(
    snapshot.history.messages,
    state.reasoning,
    state.activities,
    state.assistant,
  );
  const liveContentVersion = [
    String(state.activities.length),
    state.activities.at(-1)?.resumeToken ?? "",
    state.assistant === null
      ? ""
      : `${state.assistant.source}:${String(state.assistant.value.length)}:${String(state.assistant.isComplete)}`,
    ...state.reasoning.map(
      (entry) =>
        `${entry.source}:${String(entry.value.length)}:${String(entry.isComplete)}`,
    ),
  ].join("|");
  const { hasUnseenContent, jumpToLatest } = useTimelineFollow({
    flowRunKey: `${flowId}:${snapshot.runId}`,
    contentVersion: `${String(description.lastSequence)}:${liveContentVersion}`,
  });
  useArchiveScroll(
    snapshot.history.nextBeforeSequence,
    state.historyRequest !== null,
    snapshot.history.messages.length,
    onLoadOlder,
  );
  useEffect(() => {
    const command = state.pendingCommand?.command;
    if (command?.kind === "queue") {
      shouldFocusAfterQueueMutation.current = true;
      return;
    }
    if (
      state.pendingCommand === null &&
      shouldFocusAfterQueueMutation.current
    ) {
      shouldFocusAfterQueueMutation.current = false;
      textareaRef.current?.focus();
    }
  }, [state.pendingCommand]);
  const queueItems = mapPendingMessageQueueItems(state);
  const submitFromComposer = () => {
    onSubmit();
    textareaRef.current?.focus();
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
      </header>

      {hasUnseenContent && (
        <button
          type="button"
          className="jump-to-latest"
          aria-label="Jump to latest message"
          onClick={jumpToLatest}
        >
          <span aria-hidden="true">…</span>
        </button>
      )}

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

      {state.reconciliation === "syncing" && (
        <div className="sync-status" role="status">
          Syncing durable state…
        </div>
      )}

      <section
        className={`conversation-grid${hasSidebar ? "" : " no-sidebar"}`}
      >
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
            {timeline.length === 0 && (
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
                      Reasoning summary ·{" "}
                      <time dateTime={entry.value.createdAt}>
                        {formatTime(entry.value.createdAt)}
                      </time>{" "}
                      · {entry.value.isComplete ? "Complete" : "Streaming"}
                    </summary>
                    <RichText value={entry.value.value} />
                  </details>
                );
              }
              if (entry.kind === "activity") {
                return (
                  <article
                    className={`activity-entry ${entry.value.value.kind}`}
                    key={`activity:${entry.value.resumeToken}`}
                  >
                    <span className="activity-icon" aria-hidden="true">
                      {activityIcon(entry.value.value.kind)}
                    </span>
                    <div>
                      <strong>{activityLabel(entry.value.value)}</strong>
                      <span>{entry.value.value.message}</span>
                    </div>
                    <time dateTime={entry.value.createdAt}>
                      {formatTime(entry.value.createdAt)}
                    </time>
                  </article>
                );
              }
              if (entry.kind === "assistant") {
                return (
                  <article
                    className="message-bubble assistant live-message"
                    key={`assistant:${entry.value.source}`}
                  >
                    <div className="message-meta">
                      <strong>Assistant</strong>
                      <span>
                        {formatTime(entry.value.createdAt)} ·{" "}
                        {entry.value.isComplete ? "Finalizing" : "Streaming"}
                      </span>
                    </div>
                    <RichText value={entry.value.value} />
                  </article>
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
          </section>
        </div>

        {hasSidebar && (
          <aside className="conversation-sidebar">
            {description.pendingApproval !== null && (
              <section className="side-card approval-card">
                <p className="eyebrow">Approval required</p>
                <h2>{description.pendingApproval.toolName}</h2>
                <pre>{description.pendingApproval.argumentsJson}</pre>
                <div className="button-row">
                  <button
                    type="button"
                    disabled={areMutationsDisabled}
                    onClick={() => {
                      const callId = description.pendingApproval?.callId;
                      if (callId !== undefined) onApproveTool(callId, true);
                    }}
                  >
                    {state.pendingCommand?.command.kind === "approve"
                      ? "Processing…"
                      : "Approve"}
                  </button>
                  <button
                    type="button"
                    className="danger-button"
                    disabled={areMutationsDisabled}
                    onClick={() => {
                      const callId = description.pendingApproval?.callId;
                      if (callId !== undefined) onApproveTool(callId, false);
                    }}
                  >
                    {state.pendingCommand?.command.kind === "approve"
                      ? "Processing…"
                      : "Reject"}
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

            {description.plan !== null && (
              <PlanPanel
                state={state}
                areMutationsDisabled={areMutationsDisabled}
                onExecutePlan={onExecutePlan}
              />
            )}
          </aside>
        )}
      </section>

      <section
        className="composer-card"
        aria-label="Message composer"
        ref={composerRef}
      >
        <PendingMessageQueue
          disabled={areMutationsDisabled}
          items={queueItems}
          pendingItemID={pendingMessageID}
          onAction={(itemID, action) => {
            const message = state.snapshot.queued.find(
              (candidate) => candidate.messageId === itemID,
            );
            if (message !== undefined) onMutateQueue(message, action);
          }}
        />
        {description.pendingUserInput !== null && (
          <>
            <PendingQuestionsAdapter
              key={`${flowId}:${description.pendingUserInput.callId}`}
              pendingInput={description.pendingUserInput}
              disabled={areMutationsDisabled}
              isSubmitting={state.pendingCommand?.command.kind === "answer"}
              onSubmit={onSubmitAnswers}
            />
            <div className="composer-status-only">
              <AgentRuntimeStatus state={state} />
            </div>
          </>
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
        {description.pendingUserInput === null && (
          <ConversationComposer
            onChange={onComposerChange}
            onSubmit={submitFromComposer}
            placeholder={
              state.isPlanMode
                ? "Describe what you want the Agent to plan…"
                : "Message the Agent…"
            }
            submitDisabled={areMutationsDisabled}
            submitLabel={
              state.pendingCommand?.command.kind === "send"
                ? "Sending…"
                : state.isPlanMode
                  ? "Create plan"
                  : "Send"
            }
            status={<AgentRuntimeStatus state={state} />}
            textareaRef={textareaRef}
            value={state.composer}
          />
        )}
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

function AgentRuntimeStatus({ state }: { state: ActiveConversationState }) {
  return (
    <div className="status-stack" role="group" aria-label="Agent status">
      <span className={`connection-pill ${state.connection}`}>
        {connectionLabel(state.connection)}
      </span>
      <span className="status-copy">
        <strong>{statusLabel(state.snapshot.description.status)}</strong>
        <small>{state.snapshot.description.model}</small>
      </span>
    </div>
  );
}

interface PendingQuestionsAdapterProps {
  pendingInput: PendingUserInput;
  disabled: boolean;
  isSubmitting: boolean;
  onSubmit: (callId: CallId, answers: UserInputAnswer[]) => void;
}

function PendingQuestionsAdapter({
  pendingInput,
  disabled,
  isSubmitting,
  onSubmit,
}: PendingQuestionsAdapterProps) {
  return (
    <PendingQuestionBatch
      disabled={disabled}
      isSubmitting={isSubmitting}
      questions={mapPendingQuestions(pendingInput)}
      onSubmit={(answers) => {
        onSubmit(pendingInput.callId, mapPendingQuestionAnswers(answers));
      }}
    />
  );
}

function mapPendingQuestions(
  pendingInput: PendingUserInput,
): PendingQuestion[] {
  return pendingInput.questions.map((question) => ({
    id: question.id,
    header: question.header,
    question: question.question,
    options: question.options.map((option) => ({
      label: option.label,
      description: option.description,
    })),
  }));
}

function mapPendingQuestionAnswers(
  answers: readonly PendingQuestionAnswer[],
): UserInputAnswer[] {
  return answers.map((answer) => ({
    questionId: answer.questionId,
    answer: answer.answer,
  }));
}

interface PlanPanelProps {
  state: ActiveConversationState;
  areMutationsDisabled: boolean;
  onExecutePlan: (revision: number) => void;
}

interface PlanActionPresentation {
  label: string;
  isDisabled: boolean;
  reason: string | null;
}

function PlanPanel({
  state,
  areMutationsDisabled,
  onExecutePlan,
}: PlanPanelProps) {
  const { description } = state.snapshot;
  const plan = description.plan;
  const isNarrow = useMediaQuery("(max-width: 620px)");
  const [isExpanded, setIsExpanded] = useState(false);
  if (plan === null) return null;
  const taskStatuses = plan.tasks.map(
    (_task, index) =>
      displayedPlanTaskStatus(state, index) ?? TaskStatus.PENDING,
  );
  const completedCount = taskStatuses.filter(
    (status) => status === TaskStatus.COMPLETED,
  ).length;
  const hasRunningTask = taskStatuses.some(
    (status) => status === TaskStatus.IN_PROGRESS,
  );
  const action = planActionPresentation(state, areMutationsDisabled);
  const isContentVisible = !isNarrow || isExpanded;
  return (
    <section className="plan-card plan-panel" aria-label="Agent plan">
      {isNarrow && (
        <button
          type="button"
          className="plan-toggle"
          aria-controls="agent-plan-content"
          aria-expanded={isExpanded}
          onClick={() => {
            setIsExpanded((value) => !value);
          }}
        >
          <span>
            Plan · {String(completedCount)}/{String(plan.tasks.length)} complete
          </span>
          {hasRunningTask && (
            <TaskStatusIndicator status={TaskStatus.IN_PROGRESS} />
          )}
          <span aria-hidden="true">{isExpanded ? "▴" : "▾"}</span>
        </button>
      )}
      {isContentVisible && (
        <div className="plan-content" id="agent-plan-content">
          <div className="section-heading plan-heading">
            <div>
              <p className="eyebrow">Plan revision {plan.revision}</p>
              <h2>{statusLabel(plan.status)}</h2>
            </div>
            {plan.status !== PlanStatus.COMPLETED && (
              <button
                type="button"
                disabled={action.isDisabled}
                aria-describedby={
                  action.reason === null ? undefined : "plan-action-reason"
                }
                onClick={() => {
                  onExecutePlan(plan.revision);
                }}
              >
                {action.label}
              </button>
            )}
          </div>
          {action.reason !== null && plan.status !== PlanStatus.COMPLETED && (
            <p className="plan-action-reason" id="plan-action-reason">
              {action.reason}
            </p>
          )}
          <ol className="plan-tasks">
            {plan.tasks.map((task, index) => {
              const status = taskStatuses[index] ?? task.status;
              return (
                <li className={status} key={`${String(index)}:${task.content}`}>
                  <TaskStatusIndicator status={status} />
                  <div>
                    <strong>{statusLabel(status)}</strong>
                    <p>{task.content}</p>
                  </div>
                </li>
              );
            })}
          </ol>
        </div>
      )}
    </section>
  );
}

function planActionPresentation(
  state: ActiveConversationState,
  areMutationsDisabled: boolean,
): PlanActionPresentation {
  const { description } = state.snapshot;
  const plan = description.plan;
  if (plan === null || plan.status === PlanStatus.COMPLETED) {
    return { label: "Plan completed", isDisabled: true, reason: null };
  }
  if (state.pendingCommand?.command.kind === "execute-plan") {
    return {
      label: "Requesting execution…",
      isDisabled: true,
      reason: "Waiting for the execution request to finish.",
    };
  }
  if (description.isPlanExecutionRequested) {
    return {
      label: "Execution requested",
      isDisabled: true,
      reason: "The Agent will start this Plan from its durable wait.",
    };
  }
  if (areMutationsDisabled) {
    return {
      label: "Syncing plan…",
      isDisabled: true,
      reason: "Waiting for the current durable state reconciliation.",
    };
  }
  if (description.pendingUserInput !== null) {
    return {
      label: "Answer questions first",
      isDisabled: true,
      reason: "Submit the requested answers before continuing this Plan.",
    };
  }
  if (description.pendingApproval !== null) {
    return {
      label: "Resolve approval first",
      isDisabled: true,
      reason: "Approve or reject the pending tool before continuing this Plan.",
    };
  }
  if (description.pendingTimer !== null) {
    return {
      label: "Timer is active",
      isDisabled: true,
      reason:
        "The Plan can continue after the durable Timer finishes or is steered.",
    };
  }
  if (
    description.pendingQueuedMessageCount > 0 ||
    description.pendingSteeredMessageCount > 0
  ) {
    return {
      label: "Resolve queued messages",
      isDisabled: true,
      reason:
        "The Agent must consume or remove queued messages before continuing this Plan.",
    };
  }
  if (
    description.interactionStatus !== AgentInteractionStatus.WAITING ||
    description.status !== AgentStatus.WAITING_FOR_MESSAGE
  ) {
    const isDraft = plan.status === PlanStatus.DRAFT;
    return {
      label: isDraft ? "Preparing plan…" : "Plan running…",
      isDisabled: true,
      reason: isDraft
        ? "Execute becomes available after the Agent reaches its next durable wait."
        : "Continue becomes available if unfinished tasks remain at the next durable wait.",
    };
  }
  return {
    label: plan.status === PlanStatus.DRAFT ? "Execute plan" : "Continue plan",
    isDisabled: false,
    reason: null,
  };
}

function TaskStatusIndicator({ status }: { status: TaskStatus }) {
  if (status === TaskStatus.IN_PROGRESS) {
    return (
      <span className="task-status" aria-label="In progress">
        <span className="task-spinner" aria-hidden="true" />
      </span>
    );
  }
  return (
    <span className="task-status" aria-label={statusLabel(status)}>
      <span aria-hidden="true">{taskIcon(status)}</span>
    </span>
  );
}

function mapPendingMessageQueueItems(
  state: ActiveConversationState,
): PendingMessageQueueItem[] {
  const { queued, steered } = state.snapshot;
  return [
    ...steered.map((message): PendingMessageQueueItem => ({
      id: message.messageId,
      kind: "steered",
      label: "Steering",
      modeLabel: message.value.planMode ? "Plan" : "Chat",
      content: message.value.content,
    })),
    ...queued.map((message): PendingMessageQueueItem => ({
      id: message.messageId,
      kind: "queued",
      label: message.value.planMode ? "Plan" : "Chat",
      content: message.value.content,
      actions: ["steer", "edit", "delete"],
    })),
    ...state.optimisticSubmissions.map(
      (submission): PendingMessageQueueItem => ({
        id: submission.localID,
        kind: "submitting",
        label: submission.phase === "submitting" ? "Submitting…" : "Queued",
        modeLabel: submission.value.planMode ? "Plan" : "Chat",
        content: submission.value.content,
      }),
    ),
  ];
}

function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(
    () =>
      typeof window.matchMedia === "function" &&
      window.matchMedia(query).matches,
  );
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const media = window.matchMedia(query);
    const update = () => {
      setMatches(media.matches);
    };
    update();
    media.addEventListener("change", update);
    return () => {
      media.removeEventListener("change", update);
    };
  }, [query]);
  return matches;
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

function useArchiveScroll(
  beforeSequence: number | null,
  isLoading: boolean,
  messageCount: number,
  onLoadOlder: (beforeSequence: number) => void,
) {
  const requestedSequence = useRef<number | null>(null);
  const previousHeight = useRef<number | null>(null);
  useEffect(() => {
    if (!isLoading) requestedSequence.current = null;
  }, [isLoading]);
  useEffect(() => {
    const loadAtTop = () => {
      if (
        window.scrollY > 80 ||
        beforeSequence === null ||
        isLoading ||
        requestedSequence.current === beforeSequence
      ) {
        return;
      }
      requestedSequence.current = beforeSequence;
      previousHeight.current = document.documentElement.scrollHeight;
      onLoadOlder(beforeSequence);
    };
    window.addEventListener("scroll", loadAtTop, { passive: true });
    return () => {
      window.removeEventListener("scroll", loadAtTop);
    };
  }, [beforeSequence, isLoading, onLoadOlder]);
  useLayoutEffect(() => {
    if (isLoading || previousHeight.current === null) return;
    const addedHeight =
      document.documentElement.scrollHeight - previousHeight.current;
    if (addedHeight > 0)
      window.scrollBy({ top: addedHeight, behavior: "auto" });
    previousHeight.current = null;
  }, [isLoading, messageCount]);
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

function activityLabel(event: AgentEvent): string {
  const label = statusLabel(event.kind);
  return event.toolName === null ? label : `${label} · ${event.toolName}`;
}

function activityIcon(kind: AgentEvent["kind"]): string {
  switch (kind) {
    case EventKind.PLAN_STARTED:
    case EventKind.PLAN_UPDATED:
    case EventKind.PLAN_TASK_UPDATED:
      return "☷";
    case EventKind.STEERING_APPLIED:
      return "↪";
    case EventKind.COMPACTION_FAILED:
    case EventKind.COMPACTED:
      return "↻";
    case EventKind.MODEL_STARTED:
    case EventKind.MODEL_FAILED:
    case EventKind.MODEL_COMPLETED:
      return "✦";
    case EventKind.MODEL_TOOL_CALL:
    case EventKind.TOOL_PROGRESS:
    case EventKind.TOOL_FAILED:
    case EventKind.TOOL_COMPLETED:
      return "⚙";
    case EventKind.USER_INPUT_REQUESTED:
      return "?";
  }
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

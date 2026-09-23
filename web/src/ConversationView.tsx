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
} from "react";
import {
  ApprovalCard,
  ConversationComposer,
  ConversationView as SharedConversationView,
  PendingMessageQueue,
  PendingQuestionBatch,
  TimerCard,
  ToolCallCard,
  ToolRecoveryPanel,
  planActionPresentation as sharedPlanActionPresentation,
  useTimelineFollow,
  type PendingMessageQueueItem,
  type PendingQuestion,
  type PendingQuestionAnswer,
  type TimelineSequencedMessage,
} from "@superdurable/superagent-ui";

import {
  AgentStatus,
  MessageRole,
  PlanStatus,
  TaskStatus,
  ToolRecoveryAction as TransportToolRecoveryAction,
  ToolRecoveryResolution as TransportToolRecoveryResolution,
  type CallId,
  type FlowId,
  type PendingUserMessage,
  type PendingUserInput,
  type ToolRecoveryDecision,
  type ToolRecoveryResolution as ToolRecoveryResolutionValue,
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
  onLoadBeginning: (beforeSequence: number) => void;
  onComposerChange: (value: string) => void;
  onPlanModeChange: (value: boolean) => void;
  onSubmit: () => void;
  onSubmitAnswers: (callId: CallId, answers: UserInputAnswer[]) => void;
  onExecutePlan: (revision: number) => void;
  onApproveTool: (callId: CallId, approved: boolean) => void;
  onResolveToolRecovery: (
    recoveryId: string,
    resolution: ToolRecoveryResolutionValue,
    decisions: ToolRecoveryDecision[],
  ) => void;
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
  onLoadBeginning,
  onComposerChange,
  onPlanModeChange,
  onSubmit,
  onSubmitAnswers,
  onExecutePlan,
  onApproveTool,
  onResolveToolRecovery,
  onMutateQueue,
  onStartAnother,
}: ConversationViewProps) {
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
    description.pendingToolRecovery !== null ||
    description.pendingTimer !== null ||
    description.plan !== null;
  const timelineMessages = snapshot.history
    .messages as TimelineSequencedMessage[];
  const pendingWaits = [
    description.pendingApproval && {
      kind: "approval" as const,
      callId: description.pendingApproval.callId,
      startedAt: description.pendingApproval.startedAt ?? null,
    },
    description.pendingUserInput && {
      kind: "question" as const,
      callId: description.pendingUserInput.callId,
      startedAt: description.pendingUserInput.startedAt ?? null,
    },
    description.pendingTimer && {
      kind: "timer" as const,
      callId: description.pendingTimer.callId,
      startedAt: description.pendingTimer.startedAt ?? null,
    },
    description.pendingToolRecovery && {
      kind: "recovery" as const,
      callId: description.pendingToolRecovery.calls[0]?.callId ?? null,
      startedAt: description.pendingToolRecovery.startedAt ?? null,
    },
  ].filter((value) => value !== null);
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
  const { hasUnseenContent, jumpToLatest, keepLatestVisible } =
    useTimelineFollow({
      flowRunKey: `${flowId}:${snapshot.runId}`,
      contentVersion: `${String(description.lastSequence)}:${liveContentVersion}`,
    });
  const { shellRef, composerRef } = useComposerClearance(keepLatestVisible);
  useArchiveScroll(
    snapshot.history.nextBeforeSequence,
    state.historyRequest !== null,
    snapshot.history.messages.length,
    onLoadOlder,
  );
  useJumpToBeginningScroll(
    state.historyRequest?.mode === "beginning",
    snapshot.history.nextBeforeSequence,
    snapshot.history.messages.length,
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
              <div className="history-controls">
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
                    ? "Load earlier"
                    : state.historyRequest.mode === "beginning"
                      ? "Loading all earlier messages…"
                      : "Loading history…"}
                </button>
                <button
                  type="button"
                  className="text-button"
                  disabled={state.historyRequest !== null}
                  onClick={() => {
                    const beforeSequence = snapshot.history.nextBeforeSequence;
                    if (beforeSequence !== null)
                      onLoadBeginning(beforeSequence);
                  }}
                >
                  Jump to beginning
                </button>
              </div>
            )}
            {snapshot.history.messages.length === 0 &&
              state.activities.length === 0 &&
              state.reasoning.length === 0 &&
              state.assistant === null &&
              state.consumedUserMessages.length === 0 && (
                <div className="empty-state">
                  <h2>Start the conversation</h2>
                  <p>
                    Your messages and durable Agent replies will appear here.
                  </p>
                </div>
              )}
            <SharedConversationView
              className="sa-conversation-view conversation-timeline"
              messages={timelineMessages}
              consumedUserMessages={state.consumedUserMessages}
              reasoning={state.reasoning}
              activities={state.activities}
              assistant={state.assistant}
              pendingWaits={pendingWaits}
              isModelRunning={description.status === AgentStatus.CALLING_MODEL}
              isExecutionLive
              streamState={
                state.connection === "stale"
                  ? "disconnected"
                  : state.assistant !== null && !state.assistant.isComplete
                    ? "streaming"
                    : "complete"
              }
              renderToolCall={(item) => (
                <ToolCallCard
                  call={item.call}
                  result={
                    item.result
                      ? {
                          content: item.result.message.content,
                          toolName: item.result.message.toolName,
                          sequence: item.result.sequence,
                          createdAt: item.result.message.createdAt,
                        }
                      : null
                  }
                />
              )}
              renderMessage={({ sequence, message }) => {
                if (
                  (message.role === MessageRole.TOOL &&
                    message.toolName !== null &&
                    builtInToolNames.has(message.toolName)) ||
                  message.content === ""
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
                  </article>
                );
              }}
            />
          </section>
        </div>

        {hasSidebar && (
          <aside className="conversation-sidebar">
            {description.pendingApproval !== null && (
              <ApprovalCard
                className="sa-approval-card side-card approval-card"
                toolName={description.pendingApproval.toolName}
                argumentsJson={description.pendingApproval.argumentsJson}
                disabled={areMutationsDisabled}
                isSubmitting={state.pendingCommand?.command.kind === "approve"}
                onApprove={() => {
                  const callId = description.pendingApproval?.callId;
                  if (callId !== undefined) onApproveTool(callId, true);
                }}
                onReject={() => {
                  const callId = description.pendingApproval?.callId;
                  if (callId !== undefined) onApproveTool(callId, false);
                }}
              />
            )}

            {description.pendingToolRecovery !== null && (
              <ToolRecoveryPanel
                key={description.pendingToolRecovery.recoveryId}
                recovery={description.pendingToolRecovery}
                disabled={areMutationsDisabled}
                isSubmitting={state.pendingCommand?.command.kind === "recover"}
                onResolve={(recoveryId, recoveryResolution) => {
                  if (recoveryResolution.resolution === "stop") {
                    onResolveToolRecovery(
                      recoveryId,
                      TransportToolRecoveryResolution.STOP,
                      [],
                    );
                    return;
                  }
                  onResolveToolRecovery(
                    recoveryId,
                    TransportToolRecoveryResolution.RESUME,
                    recoveryResolution.decisions.map(
                      (decision): ToolRecoveryDecision => ({
                        callId: decision.callId,
                        action:
                          decision.action === "retry"
                            ? TransportToolRecoveryAction.RETRY
                            : TransportToolRecoveryAction.CONTINUE_WITH_UNKNOWN,
                      }),
                    ),
                  );
                }}
              />
            )}

            {description.pendingTimer !== null && (
              <TimerCard
                className="sa-timer-card side-card timer-card"
                durationSeconds={description.pendingTimer.durationSeconds}
                reason={description.pendingTimer.reason}
              />
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
  if (plan === null) {
    return { label: "Plan completed", isDisabled: true, reason: null };
  }
  return sharedPlanActionPresentation({
    planStatus: plan.status,
    isExecutePlanPending: state.pendingCommand?.command.kind === "execute-plan",
    isPlanExecutionRequested: description.isPlanExecutionRequested,
    areMutationsDisabled,
    hasPendingUserInput: description.pendingUserInput !== null,
    hasPendingApproval: description.pendingApproval !== null,
    hasPendingToolRecovery: description.pendingToolRecovery !== null,
    hasPendingTimer: description.pendingTimer !== null,
    hasPendingQueue:
      description.pendingQueuedMessageCount > 0 ||
      description.pendingSteeredMessageCount > 0,
    isWaitingForInput: state.isWaitingForInput,
    isWaitingForMessage: description.status === AgentStatus.WAITING_FOR_MESSAGE,
  });
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

function useComposerClearance(keepLatestVisible: () => void): {
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
      keepLatestVisible();
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
  }, [keepLatestVisible]);
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
    if (document.documentElement.scrollHeight <= window.innerHeight + 1) {
      loadAtTop();
    }
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

function useJumpToBeginningScroll(
  isLoadingBeginning: boolean,
  beforeSequence: number | null,
  messageCount: number,
) {
  const wasLoading = useRef(false);
  useEffect(() => {
    if (isLoadingBeginning) wasLoading.current = true;
  }, [isLoadingBeginning]);
  useLayoutEffect(() => {
    if (!wasLoading.current || isLoadingBeginning || beforeSequence !== null)
      return;
    wasLoading.current = false;
    window.scrollTo({ top: 0, behavior: "auto" });
  }, [beforeSequence, isLoadingBeginning, messageCount]);
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
  }
}

function statusLabel(value: string): string {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function messageRoleLabel(role: string): string {
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

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
  type KeyboardEvent,
  type SyntheticEvent,
} from "react";

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
  const focusAfterEdit = useRef(false);
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
  const liveContentVersion =
    state.activities.length +
    (state.assistant?.value.length ?? 0) +
    state.reasoning.reduce((total, entry) => total + entry.value.length, 0);
  useAutoScroll(description.lastSequence, liveContentVersion);
  useArchiveScroll(
    snapshot.history.nextBeforeSequence,
    state.historyRequest !== null,
    snapshot.history.messages.length,
    onLoadOlder,
  );
  useEffect(() => {
    const command = state.pendingCommand?.command;
    if (command?.kind === "queue" && command.action === "edit") {
      focusAfterEdit.current = true;
      return;
    }
    if (state.pendingCommand === null && focusAfterEdit.current) {
      focusAfterEdit.current = false;
      textareaRef.current?.focus();
    }
  }, [state.pendingCommand]);
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
        <div className="status-stack" role="group" aria-label="Agent status">
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
        <QueueTray
          state={state}
          areMutationsDisabled={areMutationsDisabled}
          pendingMessageID={pendingMessageID}
          onMutateQueue={onMutateQueue}
        />
        {description.pendingUserInput !== null && (
          <QuestionsPanel
            key={`${flowId}:${description.pendingUserInput.callId}`}
            pendingInput={description.pendingUserInput}
            disabled={areMutationsDisabled}
            isSubmitting={state.pendingCommand?.command.kind === "answer"}
            onSubmit={onSubmitAnswers}
          />
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
          <div className="composer-row">
            <textarea
              ref={textareaRef}
              aria-label="Message"
              value={state.composer}
              disabled={isBusy}
              placeholder={
                state.isPlanMode
                  ? "Describe what you want the Agent to plan…"
                  : "Message the Agent…"
              }
              rows={3}
              onChange={(event) => {
                onComposerChange(event.target.value);
              }}
              onKeyDown={handleComposerKeyDown}
            />
            <button
              type="button"
              disabled={areMutationsDisabled || state.composer.trim() === ""}
              onClick={onSubmit}
            >
              {state.pendingCommand?.command.kind === "send"
                ? "Sending…"
                : state.isPlanMode
                  ? "Create plan"
                  : "Send"}
            </button>
          </div>
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

interface QuestionsPanelProps {
  pendingInput: PendingUserInput;
  disabled: boolean;
  isSubmitting: boolean;
  onSubmit: (callId: CallId, answers: UserInputAnswer[]) => void;
}

interface QuestionDraft {
  answer: string;
  isOther: boolean;
}

function QuestionsPanel({
  pendingInput,
  disabled,
  isSubmitting,
  onSubmit,
}: QuestionsPanelProps) {
  const [currentIndex, setCurrentIndex] = useState(0);
  const [drafts, setDrafts] = useState<Record<string, QuestionDraft>>({});
  const question = pendingInput.questions[currentIndex];
  if (question === undefined) return null;
  const currentDraft = drafts[question.id];
  const hasAllAnswers = pendingInput.questions.every(
    ({ id }) => (drafts[id]?.answer.trim().length ?? 0) > 0,
  );
  const isLast = currentIndex === pendingInput.questions.length - 1;
  const setAnswer = (answer: string, isOther: boolean) => {
    setDrafts((current) => ({
      ...current,
      [question.id]: { answer, isOther },
    }));
  };
  const chooseOption = (answer: string) => {
    setAnswer(answer, false);
    if (!isLast) setCurrentIndex((index) => index + 1);
  };
  const submit = () => {
    if (!hasAllAnswers || disabled) return;
    onSubmit(
      pendingInput.callId,
      pendingInput.questions.map(({ id }) => ({
        questionId: id,
        answer: drafts[id]?.answer.trim() ?? "",
      })),
    );
  };

  return (
    <section className="pending-input" aria-label="Agent questions">
      <div className="question-heading">
        <div>
          <p className="eyebrow">Agent needs your input</p>
          <strong>
            Question {String(currentIndex + 1)} of{" "}
            {String(pendingInput.questions.length)}
          </strong>
        </div>
        <div className="question-tabs" aria-label="Questions">
          {pendingInput.questions.map((candidate, index) => (
            <button
              type="button"
              className={index === currentIndex ? "active" : "secondary"}
              aria-current={index === currentIndex ? "step" : undefined}
              key={candidate.id}
              onClick={() => {
                setCurrentIndex(index);
              }}
            >
              {candidate.header}
              {(drafts[candidate.id]?.answer.trim().length ?? 0) > 0 && (
                <span className="answered-mark" aria-label="Answered">
                  ✓
                </span>
              )}
            </button>
          ))}
        </div>
      </div>
      <fieldset className="question-content" disabled={disabled}>
        <legend>{question.header}</legend>
        <p>{question.question}</p>
        <div className="choice-row">
          {question.options.map((option) => (
            <button
              type="button"
              className={
                currentDraft?.isOther === false &&
                currentDraft.answer === option.label
                  ? "question-option selected"
                  : "question-option secondary"
              }
              key={option.label}
              onClick={() => {
                chooseOption(option.label);
              }}
            >
              <strong>{option.label}</strong>
              <small>{option.description}</small>
            </button>
          ))}
          <button
            type="button"
            className={
              currentDraft?.isOther === true
                ? "question-option selected"
                : "question-option secondary"
            }
            onClick={() => {
              setAnswer(
                currentDraft?.isOther === true ? currentDraft.answer : "",
                true,
              );
            }}
          >
            <strong>Other</strong>
            <small>Enter a different answer.</small>
          </button>
        </div>
        {currentDraft?.isOther === true && (
          <label className="other-answer">
            Other answer
            <input
              aria-label={`Other answer for ${question.header}`}
              value={currentDraft.answer}
              onChange={(event) => {
                setAnswer(event.target.value, true);
              }}
            />
          </label>
        )}
      </fieldset>
      <div className="question-navigation">
        <button
          type="button"
          className="secondary"
          disabled={disabled || currentIndex === 0}
          onClick={() => {
            setCurrentIndex((index) => Math.max(0, index - 1));
          }}
        >
          Previous
        </button>
        {!isLast && (
          <button
            type="button"
            className="secondary"
            disabled={disabled || currentDraft?.answer.trim() === ""}
            onClick={() => {
              setCurrentIndex((index) => index + 1);
            }}
          >
            Next
          </button>
        )}
        {isLast && (
          <button
            type="button"
            disabled={disabled || !hasAllAnswers}
            onClick={submit}
          >
            {isSubmitting ? "Submitting answers…" : "Submit all"}
          </button>
        )}
      </div>
    </section>
  );
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

interface QueueTrayProps {
  state: ActiveConversationState;
  areMutationsDisabled: boolean;
  pendingMessageID: string | null;
  onMutateQueue: (
    message: PendingUserMessage,
    action: QueueCommandAction,
  ) => void;
}

function QueueTray({
  state,
  areMutationsDisabled,
  pendingMessageID,
  onMutateQueue,
}: QueueTrayProps) {
  const { queued, steered } = state.snapshot;
  const hasMessages =
    queued.length > 0 ||
    steered.length > 0 ||
    state.optimisticSubmissions.length > 0;
  const signature = [
    ...steered.map(({ messageId }) => `steered:${messageId}`),
    ...queued.map(({ messageId }) => `queued:${messageId}`),
    ...state.optimisticSubmissions.map(({ localID }) => `local:${localID}`),
  ].join("|");
  const [collapsedSignature, setCollapsedSignature] = useState<string | null>(
    null,
  );
  const isExpanded = collapsedSignature !== signature;
  if (!hasMessages) return null;
  return (
    <section className="queue-tray" aria-label="Message queue">
      <button
        type="button"
        className="queue-toggle"
        aria-controls="message-queue-items"
        aria-expanded={isExpanded}
        onClick={() => {
          setCollapsedSignature(isExpanded ? signature : null);
        }}
      >
        <span>Message queue</span>
        <strong>
          {String(queued.length + state.optimisticSubmissions.length)} queued ·{" "}
          {String(steered.length)} steering
        </strong>
        <span aria-hidden="true">{isExpanded ? "▴" : "▾"}</span>
      </button>
      {isExpanded && (
        <div className="queue-items" id="message-queue-items">
          {steered.map((message) => (
            <div className="queue-message steered" key={message.messageId}>
              <strong>Steering</strong>
              <small>{message.value.planMode ? "Plan" : "Chat"}</small>
              <p>{message.value.content}</p>
            </div>
          ))}
          {queued.map((message) => (
            <div className="queue-message" key={message.messageId}>
              <strong>{message.value.planMode ? "Plan" : "Chat"}</strong>
              <p>{message.value.content}</p>
              <div className="queue-actions">
                {(["steer", "edit", "delete"] as const).map((action) => (
                  <button
                    type="button"
                    className={
                      action === "steer"
                        ? "queue-action steer-action"
                        : "queue-action text-button"
                    }
                    disabled={areMutationsDisabled}
                    key={action}
                    onClick={() => {
                      onMutateQueue(message, action);
                    }}
                  >
                    {pendingMessageID === message.messageId ? (
                      "Updating…"
                    ) : action === "steer" ? (
                      <>
                        <span aria-hidden="true">↪</span>
                        Steer now
                      </>
                    ) : (
                      statusLabel(action)
                    )}
                  </button>
                ))}
              </div>
            </div>
          ))}
          {state.optimisticSubmissions.map((submission) => (
            <div className="queue-message submitting" key={submission.localID}>
              <strong>
                {submission.phase === "submitting" ? "Submitting…" : "Queued"}
              </strong>
              <small>{submission.value.planMode ? "Plan" : "Chat"}</small>
              <p>{submission.value.content}</p>
            </div>
          ))}
        </div>
      )}
    </section>
  );
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

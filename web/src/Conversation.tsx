/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  useCallback,
  useEffect,
  useReducer,
  useRef,
  type Dispatch,
} from "react";

import {
  AgentInteractionStatus,
  EventStream,
  PollTimeoutReason,
  answerQuestions,
  approveTool,
  deleteQueuedMessage,
  executePlan,
  getAgentSnapshot,
  getArchivedMessages,
  readEvent,
  sendMessage,
  steerQueuedMessage,
  waitForAgentInteractionStatus,
  type CallId,
  type FlowId,
  type PendingUserMessage,
  type ResumeToken,
  type StreamEvent,
  type ToolName,
  type UserInputAnswer,
} from "./api/generated";
import {
  conversationReducer,
  initialConversationState,
  type Command,
  type ConversationAction,
  type LiveUpdate,
  type QueueCommandAction,
} from "./conversation-state";
import { ConversationView } from "./ConversationView";
import {
  SnapshotCoordinator,
  type SnapshotTrigger,
} from "./snapshot-coordinator";

const eventStreams = [
  EventStream.REASONING,
  EventStream.ASSISTANT,
  EventStream.ACTIVITY,
] as const;

interface ConversationProps {
  flowId: FlowId;
  builtInTools: readonly ToolName[];
  onStartAnother: () => void;
}

export function Conversation({
  flowId,
  builtInTools,
  onStartAnother,
}: ConversationProps) {
  const [state, dispatch] = useReducer(
    conversationReducer,
    undefined,
    initialConversationState,
  );
  const resumeTokens = useRef<Record<EventStream, ResumeToken | undefined>>({
    [EventStream.REASONING]: undefined,
    [EventStream.ASSISTANT]: undefined,
    [EventStream.ACTIVITY]: undefined,
  });
  const nextHistoryRequestID = useRef(1);
  const isTerminal = state.kind === "ready" && state.lifecycle === "terminal";
  const requestSnapshot = useSnapshotCoordinator(flowId, dispatch, isTerminal);
  const runCommand = useCommandRunner(dispatch, requestSnapshot);

  const historyRequest = state.kind === "ready" ? state.historyRequest : null;
  useEffect(() => {
    if (historyRequest === null) return;
    const controller = new AbortController();
    let isCurrent = true;
    void getArchivedMessages({
      query: {
        flowId,
        beforeSequence: historyRequest.beforeSequence,
      },
      signal: controller.signal,
    })
      .then((page) => {
        if (isCurrent) {
          dispatch({ type: "older-loaded", id: historyRequest.id, page });
        }
      })
      .catch((reason: unknown) => {
        if (isCurrent && !controller.signal.aborted) {
          dispatch({
            type: "older-failed",
            id: historyRequest.id,
            message: errorMessage(reason),
          });
        }
      });
    return () => {
      isCurrent = false;
      controller.abort();
    };
  }, [flowId, historyRequest]);

  const subscriptionGeneration =
    state.kind === "ready" && state.lifecycle === "active"
      ? state.subscriptionGeneration
      : -1;
  const activeRunID =
    state.kind === "ready" && state.lifecycle === "active"
      ? state.snapshot.runId
      : null;
  useEffect(() => {
    resetResumeTokens(resumeTokens.current);
  }, [flowId, activeRunID]);
  useEffect(() => {
    if (subscriptionGeneration < 0) return;
    const controller = new AbortController();
    let isCurrent = true;
    const poll = async (stream: EventStream): Promise<void> => {
      let resumeToken = resumeTokens.current[stream];
      while (isCurrent && !controller.signal.aborted) {
        try {
          const event = await readEvent({
            query: { flowId, stream, resumeToken },
            signal: controller.signal,
          });
          resumeToken = event.resumeToken;
          resumeTokens.current[stream] = resumeToken;
          const update = liveUpdate(stream, event);
          dispatch({ type: "stream-update", update });
        } catch (reason: unknown) {
          if (isAbortError(reason)) return;
          if (isPollTimeout(reason)) {
            await waitBeforeNextPoll(controller.signal);
            continue;
          }
          isCurrent = false;
          controller.abort();
          dispatch({
            type: "stream-failed",
            message: `Live updates disconnected: ${errorMessage(reason)}`,
          });
          requestSnapshot({ blocking: true, connection: "reconnecting" });
        }
      }
    };
    for (const stream of eventStreams) void poll(stream);
    return () => {
      isCurrent = false;
      controller.abort();
    };
  }, [flowId, activeRunID, subscriptionGeneration, requestSnapshot]);

  const interactionStatus =
    state.kind === "ready" && state.lifecycle === "active"
      ? state.snapshot.description.interactionStatus
      : null;
  useEffect(() => {
    if (interactionStatus === null) return;
    const controller = new AbortController();
    let isCurrent = true;
    const wait = async (): Promise<void> => {
      let expectedStatus =
        interactionStatus === AgentInteractionStatus.WAITING
          ? AgentInteractionStatus.SUBMITTED
          : AgentInteractionStatus.WAITING;
      while (isCurrent && !controller.signal.aborted) {
        try {
          const result = await waitForAgentInteractionStatus({
            query: { flowId, expectedStatus },
            signal: controller.signal,
          });
          expectedStatus =
            result.status === AgentInteractionStatus.WAITING
              ? AgentInteractionStatus.SUBMITTED
              : AgentInteractionStatus.WAITING;
          if (result.status === AgentInteractionStatus.WAITING) {
            requestSnapshot({ blocking: true });
          }
        } catch (reason: unknown) {
          if (isAbortError(reason)) return;
          if (isPollTimeout(reason)) {
            await waitBeforeNextPoll(controller.signal);
            continue;
          }
          dispatch({
            type: "stream-failed",
            message: `Durable status disconnected: ${errorMessage(reason)}`,
          });
          requestSnapshot({ blocking: true, connection: "reconnecting" });
          return;
        }
      }
    };
    void wait();
    return () => {
      isCurrent = false;
      controller.abort();
    };
  }, [flowId, interactionStatus, subscriptionGeneration, requestSnapshot]);

  if (state.kind === "loading") {
    return (
      <ConversationStatus
        title="Loading durable conversation"
        detail="Reading one atomic Agent Snapshot…"
      />
    );
  }
  if (state.kind === "failed") {
    return (
      <ConversationStatus
        title="Snapshot unavailable"
        detail={state.message}
        action={() => {
          requestSnapshot({ blocking: true, connection: "stale" });
        }}
      />
    );
  }
  if (state.lifecycle === "terminal") {
    return (
      <ConversationStatus
        title={`Agent ${statusLabel(state.snapshot.flowStatus)}`}
        detail={
          state.snapshot.errorMessage ??
          `Run ${state.snapshot.runId} is no longer active.`
        }
        action={onStartAnother}
        actionLabel="Start another agent"
      />
    );
  }

  const isBusy = state.pendingCommand !== null;
  const areMutationsDisabled = isBusy || state.reconciliation !== "open";
  const submitMessage = () => {
    const content = state.composer.trim();
    if (content === "" || areMutationsDisabled) return;
    const value = {
      content,
      planMode: state.isPlanMode,
    };
    const submission = {
      value,
      submittedAfterSequence: state.snapshot.description.lastSequence,
      knownMessageIDs: [
        ...state.snapshot.queued.map((message) => message.messageId),
        ...state.snapshot.steered.map((message) => message.messageId),
      ],
    };
    runCommand({ kind: "send", ...submission }, (signal) =>
      sendMessage({
        body: {
          flowId,
          ...value,
        },
        signal,
      }),
    );
  };
  const submitAnswers = (callID: CallId, answers: UserInputAnswer[]) => {
    if (areMutationsDisabled) return;
    const pendingInput = state.snapshot.description.pendingUserInput;
    if (pendingInput?.callId !== callID) return;
    const answersByQuestion = new Map(
      answers.map((answer) => [answer.questionId, answer.answer]),
    );
    const value = {
      content: pendingInput.questions
        .map(
          (question) =>
            `**${question.header}**: ${answersByQuestion.get(question.id) ?? ""}`,
        )
        .join("\n\n"),
      planMode: false,
    };
    const command: Command = {
      kind: "answer",
      callID,
      value,
      submittedAfterSequence: state.snapshot.description.lastSequence,
      knownMessageIDs: [
        ...state.snapshot.queued.map((message) => message.messageId),
        ...state.snapshot.steered.map((message) => message.messageId),
      ],
    };
    runCommand(command, (signal) =>
      answerQuestions({ body: { flowId, callId: callID, answers }, signal }),
    );
  };
  const mutateQueue = (
    message: PendingUserMessage,
    action: QueueCommandAction,
  ) => {
    if (areMutationsDisabled) return;
    const command: Command = { kind: "queue", action, message };
    const body = { flowId, messageId: message.messageId };
    runCommand(command, (signal) =>
      action === "steer"
        ? steerQueuedMessage({ body, signal })
        : deleteQueuedMessage({ body, signal }),
    );
  };

  return (
    <ConversationView
      flowId={flowId}
      builtInTools={builtInTools}
      state={state}
      onRetrySnapshot={() => {
        requestSnapshot({ blocking: true, connection: "stale" });
      }}
      onLoadOlder={(beforeSequence) => {
        dispatch({
          type: "older-requested",
          id: nextHistoryRequestID.current++,
          beforeSequence,
        });
      }}
      onComposerChange={(value) => {
        dispatch({ type: "composer-changed", value });
      }}
      onPlanModeChange={(value) => {
        dispatch({ type: "plan-mode-changed", value });
      }}
      onSubmit={submitMessage}
      onSubmitAnswers={submitAnswers}
      onExecutePlan={(revision) => {
        runCommand({ kind: "execute-plan" }, (signal) =>
          executePlan({ body: { flowId, revision }, signal }),
        );
      }}
      onApproveTool={(callId: CallId, approved) => {
        runCommand({ kind: "approve" }, (signal) =>
          approveTool({ body: { flowId, callId, approved }, signal }),
        );
      }}
      onMutateQueue={mutateQueue}
      onStartAnother={onStartAnother}
    />
  );
}

function ConversationStatus({
  title,
  detail,
  action,
  actionLabel = "Retry Snapshot",
}: {
  title: string;
  detail: string;
  action?: () => void;
  actionLabel?: string;
}) {
  return (
    <main className="status-shell">
      <section className="status-card">
        <h1>{title}</h1>
        <p>{detail}</p>
        {action !== undefined && (
          <button type="button" onClick={action}>
            {actionLabel}
          </button>
        )}
      </section>
    </main>
  );
}

function useSnapshotCoordinator(
  flowId: FlowId,
  dispatch: Dispatch<ConversationAction>,
  isTerminal: boolean,
) {
  const coordinator = useRef<SnapshotCoordinator | null>(null);
  useEffect(() => {
    const current = new SnapshotCoordinator({
      load: (signal) =>
        getAgentSnapshot({
          query: { flowId },
          signal,
        }),
      requested: (trigger) => {
        dispatch({ type: "snapshot-requested", ...trigger });
      },
      loaded: (snapshot) => {
        dispatch({ type: "snapshot-loaded", snapshot });
      },
      failed: (message) => {
        dispatch({ type: "snapshot-failed", message });
      },
    });
    coordinator.current = current;
    const handleVisibilityChange = () => {
      current.setVisible(document.visibilityState === "visible");
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);
    current.start();
    current.setVisible(document.visibilityState === "visible");
    return () => {
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      current.stop();
      if (coordinator.current === current) coordinator.current = null;
    };
  }, [dispatch, flowId]);
  useEffect(() => {
    if (isTerminal) coordinator.current?.stop();
  }, [isTerminal]);
  return useCallback((trigger: SnapshotTrigger) => {
    coordinator.current?.request(trigger);
  }, []);
}

function useCommandRunner(
  dispatch: Dispatch<ConversationAction>,
  requestSnapshot: (trigger: SnapshotTrigger) => void,
) {
  const nextID = useRef(1);
  const activeController = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      activeController.current?.abort();
    },
    [],
  );
  return useCallback(
    (
      command: Command,
      operation: (signal: AbortSignal) => Promise<unknown>,
    ) => {
      if (activeController.current !== null) return;
      const id = nextID.current++;
      const controller = new AbortController();
      activeController.current = controller;
      dispatch({ type: "command-started", id, command });
      void operation(controller.signal)
        .then(() => {
          if (!controller.signal.aborted) {
            dispatch({ type: "command-succeeded", id });
            requestSnapshot({ blocking: true });
          }
        })
        .catch((reason: unknown) => {
          if (!controller.signal.aborted) {
            dispatch({
              type: "command-failed",
              id,
              message: errorMessage(reason),
            });
            requestSnapshot({ blocking: true, connection: "stale" });
          }
        })
        .finally(() => {
          if (activeController.current === controller) {
            activeController.current = null;
          }
        });
    },
    [dispatch, requestSnapshot],
  );
}

function liveUpdate(stream: EventStream, event: StreamEvent): LiveUpdate {
  switch (stream) {
    case EventStream.REASONING:
      if (event.kind !== "reasoning_summary") {
        throw new Error("Reasoning Stream returned a mismatched event kind.");
      }
      return {
        kind: "reasoning",
        value: event.value,
        source: event.source,
        createdAt: event.createdAt,
      };
    case EventStream.ASSISTANT:
      if (event.kind !== "assistant_text") {
        throw new Error("Assistant Stream returned a mismatched event kind.");
      }
      return {
        kind: "assistant",
        value: event.value,
        source: event.source,
        createdAt: event.createdAt,
      };
    case EventStream.ACTIVITY:
      if (event.kind !== "activity") {
        throw new Error("Activity Stream returned a mismatched event kind.");
      }
      return {
        kind: "activity",
        value: event.value,
        resumeToken: event.resumeToken,
        source: event.source,
        createdAt: event.createdAt,
      };
  }
}

function resetResumeTokens(
  tokens: Record<EventStream, ResumeToken | undefined>,
): void {
  for (const stream of eventStreams) tokens[stream] = undefined;
}

function isPollTimeout(reason: unknown): boolean {
  return (
    typeof reason === "object" &&
    reason !== null &&
    "reason" in reason &&
    reason.reason === PollTimeoutReason.TIMEOUT
  );
}

function isAbortError(reason: unknown): boolean {
  return reason instanceof DOMException && reason.name === "AbortError";
}

function errorMessage(reason: unknown): string {
  if (reason instanceof Error) return reason.message;
  if (
    typeof reason === "object" &&
    reason !== null &&
    "detail" in reason &&
    typeof reason.detail === "string"
  ) {
    return reason.detail;
  }
  return "The request could not be completed.";
}

function statusLabel(value: string): string {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

async function waitBeforeNextPoll(signal: AbortSignal): Promise<void> {
  await new Promise<void>((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    let timeout = 0;
    const finish = () => {
      window.clearTimeout(timeout);
      signal.removeEventListener("abort", finish);
      resolve();
    };
    timeout = window.setTimeout(finish, 250);
    signal.addEventListener("abort", finish, { once: true });
  });
}

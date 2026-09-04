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
  AgentStatus,
  EventStream,
  PollTimeoutReason,
  approveTool,
  deleteQueuedMessage,
  executePlan,
  getAgentSnapshot,
  readEvent,
  sendMessage,
  steerQueuedMessage,
  type CallId,
  type FlowId,
  type PendingUserMessage,
  type ResumeToken,
  type StreamEvent,
  type ToolName,
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

const snapshotPageSize = 50;
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
  const runCommand = useCommandRunner(dispatch);

  useEffect(() => {
    const controller = new AbortController();
    let isCurrent = true;
    void getAgentSnapshot({
      query: { flowId, limit: snapshotPageSize },
      signal: controller.signal,
    })
      .then((snapshot) => {
        if (isCurrent) dispatch({ type: "snapshot-loaded", snapshot });
      })
      .catch((reason: unknown) => {
        if (isCurrent && !controller.signal.aborted) {
          dispatch({
            type: "snapshot-failed",
            message: errorMessage(reason),
          });
        }
      });
    return () => {
      isCurrent = false;
      controller.abort();
    };
  }, [flowId, state.snapshotRequest]);

  const historyRequest = state.kind === "ready" ? state.historyRequest : null;
  useEffect(() => {
    if (historyRequest === null) return;
    const controller = new AbortController();
    let isCurrent = true;
    void getAgentSnapshot({
      query: {
        flowId,
        beforeSequence: historyRequest.beforeSequence,
        limit: snapshotPageSize,
      },
      signal: controller.signal,
    })
      .then((snapshot) => {
        if (isCurrent) {
          dispatch({ type: "older-loaded", id: historyRequest.id, snapshot });
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
  useEffect(() => {
    if (subscriptionGeneration < 0) return;
    const controller = new AbortController();
    let isCurrent = true;
    let reconciliationTimeout: number | null = null;
    const scheduleReconciliation = () => {
      if (reconciliationTimeout !== null) {
        window.clearTimeout(reconciliationTimeout);
      }
      reconciliationTimeout = window.setTimeout(() => {
        dispatch({ type: "request-snapshot", connection: "live" });
      }, 200);
    };
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
          if (update.kind === "activity") {
            scheduleReconciliation();
          }
        } catch (reason: unknown) {
          if (isAbortError(reason)) return;
          if (isPollTimeout(reason)) {
            await waitBeforeNextPoll(controller.signal);
            continue;
          }
          isCurrent = false;
          controller.abort();
          resetResumeTokens(resumeTokens.current);
          dispatch({
            type: "stream-failed",
            message: `Live updates disconnected: ${errorMessage(reason)}`,
          });
        }
      }
    };
    for (const stream of eventStreams) void poll(stream);
    return () => {
      isCurrent = false;
      controller.abort();
      if (reconciliationTimeout !== null) {
        window.clearTimeout(reconciliationTimeout);
      }
    };
  }, [flowId, subscriptionGeneration]);

  const snapshotStatus =
    state.kind === "ready" && state.lifecycle === "active"
      ? state.snapshot.description.status
      : null;
  useEffect(() => {
    const delay = fastSnapshotDelay(snapshotStatus);
    if (delay === null) return;
    const timeout = window.setTimeout(() => {
      dispatch({ type: "request-snapshot", connection: "live" });
    }, delay);
    return () => {
      window.clearTimeout(timeout);
    };
  }, [snapshotStatus, state.snapshotRequest]);

  const isActive = state.kind === "ready" && state.lifecycle === "active";
  useEffect(() => {
    if (!isActive) return;
    const requestReconciliation = () => {
      dispatch({ type: "request-snapshot", connection: "stale" });
    };
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") requestReconciliation();
    };
    window.addEventListener("focus", requestReconciliation);
    window.addEventListener("online", requestReconciliation);
    document.addEventListener("visibilitychange", onVisibilityChange);
    const interval = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        dispatch({ type: "request-snapshot", connection: "live" });
      }
    }, 8_000);
    return () => {
      window.clearInterval(interval);
      window.removeEventListener("focus", requestReconciliation);
      window.removeEventListener("online", requestReconciliation);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [isActive]);

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
          dispatch({ type: "request-snapshot", connection: "stale" });
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
  const submitMessage = () => {
    const content = state.composer.trim();
    if (content === "" || isBusy) return;
    const value = {
      content,
      planMode:
        state.snapshot.description.pendingUserInput === null &&
        state.isPlanMode,
    };
    runCommand(
      {
        kind: "send",
        value,
        submittedAfterSequence: state.snapshot.description.lastSequence,
        knownMessageIDs: [
          ...state.snapshot.queued.map((message) => message.messageId),
          ...state.snapshot.steered.map((message) => message.messageId),
        ],
      },
      (signal) =>
        sendMessage({
          body: {
            flowId,
            ...value,
          },
          signal,
        }),
    );
  };
  const mutateQueue = (
    message: PendingUserMessage,
    action: QueueCommandAction,
  ) => {
    if (isBusy) return;
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
        dispatch({ type: "request-snapshot", connection: "stale" });
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

function useCommandRunner(dispatch: Dispatch<ConversationAction>) {
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
          }
        })
        .catch((reason: unknown) => {
          if (!controller.signal.aborted) {
            dispatch({
              type: "command-failed",
              id,
              message: errorMessage(reason),
            });
          }
        })
        .finally(() => {
          if (activeController.current === controller) {
            activeController.current = null;
          }
        });
    },
    [dispatch],
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

function fastSnapshotDelay(status: AgentStatus | null): number | null {
  switch (status) {
    case AgentStatus.INITIALIZING:
      return 250;
    case AgentStatus.COMPACTING_CONTEXT:
    case AgentStatus.CALLING_MODEL:
    case AgentStatus.ROUTING_TOOL:
    case AgentStatus.EXECUTING_TOOL:
    case AgentStatus.APPLYING_STEERING:
      return 750;
    case AgentStatus.WAITING_FOR_MESSAGE:
    case AgentStatus.WAITING_FOR_TOOL_APPROVAL:
    case AgentStatus.WAITING_FOR_TIMER:
    case null:
      return null;
  }
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

/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "./App";
import {
  AgentStatus,
  EventKind,
  EventStream,
  MessageRole,
  PlanStatus,
  Provider,
  TaskStatus,
  answerQuestions,
  getAgentSnapshot,
  getPortal,
  listRecentEvents,
  readEvent,
  resolveToolRecovery,
  sendMessage,
  startAgent,
  steerQueuedMessage,
  waitForWaitingInputRound,
  type AgentSnapshot,
  type AgentDescription,
  type Portal,
  type StreamEvent,
} from "./api/generated";
import type * as GeneratedAPI from "./api/generated";

vi.mock("./api/generated", async (importOriginal) => {
  const generated = await importOriginal<typeof GeneratedAPI>();
  return {
    ...generated,
    answerQuestions: vi.fn(),
    approveTool: vi.fn(),
    deleteQueuedMessage: vi.fn(),
    executePlan: vi.fn(),
    getAgentSnapshot: vi.fn(),
    getPortal: vi.fn(),
    listRecentEvents: vi.fn(),
    readEvent: vi.fn(),
    resolveToolRecovery: vi.fn(),
    sendMessage: vi.fn(),
    startAgent: vi.fn(),
    steerQueuedMessage: vi.fn(),
    waitForWaitingInputRound: vi.fn(),
  };
});

const portal: Portal = {
  providers: [
    {
      id: Provider.MOCK,
      label: "Mock",
      modelPrefix: "mock/",
      defaultModel: "mock/reliable",
      credentialEnvironmentVariable: null,
      configured: true,
    },
  ],
  mcpServers: ["local-tools"],
  tools: [
    {
      name: "local-tools.search",
      description: "Search a deterministic fixture.",
      requiresApproval: false,
      server: "local-tools",
    },
  ],
  builtInTools: ["ask_user", "wait"],
};

const activeDescription: AgentDescription = {
  status: AgentStatus.WAITING_FOR_MESSAGE,
  waitingInputRound: 1,
  model: "mock/reliable",
  systemPrompt: "Be helpful.",
  firstRetainedSequence: 1,
  lastSequence: 0,
  summarizedThroughSequence: 0,
  pendingApproval: null,
  pendingToolRecovery: null,
  pendingTimer: null,
  pendingUserInput: null,
  plan: null,
  isPlanExecutionRequested: false,
  pendingQueuedMessageCount: 0,
  pendingSteeredMessageCount: 0,
  availableMcpServers: ["local-tools"],
  availableTools: ["local-tools.search"],
  inactivityDeadline: null,
};

const snapshot: AgentSnapshot = {
  runId: "run-1",
  history: { messages: [], nextBeforeSequence: null },
  description: activeDescription,
  queued: [],
  steered: [],
};

describe("App", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.history.replaceState({}, "", "/");
    vi.mocked(getPortal).mockResolvedValue(portal);
    vi.mocked(getAgentSnapshot).mockResolvedValue(snapshot);
    vi.mocked(answerQuestions).mockResolvedValue({ accepted: true });
    vi.mocked(listRecentEvents).mockResolvedValue({ events: [] });
    vi.mocked(readEvent).mockImplementation(
      ({ signal }) =>
        new Promise((_resolve, reject) => {
          signal?.addEventListener(
            "abort",
            () => {
              reject(new DOMException("Aborted", "AbortError"));
            },
            { once: true },
          );
        }),
    );
    vi.mocked(waitForWaitingInputRound).mockImplementation(
      ({ signal }) =>
        new Promise((_resolve, reject) => {
          signal?.addEventListener(
            "abort",
            () => {
              reject(new DOMException("Aborted", "AbortError"));
            },
            { once: true },
          );
        }),
    );
    vi.mocked(startAgent).mockResolvedValue({ flowId: "flow-created" });
    vi.mocked(sendMessage).mockResolvedValue({ accepted: true });
    vi.mocked(resolveToolRecovery).mockResolvedValue({ accepted: true });
    vi.mocked(steerQueuedMessage).mockResolvedValue({
      messageId: "message-1",
      action: "steered",
    });
  });

  afterEach(cleanup);

  it("loads the generated portal contract", async () => {
    render(<App />);

    expect(screen.getByText("Loading SuperAgent")).toBeInTheDocument();
    expect(await screen.findByText("Start a SuperAgent")).toBeInTheDocument();
    expect(getPortal).toHaveBeenCalledTimes(1);
    expect(startAgent).not.toHaveBeenCalled();
  });

  it("prefers a configured provider and omits unavailable MCP controls", async () => {
    const mockProvider = portal.providers[0];
    if (mockProvider === undefined) throw new Error("expected mock provider");
    vi.mocked(getPortal).mockResolvedValueOnce({
      ...portal,
      providers: [
        mockProvider,
        {
          id: Provider.OPENAI,
          label: "OpenAI",
          modelPrefix: "openai",
          defaultModel: "gpt-5-mini",
          credentialEnvironmentVariable: "OPENAI_API_KEY",
          configured: true,
        },
      ],
      mcpServers: [],
      tools: [],
    });

    render(<App />);

    expect(await screen.findByRole("radio", { name: /OpenAI/ })).toBeChecked();
    expect(
      screen.queryByRole("checkbox", { name: "Enable trusted MCP servers" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "No trusted MCP servers are configured for this Worker.",
      ),
    ).toBeInTheDocument();
  });

  it("atomically loads one Snapshot when resuming a Flow", async () => {
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "SuperAgent" }),
    ).toBeInTheDocument();
    expect(screen.getByText("flow-existing")).toBeInTheDocument();
    expect(screen.getByText("run-1")).toBeInTheDocument();
    expect(getPortal).toHaveBeenCalledTimes(1);
    expect(getAgentSnapshot).toHaveBeenCalledTimes(1);
    expect(listRecentEvents).toHaveBeenCalledTimes(3);
    expect(readEvent).toHaveBeenCalledTimes(3);
    expect(startAgent).not.toHaveBeenCalled();
  });

  it("recovers each bounded Stream tail before resuming live polls", async () => {
    vi.mocked(listRecentEvents).mockImplementation(({ query }) => {
      switch (query.stream) {
        case EventStream.REASONING:
          return Promise.resolve({
            events: [
              {
                kind: "reasoning_summary",
                value: "Recovered reasoning",
                resumeToken: "reasoning-tail",
                createdAt: "2026-09-03T00:01:00Z",
                source: "model-1",
              },
            ],
          });
        case EventStream.ASSISTANT:
          return Promise.resolve({
            events: [
              {
                kind: "assistant_text",
                value: "Recovered answer",
                resumeToken: "assistant-tail",
                createdAt: "2026-09-03T00:02:00Z",
                source: "model-1",
              },
            ],
          });
        case EventStream.ACTIVITY:
          return Promise.resolve({
            events: [
              activityEvent(
                "activity-tail",
                EventKind.TOOL_PROGRESS,
                "Recovered activity",
                "2026-09-03T00:03:00Z",
              ),
            ],
          });
      }
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    const history = await screen.findByLabelText("Conversation history");
    await within(history).findByText("Recovered activity");
    expect(within(history).getByText("Recovered answer")).toBeInTheDocument();
    expect(
      within(history).getByText("Recovered reasoning"),
    ).toBeInTheDocument();
    await waitFor(() => {
      expect(readEvent).toHaveBeenCalledTimes(3);
    });
    const queries = vi
      .mocked(readEvent)
      .mock.calls.map(([options]) => options.query);
    expect(queries).toContainEqual({
      flowId: "flow-existing",
      stream: EventStream.ACTIVITY,
      resumeToken: "activity-tail",
    });
    expect(queries).toContainEqual({
      flowId: "flow-existing",
      stream: EventStream.ASSISTANT,
      resumeToken: "assistant-tail",
    });
    expect(queries).toContainEqual({
      flowId: "flow-existing",
      stream: EventStream.REASONING,
      resumeToken: "reasoning-tail",
    });
  });

  it("starts through the generated client and loads one Snapshot", async () => {
    render(<App />);
    const button = await screen.findByRole("button", { name: "Start agent" });

    fireEvent.click(button);

    await waitFor(() => {
      expect(startAgent).toHaveBeenCalledTimes(1);
    });
    expect(
      await screen.findByRole("heading", { name: "SuperAgent" }),
    ).toBeInTheDocument();
    expect(getAgentSnapshot).toHaveBeenCalledTimes(1);
    expect(window.location.search).toBe("?flowId=flow-created");
  });

  it("restores manual tool recovery and submits one complete decision", async () => {
    vi.mocked(getAgentSnapshot)
      .mockResolvedValueOnce({
        ...snapshot,
        description: {
          ...activeDescription,
          status: AgentStatus.WAITING_FOR_TOOL_RECOVERY,
          pendingToolRecovery: {
            recoveryId: "recovery-2",
            calls: [
              {
                callId: "call-1",
                toolName: "local-tools.search",
                argumentsJson: '{"query":"Dex"}',
                errorType: "timeout",
              },
            ],
          },
        },
      })
      .mockResolvedValue(snapshot);
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    const heading = await screen.findByRole("heading", {
      name: "Execution outcome is unknown",
    });
    expect(heading).toHaveFocus();
    fireEvent.click(
      screen.getByRole("radio", { name: "Continue with unknown result" }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Apply recovery decisions" }),
    );

    await waitFor(() => {
      expect(resolveToolRecovery).toHaveBeenCalledWith(
        expect.objectContaining({
          body: {
            flowId: "flow-existing",
            recoveryId: "recovery-2",
            resolution: "resume",
            decisions: [{ callId: "call-1", action: "continue_with_unknown" }],
          },
        }),
      );
    });
  });

  it("steers a queued message by its stable Snapshot ID", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        pendingQueuedMessageCount: 1,
      },
      queued: [
        {
          messageId: "message-1",
          value: { content: "Please prioritize this", planMode: false },
        },
      ],
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    expect(
      await screen.findByRole("button", { name: /Message queue/ }),
    ).toHaveAttribute("aria-expanded", "true");
    const steer = await screen.findByRole("button", { name: "Steer now" });
    expect(steer).toHaveClass("steer-action");
    fireEvent.click(steer);

    await waitFor(() => {
      expect(steerQueuedMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          body: { flowId: "flow-existing", messageId: "message-1" },
        }),
      );
    });
  });

  it("does not duplicate the Snapshot when the window regains focus", async () => {
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);
    await screen.findByRole("heading", { name: "SuperAgent" });

    fireEvent.focus(window);

    await Promise.resolve();
    expect(getAgentSnapshot).toHaveBeenCalledTimes(1);
  });

  it("suspends live reads while hidden and resumes them when visible", async () => {
    const visibility = vi
      .spyOn(document, "visibilityState", "get")
      .mockReturnValue("hidden");
    try {
      window.history.replaceState({}, "", "/?flowId=flow-existing");
      render(<App />);
      await screen.findByRole("heading", { name: "SuperAgent" });

      expect(listRecentEvents).not.toHaveBeenCalled();
      expect(waitForWaitingInputRound).not.toHaveBeenCalled();

      visibility.mockReturnValue("visible");
      fireEvent(document, new Event("visibilitychange"));
      await waitFor(() => {
        expect(listRecentEvents).toHaveBeenCalledTimes(3);
        expect(waitForWaitingInputRound).toHaveBeenCalledTimes(1);
      });
    } finally {
      visibility.mockRestore();
    }
  });

  it("advances the waiting input watermark from each actual response", async () => {
    const firstWait = deferred<{ waitingInputRound: number }>();
    const observed: number[] = [];
    vi.mocked(waitForWaitingInputRound).mockImplementation(
      ({ query, signal }) => {
        observed.push(query.afterWaitingInputRound);
        if (observed.length === 1) return firstWait.promise;
        return pendingStream(signal);
      },
    );
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);
    await screen.findByRole("heading", { name: "SuperAgent" });
    await waitFor(() => {
      expect(observed).toEqual([1]);
    });

    firstWait.resolve({ waitingInputRound: 4 });

    await waitFor(() => {
      expect(observed).toEqual([1, 4]);
      expect(getAgentSnapshot).toHaveBeenCalledTimes(2);
    });
  });

  it("shows an optimistic queue item while message submission is pending", async () => {
    vi.mocked(sendMessage).mockImplementation(
      () => new Promise(() => undefined),
    );
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);
    const composer = await screen.findByRole("textbox", { name: "Message" });

    fireEvent.change(composer, { target: { value: "new work" } });
    const send = screen.getByRole("button", { name: "Send" });
    send.focus();
    fireEvent.click(send);

    const queueToggle = await screen.findByRole("button", {
      name: /Message queue/,
    });
    expect(queueToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Submitting…")).toBeInTheDocument();
    expect(screen.getByText("new work")).toBeInTheDocument();
    expect(composer).toHaveValue("");
    expect(composer).toBeEnabled();
    expect(composer).toHaveFocus();
    fireEvent.change(composer, { target: { value: "next message" } });
    expect(composer).toHaveValue("next message");
  });

  it("releases the idle durable wait before sending the first message", async () => {
    let isWaitAborted = false;
    let isWaitSettled = false;
    vi.mocked(waitForWaitingInputRound).mockImplementationOnce(
      ({ signal }) =>
        new Promise((_resolve, reject) => {
          signal?.addEventListener(
            "abort",
            () => {
              isWaitAborted = true;
              queueMicrotask(() => {
                isWaitSettled = true;
                reject(new DOMException("Aborted", "AbortError"));
              });
            },
            { once: true },
          );
        }),
    );
    vi.mocked(sendMessage).mockImplementationOnce(() => {
      expect(isWaitAborted).toBe(true);
      expect(isWaitSettled).toBe(true);
      return Promise.resolve({ accepted: true });
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);
    const composer = await screen.findByRole("textbox", { name: "Message" });
    await waitFor(() => {
      expect(waitForWaitingInputRound).toHaveBeenCalledTimes(1);
    });

    fireEvent.change(composer, { target: { value: "first" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => {
      expect(sendMessage).toHaveBeenCalledTimes(1);
    });
  });

  it("gates mutations until the post-command Snapshot succeeds", async () => {
    const command = deferred<{ accepted: true }>();
    const reconciliation = deferred<AgentSnapshot>();
    vi.mocked(sendMessage).mockReturnValueOnce(command.promise);
    vi.mocked(getAgentSnapshot)
      .mockResolvedValueOnce(snapshot)
      .mockReturnValueOnce(reconciliation.promise);
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);
    const composer = await screen.findByRole("textbox", { name: "Message" });

    fireEvent.change(composer, { target: { value: "first" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => {
      expect(sendMessage).toHaveBeenCalledTimes(1);
    });
    act(() => {
      command.resolve({ accepted: true });
    });

    expect(await screen.findByRole("status")).toHaveTextContent(
      "Syncing durable state…",
    );
    expect(sendMessage).toHaveBeenCalledTimes(1);
    fireEvent.change(composer, { target: { value: "draft while syncing" } });
    expect(sendMessage).toHaveBeenCalledTimes(1);
    expect(composer).toBeEnabled();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    fireEvent.keyDown(composer, { key: "Enter", ctrlKey: true });
    expect(sendMessage).toHaveBeenCalledTimes(1);
    act(() => {
      reconciliation.resolve(snapshot);
    });
    await waitFor(() => {
      expect(
        screen.queryByText("Syncing durable state…"),
      ).not.toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("restores a failed submission and keeps its accessible error after reconciliation", async () => {
    vi.mocked(sendMessage).mockRejectedValueOnce(new Error("send failed"));
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);
    const composer = await screen.findByRole("textbox", { name: "Message" });

    fireEvent.click(screen.getByRole("checkbox", { name: "Plan mode" }));
    fireEvent.change(composer, { target: { value: "recover this draft" } });
    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("send failed");
    expect(composer).toHaveValue("recover this draft");
    expect(screen.getByRole("checkbox", { name: "Plan mode" })).toBeChecked();
    await waitFor(() => {
      expect(getAgentSnapshot).toHaveBeenCalledTimes(2);
    });
    expect(screen.getByRole("alert")).toHaveTextContent("send failed");
  });

  it("moves unassociated stream history into one earlier activity block", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.CALLING_MODEL,
        waitingInputRound: 1,
      },
      history: {
        messages: [
          message(1, "user", "Start work", "2026-09-03T00:00:00Z"),
          message(2, "assistant", "Finished", "2026-09-03T00:04:00Z"),
        ],
        nextBeforeSequence: null,
      },
    });
    const activityEvents = [
      activityEvent(
        "activity-1",
        EventKind.MODEL_STARTED,
        "Calling mock/reliable.",
        "2026-09-03T00:01:00Z",
      ),
      activityEvent(
        "activity-2",
        EventKind.TOOL_PROGRESS,
        "Running local-tools.search.",
        "2026-09-03T00:02:00Z",
        "local-tools.search",
      ),
    ];
    let activityIndex = 0;
    let sentReasoning = false;
    vi.mocked(readEvent).mockImplementation(({ query, signal }) => {
      if (query.stream === EventStream.ACTIVITY) {
        const event = activityEvents[activityIndex];
        activityIndex++;
        if (event !== undefined) return Promise.resolve(event);
      }
      if (query.stream === EventStream.REASONING && !sentReasoning) {
        sentReasoning = true;
        return Promise.resolve({
          kind: "reasoning_summary",
          value: "Checked the available tools.",
          resumeToken: "reasoning-1",
          createdAt: "2026-09-03T00:03:00Z",
          source: "model-1",
        });
      }
      return pendingStream(signal);
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    const history = await screen.findByLabelText("Conversation history");
    await within(history).findByText("Running local-tools.search.");
    expect(
      within(history).getByText(/Historical activity · 3 events/),
    ).toBeInTheDocument();
    expect(
      within(history).getByText("Calling mock/reliable."),
    ).toBeInTheDocument();
    expect(
      within(history).getByText("Checked the available tools."),
    ).toBeInTheDocument();
    expect(within(history).getByText("Start work")).toBeInTheDocument();
    expect(within(history).getByText("Finished")).toBeInTheDocument();
    expect(screen.queryByLabelText("Agent activity")).not.toBeInTheDocument();
  });

  it("shows queued messages above the composer and streams Plan task progress", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.CALLING_MODEL,
        waitingInputRound: 1,
        pendingQueuedMessageCount: 1,
        plan: {
          revision: 4,
          status: PlanStatus.ACTIVE,
          tasks: [{ content: "Implement the UI", status: TaskStatus.PENDING }],
        },
      },
      queued: [
        {
          messageId: "queued-1",
          value: { content: "Follow up", planMode: false },
        },
      ],
    });
    let sentPlanEvent = false;
    vi.mocked(readEvent).mockImplementation(({ query, signal }) => {
      if (query.stream === EventStream.ACTIVITY && !sentPlanEvent) {
        sentPlanEvent = true;
        return Promise.resolve({
          ...activityEvent(
            "plan-task-1",
            EventKind.PLAN_TASK_UPDATED,
            "Started plan task 1.",
            "2026-09-03T00:01:00Z",
          ),
          value: {
            kind: EventKind.PLAN_TASK_UPDATED,
            message: "Started plan task 1.",
            callId: null,
            toolName: null,
            messageSequence: null,
            planBaseRevision: 4,
            planRevision: 5,
            planTaskIndex: 0,
            planTaskStatus: TaskStatus.IN_PROGRESS,
            inputConsumption: null,
          },
        });
      }
      return pendingStream(signal);
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    const composer = await screen.findByLabelText("Message composer");
    const queue = within(composer).getByLabelText("Message queue");
    const agentStatus = within(composer).getByRole("group", {
      name: "Agent status",
    });
    const sendButton = within(composer).getByRole("button", { name: "Send" });
    expect(agentStatus.parentElement).toHaveClass("composer-actions");
    expect(agentStatus.nextElementSibling).toBe(sendButton);
    const toggle = within(queue).getByRole("button", { name: /Message queue/ });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(within(queue).getByText("Follow up")).toBeInTheDocument();
    const plan = screen.getByLabelText("Agent plan");
    expect(plan.closest("aside")).not.toBeNull();
    expect(
      await within(plan).findByLabelText("In progress"),
    ).toBeInTheDocument();
    expect(within(plan).getByText("Implement the UI")).toBeInTheDocument();
    const blockedAction = within(plan).getByRole("button", {
      name: "Resolve queued messages",
    });
    expect(blockedAction).toBeDisabled();
    expect(blockedAction).toHaveAccessibleDescription(
      "The Agent must consume or remove queued messages before continuing this Plan.",
    );

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(within(queue).queryByText("Follow up")).not.toBeInTheDocument();
  });

  it("disables an active Plan action while the Agent is running", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.CALLING_MODEL,
        waitingInputRound: 1,
        plan: {
          revision: 7,
          status: PlanStatus.ACTIVE,
          tasks: [{ content: "Finish the work", status: TaskStatus.PENDING }],
        },
      },
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    const plan = await screen.findByLabelText("Agent plan");
    const action = within(plan).getByRole("button", {
      name: "Plan running…",
    });
    expect(action).toBeDisabled();
    expect(action).toHaveAccessibleDescription(
      "Continue becomes available if unfinished tasks remain at the next durable wait.",
    );
  });

  it("moves a consumed queued message into the conversation before Snapshot", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.CALLING_MODEL,
        pendingQueuedMessageCount: 1,
      },
      queued: [
        {
          messageId: "queued-1",
          value: { content: "Show this immediately", planMode: false },
        },
      ],
    });
    const consumed = deferred<StreamEvent>();
    let sentConsumption = false;
    vi.mocked(readEvent).mockImplementation(({ query, signal }) => {
      if (query.stream === EventStream.ACTIVITY && !sentConsumption) {
        sentConsumption = true;
        return consumed.promise;
      }
      return pendingStream(signal);
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    const queue = await screen.findByRole("region", { name: "Message queue" });
    expect(
      within(queue).getByText("Show this immediately"),
    ).toBeInTheDocument();
    act(() => {
      consumed.resolve({
        ...activityEvent(
          "input-consumed-1",
          EventKind.INPUT_CONSUMED,
          "Consumed 1 queued user message.",
          "2026-09-03T00:01:00Z",
        ),
        value: {
          kind: EventKind.INPUT_CONSUMED,
          message: "Consumed 1 queued user message.",
          callId: null,
          toolName: null,
          messageSequence: null,
          inputConsumption: {
            queuedMessageIds: ["queued-1"],
            steeredMessageIds: [],
            planExecutionRevision: null,
          },
        },
      });
    });

    const history = screen.getByRole("region", {
      name: "Conversation history",
    });
    expect(
      await within(history).findByText("Show this immediately"),
    ).toBeInTheDocument();
    expect(
      within(queue).queryByText("Show this immediately"),
    ).not.toBeInTheDocument();
    expect(getAgentSnapshot).toHaveBeenCalledTimes(1);
  });

  it("keeps the Plan boundary closed after its request is consumed", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        plan: {
          revision: 7,
          status: PlanStatus.ACTIVE,
          tasks: [{ content: "Finish the work", status: TaskStatus.PENDING }],
        },
        isPlanExecutionRequested: true,
      },
    });
    const consumed = deferred<StreamEvent>();
    let sentConsumption = false;
    vi.mocked(readEvent).mockImplementation(({ query, signal }) => {
      if (query.stream === EventStream.ACTIVITY && !sentConsumption) {
        sentConsumption = true;
        return consumed.promise;
      }
      return new Promise((_resolve, reject) => {
        signal?.addEventListener(
          "abort",
          () => {
            reject(new DOMException("Aborted", "AbortError"));
          },
          { once: true },
        );
      });
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    const plan = await screen.findByLabelText("Agent plan");
    expect(
      within(plan).getByRole("button", { name: "Execution requested" }),
    ).toBeDisabled();
    act(() => {
      consumed.resolve({
        ...activityEvent(
          "input-consumed-1",
          EventKind.INPUT_CONSUMED,
          "Consumed plan execution request for revision 7.",
          "2026-09-03T00:01:00Z",
        ),
        value: {
          kind: EventKind.INPUT_CONSUMED,
          message: "Consumed plan execution request for revision 7.",
          callId: null,
          toolName: null,
          messageSequence: null,
          inputConsumption: {
            queuedMessageIds: [],
            steeredMessageIds: [],
            planExecutionRevision: 7,
          },
        },
      });
    });

    await waitFor(() => {
      expect(
        within(plan).getByRole("button", { name: "Plan running…" }),
      ).toBeDisabled();
    });
    expect(
      screen.getByText("Consumed plan execution request for revision 7."),
    ).toBeInTheDocument();
    expect(getAgentSnapshot).toHaveBeenCalledTimes(1);
  });

  it("reconciles durable state after a live Snapshot request", async () => {
    const requested = deferred<StreamEvent>();
    let sentRequest = false;
    vi.mocked(readEvent).mockImplementation(({ query, signal }) => {
      if (query.stream === EventStream.ACTIVITY && !sentRequest) {
        sentRequest = true;
        return requested.promise;
      }
      return pendingStream(signal);
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    await screen.findByRole("heading", { name: "SuperAgent" });
    expect(getAgentSnapshot).toHaveBeenCalledTimes(1);
    act(() => {
      requested.resolve(
        activityEvent(
          "snapshot-required-1",
          EventKind.SNAPSHOT_REQUIRED,
          "Durable interaction state changed.",
          "2026-09-03T00:01:00Z",
        ),
      );
    });

    await waitFor(() => {
      expect(getAgentSnapshot).toHaveBeenCalledTimes(3);
    });
    expect(
      screen.queryByText("Durable interaction state changed."),
    ).not.toBeInTheDocument();
  });

  it.each([
    {
      name: "pending questions",
      label: "Answer questions first",
      reason: "Submit the requested answers before continuing this Plan.",
      patch: {
        pendingUserInput: {
          callId: "call-input",
          questions: [
            {
              id: "region",
              header: "Region",
              question: "Choose a region",
              options: [
                { label: "West", description: "Use West." },
                { label: "East", description: "Use East." },
              ],
            },
          ],
        },
      },
    },
    {
      name: "pending approval",
      label: "Resolve approval first",
      reason: "Approve or reject the pending tool before continuing this Plan.",
      patch: {
        pendingApproval: {
          callId: "call-approval",
          toolName: "fixture__echo",
          argumentsJson: "{}",
        },
      },
    },
    {
      name: "active timer",
      label: "Timer is active",
      reason:
        "The Plan can continue after the durable Timer finishes or is steered.",
      patch: {
        pendingTimer: {
          callId: "call-timer",
          durationSeconds: 30,
          reason: "wait for a dependency",
        },
      },
    },
  ] satisfies {
    name: string;
    label: string;
    reason: string;
    patch: Partial<AgentDescription>;
  }[])("explains the $name Plan blocker", async ({ label, reason, patch }) => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        ...patch,
        plan: {
          revision: 7,
          status: PlanStatus.ACTIVE,
          tasks: [{ content: "Finish the work", status: TaskStatus.PENDING }],
        },
      },
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    const plan = await screen.findByLabelText("Agent plan");
    const action = within(plan).getByRole("button", { name: label });
    expect(action).toBeDisabled();
    expect(action).toHaveAccessibleDescription(reason);
  });

  it("keeps a chosen answer local until the batch is submitted", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.WAITING_FOR_MESSAGE,
        pendingUserInput: {
          callId: "call-1",
          questions: [
            {
              id: "pace",
              header: "Pace",
              question: "Choose a pace",
              options: [
                { label: "Relaxed", description: "Take more time." },
                { label: "Fast", description: "Finish quickly." },
              ],
            },
          ],
        },
      },
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    fireEvent.click(await screen.findByRole("button", { name: /^Relaxed/u }));

    expect(screen.getByLabelText("Answered")).toBeInTheDocument();
    expect(
      screen.getByRole("textbox", { name: "Additional details for Pace" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Choose a pace")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Submit all" })).toBeEnabled();
    expect(answerQuestions).not.toHaveBeenCalled();
  });

  it("hides an answered input immediately after server acceptance", async () => {
    const pendingSnapshot: AgentSnapshot = {
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.WAITING_FOR_MESSAGE,
        pendingUserInput: {
          callId: "call-1",
          questions: [
            {
              id: "pace",
              header: "Pace",
              question: "Choose a pace",
              options: [
                { label: "Relaxed", description: "Take more time." },
                { label: "Fast", description: "Finish quickly." },
              ],
            },
          ],
        },
      },
    };
    const answeredSnapshot: AgentSnapshot = {
      ...snapshot,
      history: {
        messages: [
          {
            sequence: 1,
            message: {
              role: MessageRole.USER,
              content: "**Pace**: Relaxed",
              toolCalls: [],
              toolCallId: null,
              toolName: null,
              createdAt: "2026-09-03T00:00:01Z",
            },
          },
        ],
        nextBeforeSequence: null,
      },
      description: {
        ...activeDescription,
        status: AgentStatus.CALLING_MODEL,
        lastSequence: 1,
      },
    };
    vi.mocked(getAgentSnapshot)
      .mockResolvedValueOnce(pendingSnapshot)
      .mockResolvedValueOnce(answeredSnapshot);
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    fireEvent.click(await screen.findByRole("button", { name: /^Relaxed/u }));
    fireEvent.click(screen.getByRole("button", { name: "Submit all" }));

    await waitFor(() => {
      expect(answerQuestions).toHaveBeenCalledWith(
        expect.objectContaining({
          body: {
            flowId: "flow-existing",
            callId: "call-1",
            answers: [{ questionId: "pace", answer: "Relaxed" }],
          },
        }),
      );
    });
    await waitFor(() => {
      expect(screen.queryByText("Choose a pace")).not.toBeInTheDocument();
    });
    expect(
      screen.queryByRole("button", { name: "Submit all" }),
    ).not.toBeInTheDocument();
    await waitFor(() => {
      expect(getAgentSnapshot).toHaveBeenCalledTimes(2);
    });
    const history = screen.getByRole("region", {
      name: "Conversation history",
    });
    expect(within(history).getByText(/Pace.*Relaxed/)).toBeInTheDocument();
    expect(
      screen.queryByRole("region", { name: "Message queue" }),
    ).not.toBeInTheDocument();
  });

  it("shows an answered input when its Activity arrives before Snapshot", async () => {
    const pendingSnapshot: AgentSnapshot = {
      ...snapshot,
      description: {
        ...activeDescription,
        pendingUserInput: {
          callId: "call-1",
          questions: [
            {
              id: "pace",
              header: "Pace",
              question: "Choose a pace",
              options: [
                { label: "Relaxed", description: "Take more time." },
                { label: "Fast", description: "Finish quickly." },
              ],
            },
          ],
        },
      },
    };
    const acceptedSnapshot: AgentSnapshot = {
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.WAITING_FOR_MESSAGE,
        pendingUserInput: null,
      },
    };
    const durableSnapshot: AgentSnapshot = {
      ...snapshot,
      history: {
        messages: [
          message(
            1,
            MessageRole.USER,
            "**Pace**: Relaxed",
            "2026-09-03T00:00:02Z",
          ),
        ],
        nextBeforeSequence: null,
      },
      description: {
        ...activeDescription,
        status: AgentStatus.CALLING_MODEL,
        lastSequence: 1,
      },
    };
    const answered = deferred<StreamEvent>();
    const reconciled = deferred<AgentSnapshot>();
    let activityReads = 0;
    vi.mocked(getAgentSnapshot)
      .mockResolvedValueOnce(pendingSnapshot)
      .mockResolvedValueOnce(acceptedSnapshot)
      .mockReturnValueOnce(reconciled.promise);
    vi.mocked(readEvent).mockImplementation(({ query, signal }) => {
      if (query.stream === EventStream.ACTIVITY) {
        activityReads += 1;
        if (activityReads === 2) return answered.promise;
      }
      return pendingStream(signal);
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    fireEvent.click(await screen.findByRole("button", { name: /^Relaxed/u }));
    fireEvent.click(screen.getByRole("button", { name: "Submit all" }));
    await waitFor(() => {
      expect(getAgentSnapshot).toHaveBeenCalledTimes(2);
    });

    act(() => {
      answered.resolve({
        ...activityEvent(
          "answered-1",
          EventKind.USER_INPUT_ANSWERED,
          "Answered 1 question.",
          "2026-09-03T00:00:02Z",
        ),
        source: "await-user-1",
        value: {
          kind: EventKind.USER_INPUT_ANSWERED,
          message: "Answered 1 question.",
          callId: "call-1",
          toolName: null,
          messageSequence: 1,
          inputConsumption: null,
        },
      });
    });

    const history = screen.getByRole("region", {
      name: "Conversation history",
    });
    expect(
      await within(history).findByText(/Pace.*Relaxed/),
    ).toBeInTheDocument();
    expect(screen.getByText("Answered 1 question.")).toBeInTheDocument();
    expect(getAgentSnapshot).toHaveBeenCalledTimes(3);

    act(() => {
      reconciled.resolve(durableSnapshot);
    });
    await waitFor(() => {
      expect(within(history).getAllByText(/Pace.*Relaxed/)).toHaveLength(1);
    });
  });

  it("navigates, revises, and atomically submits three question answers", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        pendingUserInput: {
          callId: "call-three",
          questions: [
            question("region", "Region", "Which region?", "West", "East"),
            question("pace", "Pace", "Which pace?", "Fast", "Careful"),
            question("format", "Format", "Which format?", "Short", "Detailed"),
          ],
        },
      },
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    fireEvent.click(await screen.findByRole("button", { name: /^West/u }));
    fireEvent.change(
      screen.getByRole("textbox", { name: "Additional details for Region" }),
      { target: { value: "California departure" } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText("Which pace?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Fast/u }));
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText("Which format?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Other/u }));
    fireEvent.change(
      screen.getByRole("textbox", { name: "Other answer for Format" }),
      {
        target: { value: "Checklist" },
      },
    );
    fireEvent.click(screen.getByRole("button", { name: /^Pace/u }));
    fireEvent.click(screen.getByRole("button", { name: /^Careful/u }));
    fireEvent.click(screen.getByRole("button", { name: /^Format/u }));
    fireEvent.click(screen.getByRole("button", { name: "Submit all" }));

    await waitFor(() => {
      expect(answerQuestions).toHaveBeenCalledWith(
        expect.objectContaining({
          body: {
            flowId: "flow-existing",
            callId: "call-three",
            answers: [
              {
                questionId: "region",
                answer: "West: California departure",
              },
              { questionId: "pace", answer: "Careful" },
              { questionId: "format", answer: "Checklist" },
            ],
          },
        }),
      );
    });
  });

  it("renders assistant Markdown without exposing built-in tool records", async () => {
    vi.mocked(getPortal).mockResolvedValueOnce({
      ...portal,
      builtInTools: ["request_user_input"],
    });
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      history: {
        messages: [
          {
            sequence: 1,
            message: {
              role: "assistant",
              content: "**Durable reply**",
              toolCalls: [
                {
                  id: "call-1",
                  name: "request_user_input",
                  argumentsJson: '{"prompt":"Choose"}',
                },
              ],
              toolCallId: null,
              toolName: null,
              createdAt: "2026-09-03T00:00:00Z",
            },
          },
          {
            sequence: 2,
            message: {
              role: "tool",
              content: '{"status":"waiting_for_user"}',
              toolCalls: [],
              toolCallId: "call-1",
              toolName: "request_user_input",
              createdAt: "2026-09-03T00:00:01Z",
            },
          },
        ],
        nextBeforeSequence: null,
      },
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    const reply = await screen.findByText("Durable reply");
    expect(reply.tagName).toBe("STRONG");
    expect(screen.queryByText(/waiting_for_user/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Tool request/)).not.toBeInTheDocument();
  });
});

function pendingStream(signal: AbortSignal | null | undefined): Promise<never> {
  return new Promise((_resolve, reject) => {
    signal?.addEventListener(
      "abort",
      () => {
        reject(new DOMException("Aborted", "AbortError"));
      },
      { once: true },
    );
  });
}

function activityEvent(
  resumeToken: string,
  kind: EventKind,
  summary: string,
  createdAt: string,
  toolName: string | null = null,
) {
  return {
    kind: "activity" as const,
    value: {
      kind,
      message: summary,
      callId: null,
      toolName,
      messageSequence: null,
      inputConsumption: null,
    },
    resumeToken,
    createdAt,
    source: "model-1",
  };
}

function message(
  sequence: number,
  role: "user" | "assistant",
  content: string,
  createdAt: string,
) {
  return {
    sequence,
    message: {
      role,
      content,
      toolCalls: [],
      toolCallId: null,
      toolName: null,
      createdAt,
    },
  };
}

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

function deferred<T>(): Deferred<T> {
  let resolvePromise: (value: T) => void = () => undefined;
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve;
  });
  return { promise, resolve: resolvePromise };
}

function question(
  id: string,
  header: string,
  prompt: string,
  first: string,
  second: string,
) {
  return {
    id,
    header,
    question: prompt,
    options: [
      { label: first, description: `Choose ${first}.` },
      { label: second, description: `Choose ${second}.` },
    ],
  };
}

/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
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
  AgentInteractionStatus,
  AgentStatus,
  EventKind,
  EventStream,
  FlowStatus,
  PlanStatus,
  Provider,
  TaskStatus,
  getAgentSnapshot,
  getPortal,
  readEvent,
  sendMessage,
  startAgent,
  steerQueuedMessage,
  waitForAgentInteractionStatus,
  type AgentSnapshot,
  type AgentDescription,
  type Portal,
} from "./api/generated";
import type * as GeneratedAPI from "./api/generated";

vi.mock("./api/generated", async (importOriginal) => {
  const generated = await importOriginal<typeof GeneratedAPI>();
  return {
    ...generated,
    approveTool: vi.fn(),
    deleteQueuedMessage: vi.fn(),
    executePlan: vi.fn(),
    getAgentSnapshot: vi.fn(),
    getPortal: vi.fn(),
    readEvent: vi.fn(),
    sendMessage: vi.fn(),
    startAgent: vi.fn(),
    steerQueuedMessage: vi.fn(),
    waitForAgentInteractionStatus: vi.fn(),
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
  interactionStatus: AgentInteractionStatus.WAITING,
  model: "mock/reliable",
  systemPrompt: "Be helpful.",
  firstRetainedSequence: 1,
  lastSequence: 0,
  summarizedThroughSequence: 0,
  pendingApproval: null,
  pendingTimer: null,
  pendingUserInput: null,
  plan: null,
  isPlanExecutionRequested: false,
  pendingQueuedMessageCount: 0,
  pendingSteeredMessageCount: 0,
  availableMcpServers: ["local-tools"],
  availableTools: ["local-tools.search"],
};

const snapshot: AgentSnapshot = {
  runId: "run-1",
  flowStatus: FlowStatus.RUNNING,
  errorType: null,
  errorMessage: null,
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
    vi.mocked(waitForAgentInteractionStatus).mockImplementation(
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
    expect(readEvent).toHaveBeenCalledTimes(3);
    expect(startAgent).not.toHaveBeenCalled();
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

    const steer = await screen.findByRole("button", { name: "Steer" });
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

  it("shows a terminal Flow result without opening live subscriptions", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      runId: "run-terminal",
      flowStatus: FlowStatus.TERMINATED,
      errorType: null,
      errorMessage: "stopped by operator",
      history: { messages: [], nextBeforeSequence: null },
      description: null,
      queued: [],
      steered: [],
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Agent Terminated" }),
    ).toBeInTheDocument();
    expect(screen.getByText("stopped by operator")).toBeInTheDocument();
    expect(readEvent).not.toHaveBeenCalled();
  });

  it("shows an optimistic queue item while message submission is pending", async () => {
    vi.mocked(sendMessage).mockImplementation(
      () => new Promise(() => undefined),
    );
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);
    const composer = await screen.findByRole("textbox", { name: "Message" });

    fireEvent.change(composer, { target: { value: "new work" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByText("Submitting…")).toBeInTheDocument();
    expect(screen.getByText("new work")).toBeInTheDocument();
    expect(composer).toHaveValue("");
  });

  it("renders every Activity event inside the chronological conversation", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
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
    const content = history.textContent;
    expect(content.indexOf("Start work")).toBeLessThan(
      content.indexOf("Calling mock/reliable."),
    );
    expect(content.indexOf("Calling mock/reliable.")).toBeLessThan(
      content.indexOf("Running local-tools.search."),
    );
    expect(content.indexOf("Running local-tools.search.")).toBeLessThan(
      content.indexOf("Checked the available tools."),
    );
    expect(content.indexOf("Checked the available tools.")).toBeLessThan(
      content.indexOf("Finished"),
    );
    expect(screen.queryByLabelText("Agent activity")).not.toBeInTheDocument();
  });

  it("shows queued messages above the composer and streams Plan task progress", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.CALLING_MODEL,
        interactionStatus: AgentInteractionStatus.SUBMITTED,
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
          },
        });
      }
      return pendingStream(signal);
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    const composer = await screen.findByLabelText("Message composer");
    const queue = within(composer).getByLabelText("Message queue");
    expect(within(queue).getByText("Follow up")).toBeInTheDocument();
    const plan = screen.getByLabelText("Agent plan");
    expect(plan.closest("aside")).not.toBeNull();
    expect(
      await within(plan).findByLabelText("In progress"),
    ).toBeInTheDocument();
    expect(within(plan).getByText("Implement the UI")).toBeInTheDocument();

    const toggle = within(queue).getByRole("button", { name: /Message queue/ });
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(within(queue).queryByText("Follow up")).not.toBeInTheDocument();
  });

  it("fills the composer from a durable input choice", async () => {
    vi.mocked(getAgentSnapshot).mockResolvedValueOnce({
      ...snapshot,
      description: {
        ...activeDescription,
        status: AgentStatus.WAITING_FOR_MESSAGE,
        pendingUserInput: {
          callId: "call-1",
          prompt: "Choose a pace",
          choices: ["Relaxed", "Fast"],
        },
      },
    });
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);

    fireEvent.click(await screen.findByRole("button", { name: "Relaxed" }));

    expect(
      screen.getByRole("textbox", { name: "Answer Agent question" }),
    ).toHaveValue("Relaxed");
    expect(screen.getByRole("button", { name: "Submit answer" })).toBeEnabled();
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

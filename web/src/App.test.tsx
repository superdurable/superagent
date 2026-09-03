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
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "./App";
import {
  AgentStatus,
  FlowStatus,
  Provider,
  getAgentSnapshot,
  getPortal,
  readEvent,
  sendMessage,
  startAgent,
  steerQueuedMessage,
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

    expect(screen.getByText("Loading Superagent")).toBeInTheDocument();
    expect(await screen.findByText("Start a Superagent")).toBeInTheDocument();
    expect(getPortal).toHaveBeenCalledTimes(1);
    expect(startAgent).not.toHaveBeenCalled();
  });

  it("atomically loads one Snapshot when resuming a Flow", async () => {
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Superagent" }),
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
      await screen.findByRole("heading", { name: "Superagent" }),
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

  it("reconciles the durable Snapshot when the window regains focus", async () => {
    window.history.replaceState({}, "", "/?flowId=flow-existing");
    render(<App />);
    await screen.findByRole("heading", { name: "Superagent" });

    fireEvent.focus(window);

    await waitFor(() => {
      expect(getAgentSnapshot).toHaveBeenCalledTimes(2);
    });
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
});

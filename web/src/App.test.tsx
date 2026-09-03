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
import { Provider, type Portal } from "./api/generated";
import { getPortal, startAgent } from "./api/generated";
import type * as GeneratedAPI from "./api/generated";

vi.mock("./api/generated", async (importOriginal) => {
  const generated = await importOriginal<typeof GeneratedAPI>();
  return {
    ...generated,
    getPortal: vi.fn(),
    startAgent: vi.fn(),
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

describe("App", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.history.replaceState({}, "", "/");
    vi.mocked(getPortal).mockResolvedValue(portal);
    vi.mocked(startAgent).mockResolvedValue({ flowId: "flow-created" });
  });

  afterEach(cleanup);

  it("loads the generated portal contract", async () => {
    render(<App />);

    expect(screen.getByText("Loading Superagent")).toBeInTheDocument();
    expect(await screen.findByText("Start a Superagent")).toBeInTheDocument();
    expect(getPortal).toHaveBeenCalledTimes(1);
    expect(startAgent).not.toHaveBeenCalled();
  });

  it("stops at the Snapshot gate when resuming a Flow", async () => {
    window.history.replaceState({}, "", "/?flowId=flow-existing");

    render(<App />);

    expect(
      await screen.findByText("Waiting for Dex Snapshot API"),
    ).toBeInTheDocument();
    expect(screen.getByText("flow-existing")).toBeInTheDocument();
    expect(getPortal).toHaveBeenCalledTimes(1);
    expect(startAgent).not.toHaveBeenCalled();
  });

  it("starts through the generated client and enters the Snapshot gate", async () => {
    render(<App />);
    const button = await screen.findByRole("button", { name: "Start agent" });

    fireEvent.click(button);

    await waitFor(() => {
      expect(startAgent).toHaveBeenCalledTimes(1);
    });
    expect(
      await screen.findByText("Waiting for Dex Snapshot API"),
    ).toBeInTheDocument();
    expect(window.location.search).toBe("?flowId=flow-created");
  });
});

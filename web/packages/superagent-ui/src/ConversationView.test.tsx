/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ConversationView,
  type ConversationViewProps,
} from "./ConversationView.js";

const base = Date.parse("2026-09-23T12:00:00Z");

afterEach(() => {
  vi.useRealTimers();
});

describe("ConversationView", () => {
  it("summarizes successful and failed operations in a collapsed work log", () => {
    const { container } = render(
      <ConversationView
        messages={toolTurn([
          { id: "success", name: "read_file", content: "done" },
          {
            id: "failure",
            name: "apply_patch",
            content: '{"isError":true}',
          },
        ])}
      />,
    );

    expect(container).toHaveTextContent("3 operations");
    expect(container).toHaveTextContent("2 succeeded");
    expect(container).toHaveTextContent("1 failed");
    expect(screen.getByText("Work log").closest("details")).not.toHaveAttribute(
      "open",
    );
  });

  it("keeps a zero failure count visible for successful work", () => {
    const { container } = render(
      <ConversationView
        messages={toolTurn([
          { id: "success", name: "read_file", content: "done" },
        ])}
      />,
    );

    expect(container).toHaveTextContent("2 succeeded");
    expect(container).toHaveTextContent("0 failed");
  });

  it("updates a running tool duration without announcing every tick", () => {
    vi.useFakeTimers();
    vi.setSystemTime(base + 2_000);
    const { container } = render(
      <ConversationView
        messages={[
          {
            sequence: 1,
            message: {
              role: "user",
              content: "run it",
              toolCalls: [],
              toolCallId: null,
              toolName: null,
              createdAt: new Date(base).toISOString(),
            },
          },
          {
            sequence: 2,
            message: {
              role: "assistant",
              content: "",
              toolCalls: [
                {
                  id: "call-1",
                  name: "exec_long_command",
                  argumentsJson: "{}",
                },
              ],
              toolCallId: null,
              toolName: null,
              createdAt: new Date(base + 500).toISOString(),
            },
          },
        ]}
        activities={[
          {
            resumeToken: "tool-started",
            source: "tool-call-1",
            createdAt: new Date(base).toISOString(),
            value: {
              kind: "model_tool_call",
              message: "Calling exec_long_command.",
              callId: "call-1",
              toolName: "exec_long_command",
              messageSequence: 2,
              attempt: 1,
            },
          },
        ]}
      />,
    );

    toggleWorkLog(true);
    expect(container).toHaveTextContent("2s");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    act(() => {
      vi.advanceTimersByTime(2_000);
    });
    expect(container).toHaveTextContent("4s");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("keeps shared attempt, status, and duration metadata with a custom tool slot", () => {
    const { container } = render(
      <ConversationView
        messages={[
          {
            sequence: 1,
            message: {
              role: "user",
              content: "run it",
              toolCalls: [],
              toolCallId: null,
              toolName: null,
              createdAt: new Date(base).toISOString(),
            },
          },
          {
            sequence: 2,
            message: {
              role: "assistant",
              content: "",
              toolCalls: [
                {
                  id: "call-1",
                  name: "read_file",
                  argumentsJson: "{}",
                },
              ],
              toolCallId: null,
              toolName: null,
              createdAt: new Date(base + 500).toISOString(),
            },
          },
          {
            sequence: 3,
            message: {
              role: "tool",
              content: "done",
              toolCalls: [],
              toolCallId: "call-1",
              toolName: "read_file",
              startedAt: new Date(base + 1_000).toISOString(),
              createdAt: new Date(base + 13_000).toISOString(),
            },
          },
        ]}
        activities={[
          {
            resumeToken: "attempt-2",
            source: "tool-call-1",
            createdAt: new Date(base + 2_000).toISOString(),
            value: {
              kind: "tool_progress",
              message: "Retrying read_file.",
              callId: "call-1",
              toolName: "read_file",
              messageSequence: 2,
              attempt: 2,
            },
          },
        ]}
        renderToolCall={() => <div>custom tool body</div>}
      />,
    );

    expect(container).not.toHaveTextContent("custom tool body");
    toggleWorkLog(true);
    expect(container).toHaveTextContent("read_file");
    expect(container).toHaveTextContent("2 attempts");
    expect(container).toHaveTextContent("complete");
    expect(container).toHaveTextContent("12s");
    expect(container).toHaveTextContent("custom tool body");
    toggleWorkLog(false);
    expect(container).not.toHaveTextContent("custom tool body");
  });

  it("does not run an elapsed clock for terminal unknown work", () => {
    vi.useFakeTimers();
    vi.setSystemTime(base + 2_000);
    const { container } = render(
      <ConversationView
        isExecutionLive={false}
        messages={[
          {
            sequence: 1,
            message: {
              role: "user",
              content: "run",
              toolCalls: [],
              toolCallId: null,
              toolName: null,
              createdAt: new Date(base).toISOString(),
            },
          },
          {
            sequence: 2,
            message: {
              role: "assistant",
              content: "",
              toolCalls: [
                { id: "lost", name: "read_file", argumentsJson: "{}" },
              ],
              toolCallId: null,
              toolName: null,
              createdAt: new Date(base + 500).toISOString(),
            },
          },
        ]}
      />,
    );
    toggleWorkLog(true);
    expect(container).toHaveTextContent("unknown");
    act(() => {
      vi.advanceTimersByTime(10_000);
    });
    expect(container).not.toHaveTextContent("10s");
  });
});

function toggleWorkLog(open: boolean) {
  const details = screen.getByText("Work log").closest("details");
  if (details === null) throw new Error("work log details missing");
  details.open = open;
  fireEvent(details, new Event("toggle"));
}

function toolTurn(
  tools: readonly { id: string; name: string; content: string }[],
): ConversationViewProps["messages"] {
  return [
    {
      sequence: 1,
      message: {
        role: "user",
        content: "run tools",
        toolCalls: [],
        toolCallId: null,
        toolName: null,
        createdAt: new Date(base).toISOString(),
      },
    },
    {
      sequence: 2,
      message: {
        role: "assistant",
        content: "",
        toolCalls: tools.map(({ id, name }) => ({
          id,
          name,
          argumentsJson: "{}",
        })),
        toolCallId: null,
        toolName: null,
        createdAt: new Date(base + 500).toISOString(),
      },
    },
    ...tools.map(({ id, name, content }, index) => ({
      sequence: index + 3,
      message: {
        role: "tool" as const,
        content,
        toolCalls: [],
        toolCallId: id,
        toolName: name,
        createdAt: new Date(base + 1_000 + index * 500).toISOString(),
      },
    })),
  ];
}

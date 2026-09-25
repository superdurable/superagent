/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from "vitest";
import {
  buildConversationPresentation,
  formatDuration,
  toolCallFingerprint,
  unionDuration,
} from "./conversationPresentation.js";
import type {
  TimelineActivityEntry,
  TimelineSequencedMessage,
} from "./conversationTimeline.js";

const base = Date.parse("2026-09-23T12:00:00Z");
const iso = (offset: number) => new Date(base + offset).toISOString();

function required<T>(value: T | undefined): T {
  if (value === undefined) throw new Error("expected test value");
  return value;
}

function message(
  sequence: number,
  role: "user" | "assistant" | "tool",
  content: string,
  options: Partial<TimelineSequencedMessage["message"]> = {},
): TimelineSequencedMessage {
  return {
    sequence,
    message: {
      role,
      content,
      toolCalls: [],
      toolCallId: null,
      toolName: null,
      createdAt: iso(sequence * 1_000),
      ...options,
    },
  };
}

describe("conversation presentation", () => {
  it("builds model and tool work from durable messages", () => {
    const messages = [
      message(1, "user", "change it"),
      message(2, "assistant", "", {
        startedAt: iso(1_100),
        createdAt: iso(2_000),
        toolCalls: [
          { id: "call-1", name: "read_file", argumentsJson: '{"b":2,"a":1}' },
        ],
      }),
      message(3, "tool", "done", {
        startedAt: iso(2_100),
        createdAt: iso(4_100),
        toolCallId: "call-1",
        toolName: "read_file",
      }),
      message(4, "assistant", "finished", {
        startedAt: iso(4_200),
        createdAt: iso(5_000),
      }),
    ];
    const view = buildConversationPresentation({ messages }, base + 6_000);
    expect(view.turns).toHaveLength(1);
    const turn = required(view.turns[0]);
    expect(turn.models).toHaveLength(2);
    expect(required(required(turn.tools[0]).calls[0]).durationMs).toBe(2_000);
    expect(turn.messages.map((entry) => entry.message.role)).toEqual([
      "user",
      "assistant",
    ]);
  });

  it("aggregates progress and retries by call id", () => {
    const activities: TimelineActivityEntry[] = [
      {
        resumeToken: "started",
        source: "tool-source",
        createdAt: iso(1_000),
        value: {
          kind: "model_tool_call",
          message: "started",
          callId: "call-1",
          toolName: "exec_long_command",
          messageSequence: 2,
          attempt: 1,
        },
      },
      ...Array.from({ length: 111 }, (_, index) => ({
        resumeToken: `r-${String(index)}`,
        source: "tool-source",
        createdAt: iso(index),
        value: {
          kind: "tool_progress",
          message: "working",
          callId: "call-1",
          toolName: "exec_long_command",
          messageSequence: 2,
          attempt: index === 110 ? 3 : 1,
        },
      })),
    ];
    const messages = [
      message(1, "user", "run"),
      message(2, "assistant", "", {
        toolCalls: [
          {
            id: "call-1",
            name: "exec_long_command",
            argumentsJson: '{"command":"make"}',
          },
        ],
      }),
    ];
    const turnValue = buildConversationPresentation(
      { messages, activities },
      base + 120_000,
    ).turns[0];
    const turn = required(turnValue);
    const item = required(required(turn.tools[0]).calls[0]);
    expect(turn.models).toHaveLength(1);
    expect(item.progressCount).toBe(111);
    expect(item.attemptCount).toBe(3);
    expect(item.status).toBe("running");
    expect(item.durationMs).toBe(119_000);
  });

  it("attaches an in-flight or failed next model invocation to the current turn", () => {
    const messages = [message(1, "user", "continue")];
    const started: TimelineActivityEntry = {
      resumeToken: "model-started",
      source: "model-next",
      createdAt: iso(2_000),
      value: {
        kind: "model_started",
        message: "Calling model.",
        messageSequence: 2,
        attempt: 1,
      },
    };
    const running = buildConversationPresentation(
      { messages, activities: [started], isModelRunning: true },
      base + 5_000,
    );
    const runningTurn = required(running.turns[0]);
    expect(running.earlierActivity).toHaveLength(0);
    expect(required(runningTurn.models[0]).status).toBe("running");
    expect(required(runningTurn.models[0]).durationMs).toBe(3_000);
    expect(runningTurn.durationMs).toBe(4_000);

    const failed = buildConversationPresentation(
      {
        messages,
        activities: [
          started,
          {
            resumeToken: "model-failed",
            source: "model-next",
            createdAt: iso(4_000),
            value: {
              kind: "model_failed",
              message: "Model failed.",
              messageSequence: 2,
              attempt: 1,
            },
          },
        ],
      },
      base + 5_000,
    );
    expect(required(required(failed.turns[0]).models[0]).status).toBe("failed");
    expect(required(required(failed.turns[0]).models[0]).durationMs).toBe(
      2_000,
    );
  });

  it("places historical activity in its associated or nearest turn", () => {
    const messages = [
      message(1, "user", "first"),
      message(2, "assistant", "first answer"),
      message(10, "user", "second"),
      message(11, "assistant", "second answer"),
    ];
    const activities: TimelineActivityEntry[] = [
      {
        resumeToken: "associated",
        source: "activity",
        createdAt: iso(3_000),
        value: {
          kind: "compacted",
          message: "Compacted through message 2.",
          messageSequence: 2,
        },
      },
      {
        resumeToken: "legacy",
        source: "activity",
        createdAt: iso(9_000),
        value: {
          kind: "compacted",
          message: "Legacy compaction event.",
          messageSequence: null,
        },
      },
    ];

    const view = buildConversationPresentation({ messages, activities });

    expect(view.earlierActivity).toHaveLength(0);
    expect(required(view.turns[0]).historicalActivities).toEqual([
      activities[0],
    ]);
    expect(required(view.turns[1]).historicalActivities).toEqual([
      activities[1],
    ]);
  });

  it("associates input consumption with its durable user turn", () => {
    const messages = [
      message(1, "user", "first"),
      message(2, "assistant", "first answer"),
      message(3, "user", "second"),
    ];
    const activity: TimelineActivityEntry = {
      resumeToken: "input-consumed",
      source: "await-user",
      createdAt: iso(3_000),
      value: {
        kind: "input_consumed",
        message: "Consumed 1 queued user message.",
        messageSequence: 3,
      },
    };

    const view = buildConversationPresentation({
      messages,
      activities: [
        {
          ...activity,
          resumeToken: "legacy-input-consumed",
          value: { ...activity.value, messageSequence: null },
        },
        activity,
      ],
    });

    expect(view.earlierActivity).toHaveLength(0);
    expect(required(view.turns[1]).activities).toContain(activity);
  });

  it("keeps live unanchored activity in the current user turn", () => {
    const messages = [
      message(1, "user", "first"),
      message(2, "assistant", "first answer"),
      message(3, "user", "continue"),
    ];
    const toolActivity: TimelineActivityEntry = {
      resumeToken: "live-tool",
      source: "tool-source",
      createdAt: iso(4_000),
      value: {
        kind: "model_tool_call",
        message: "Calling read_file.",
        callId: "not-durable-yet",
        toolName: "read_file",
        messageSequence: 5,
      },
    };
    const modelActivity: TimelineActivityEntry = {
      resumeToken: "live-model",
      source: "model-source",
      createdAt: iso(5_000),
      value: {
        kind: "model_started",
        message: "Calling model.",
        messageSequence: 6,
      },
    };

    const view = buildConversationPresentation({
      messages,
      activities: [toolActivity, modelActivity],
      isModelRunning: true,
      isExecutionLive: true,
    });

    expect(view.earlierActivity).toHaveLength(0);
    expect(required(view.turns[1]).activities).toContain(toolActivity);
    expect(required(view.turns[1]).liveActivities).toEqual([toolActivity]);
    expect(required(view.turns[1]).models).toHaveLength(1);
  });

  it("leaves unanchored terminal activity in historical activity", () => {
    const messages = [
      message(1, "user", "first"),
      message(2, "assistant", "done"),
    ];
    const activity: TimelineActivityEntry = {
      resumeToken: "unanchored-terminal",
      source: "tool-source",
      createdAt: iso(3_000),
      value: {
        kind: "model_tool_call",
        message: "Calling read_file.",
        callId: "missing",
        toolName: "read_file",
        messageSequence: 4,
      },
    };

    const view = buildConversationPresentation({
      messages,
      activities: [activity],
      isExecutionLive: false,
    });

    expect(view.earlierActivity).toEqual([activity]);
  });

  it("keeps current and resolved waits separate from tool execution", () => {
    const messages = [
      message(1, "user", "build"),
      message(2, "assistant", "waiting", {
        toolCalls: [
          { id: "call-1", name: "exec_short_command", argumentsJson: "{}" },
        ],
      }),
    ];
    const current = buildConversationPresentation(
      {
        messages,
        pendingWaits: [
          { kind: "approval", callId: "call-1", startedAt: iso(5_000) },
        ],
        activities: [
          {
            resumeToken: "requested",
            source: "approval",
            createdAt: iso(5_000),
            value: {
              kind: "tool_approval_requested",
              message: "requested",
              callId: "call-1",
              toolName: "exec_short_command",
              messageSequence: 2,
            },
          },
        ],
      },
      base + 17_000,
    ).turns[0];
    const currentTurn = required(current);
    expect(required(required(currentTurn.tools[0]).calls[0]).status).toBe(
      "waiting",
    );
    expect(
      required(required(currentTurn.tools[0]).calls[0]).durationMs,
    ).toBeNull();
    expect(currentTurn.waitDurationMs).toBe(12_000);
    expect(currentTurn.workDurationMs).toBeNull();
    expect(currentTurn.durationMs).toBe(16_000);

    const resolved = buildConversationPresentation(
      {
        messages,
        activities: [
          {
            resumeToken: "requested",
            source: "approval",
            createdAt: iso(5_000),
            value: {
              kind: "tool_approval_requested",
              message: "requested",
              callId: "call-1",
              toolName: "exec_short_command",
              messageSequence: 2,
            },
          },
          {
            resumeToken: "resolved",
            source: "approval",
            createdAt: iso(17_000),
            value: {
              kind: "tool_approval_resolved",
              message: "approved",
              callId: "call-1",
              toolName: "exec_short_command",
              messageSequence: 2,
            },
          },
        ],
      },
      base + 20_000,
    ).turns[0];
    expect(required(resolved).waitDurationMs).toBe(12_000);
  });

  it("groups semantically identical repeated calls", () => {
    const messages = [
      message(1, "user", "read"),
      message(2, "assistant", "", {
        toolCalls: [
          {
            id: "a",
            name: "read_file",
            argumentsJson: '{"path":"x","options":{"b":2,"a":1}}',
          },
          {
            id: "b",
            name: "read_file",
            argumentsJson: '{"options":{"a":1,"b":2},"path":"x"}',
          },
        ],
      }),
    ];
    const groups = required(
      buildConversationPresentation({ messages }).turns[0],
    ).tools;
    expect(groups).toHaveLength(1);
    const group = required(groups[0]);
    expect(group.calls).toHaveLength(2);
    expect(toolCallFingerprint(required(group.calls[0]).call)).toBe(
      toolCallFingerprint(required(group.calls[1]).call),
    );
  });

  it("keeps model and tool work in causal order", () => {
    const messages = [
      message(1, "user", "inspect"),
      message(2, "assistant", "", {
        toolCalls: [{ id: "a", name: "read_file", argumentsJson: "{}" }],
      }),
      message(3, "tool", "ok", { toolCallId: "a", toolName: "read_file" }),
      message(4, "assistant", "", {
        toolCalls: [{ id: "b", name: "search_files", argumentsJson: "{}" }],
      }),
      message(5, "tool", "ok", { toolCallId: "b", toolName: "search_files" }),
      message(6, "assistant", "done"),
    ];
    const turn = required(buildConversationPresentation({ messages }).turns[0]);
    expect(turn.operations.map((operation) => operation.kind)).toEqual([
      "model",
      "tool",
      "model",
      "tool",
      "model",
    ]);
    expect(turn.models.map((model) => model.summary)).toEqual([
      "Model requested read_file",
      "Model requested search_files",
      "Model replied",
    ]);
  });

  it("projects durable questions and their exact user answers", () => {
    const messages = [
      message(1, "user", "start"),
      message(2, "assistant", "", {
        toolCalls: [
          {
            id: "question-1",
            name: "request_user_input",
            argumentsJson: JSON.stringify({
              questions: [
                {
                  id: "scope",
                  header: "Scope",
                  question: "Full fix?",
                  options: [
                    { label: "Yes", description: "Implement everything" },
                  ],
                },
              ],
            }),
          },
        ],
      }),
      message(3, "user", "**Scope**: Yes", {
        answeredInputCallId: "question-1",
      }),
    ];
    const presentation = buildConversationPresentation({ messages });
    const question = required(required(presentation.turns[0]).questions[0]);
    expect(question.status).toBe("answered");
    expect(question.questions[0]?.question).toBe("Full fix?");
    expect(question.answer?.sequence).toBe(3);
    expect(required(presentation.turns[0]).tools).toHaveLength(0);
  });

  it("marks a rejected input request as failed history", () => {
    const messages = [
      message(1, "user", "start"),
      message(2, "assistant", "", {
        toolCalls: [
          {
            id: "question-failed",
            name: "request_user_input",
            argumentsJson: JSON.stringify({
              questions: [
                {
                  id: "scope",
                  header: "A header rejected by the runtime",
                  question: "Full fix?",
                  options: [
                    { label: "Yes", description: "Implement everything" },
                    { label: "No", description: "Stop here" },
                  ],
                },
              ],
            }),
          },
        ],
      }),
      message(3, "tool", '{"status":"failed","error":"invalid_user_input"}', {
        toolCallId: "question-failed",
        toolName: "request_user_input",
      }),
    ];

    const question = required(
      required(buildConversationPresentation({ messages }).turns[0])
        .questions[0],
    );
    expect(question.status).toBe("failed");
    expect(question.questions[0]?.header).toBe(
      "A header rejected by the runtime",
    );
  });

  it("marks legacy rejected input without a tool result as failed", () => {
    const messages = [
      message(1, "user", "start"),
      message(2, "assistant", "", {
        toolCalls: [
          {
            id: "question-rejected-before-execution",
            name: "request_user_input",
            argumentsJson: JSON.stringify({
              questions: [
                {
                  id: "scope",
                  header: "Scope",
                  question: "Full fix?",
                  options: [],
                },
              ],
            }),
          },
        ],
      }),
      message(3, "assistant", "Please clarify the scope."),
    ];

    const question = required(
      required(buildConversationPresentation({ messages }).turns[0])
        .questions[0],
    );
    expect(question.status).toBe("failed");
  });

  it("formats durations and unions parallel work safely", () => {
    expect(formatDuration(500)).toBe("<1s");
    expect(formatDuration(12_000)).toBe("12s");
    expect(formatDuration(68_000)).toBe("1m 08s");
    expect(formatDuration(3_720_000)).toBe("1h 02m");
    expect(formatDuration(-1)).toBeNull();
    expect(
      unionDuration([
        [0, 10],
        [5, 20],
        [30, 40],
      ]),
    ).toBe(30);
  });
});

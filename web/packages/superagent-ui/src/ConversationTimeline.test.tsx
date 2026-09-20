/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ConversationTimeline } from "./ConversationTimeline";
import { ToolCallCard } from "./ToolCallCard";

describe("ConversationTimeline", () => {
  it("renders entries in chronological order and honors custom activity slot", () => {
    render(
      <ConversationTimeline
        messages={[
          {
            sequence: 1,
            message: {
              role: "user",
              content: "hi",
              toolCalls: [],
              toolCallId: null,
              toolName: null,
              createdAt: "2026-09-03T00:00:00Z",
            },
          },
          {
            sequence: 2,
            message: {
              role: "assistant",
              content: "hello",
              toolCalls: [],
              toolCallId: null,
              toolName: null,
              createdAt: "2026-09-03T00:02:00Z",
            },
          },
        ]}
        activities={[
          {
            resumeToken: "a1",
            source: "model",
            createdAt: "2026-09-03T00:01:00Z",
            value: {
              kind: "model_started",
              message: "started",
              messageSequence: null,
            },
          },
        ]}
        renderMessage={(entry) => (
          <div>{`message:${String(entry.sequence)}`}</div>
        )}
        renderActivity={() => <div>custom-activity</div>}
        renderReasoning={() => null}
        renderAssistant={() => null}
      />,
    );
    const items = screen.getAllByText(/message:|custom-activity/);
    expect(items.map((item) => item.textContent)).toEqual([
      "message:1",
      "custom-activity",
      "message:2",
    ]);
  });
});

describe("ToolCallCard", () => {
  it("collapses apply_patch by default and shows diff when open", () => {
    const { container } = render(
      <ToolCallCard
        call={{
          id: "c1",
          name: "apply_patch",
          argumentsJson: JSON.stringify({
            patch: `--- a/demo.ts
+++ b/demo.ts
@@ -1 +1 @@
-old
+new
`,
          }),
        }}
        result={{
          content: JSON.stringify({ changed_paths: ["demo.ts"] }),
          toolName: "apply_patch",
          sequence: 2,
          createdAt: "2026-09-03T00:00:01Z",
        }}
      />,
    );
    const details = container.querySelector("details");
    expect(details?.open).toBe(false);
    expect(screen.getByText(/Edited `demo.ts`/)).toBeInTheDocument();
  });

  it("renders exec command and grey output", () => {
    render(
      <ToolCallCard
        defaultOpen
        call={{
          id: "c2",
          name: "exec_short_command",
          argumentsJson: JSON.stringify({ argv: ["git", "status"] }),
        }}
        result={{
          content: JSON.stringify({
            stdout: { head: "\u001b[32mclean\u001b[39m", omitted_bytes: 0 },
            stderr: { head: "", omitted_bytes: 0 },
          }),
          toolName: "exec_short_command",
          sequence: 3,
          createdAt: "2026-09-03T00:00:02Z",
        }}
      />,
    );
    expect(screen.getByText("Ran git")).toBeInTheDocument();
    expect(screen.getByText("clean")).toBeInTheDocument();
  });
});

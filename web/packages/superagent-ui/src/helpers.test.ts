/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from "vitest";

import { parsePatchDiff, summarizePatchFiles } from "./parsePatchDiff";
import {
  formatShellCommand,
  projectCommandOutput,
  stripAnsi,
} from "./toolPayload";
import { planActionPresentation } from "./planAction";
import {
  mergeActivityEvent,
  mergeSequencedMessages,
} from "./viewStateMerge";

describe("parsePatchDiff", () => {
  it("parses unified diffs with add and remove lines", () => {
    const files = parsePatchDiff(`--- a/demo.ts
+++ b/demo.ts
@@ -1,3 +1,3 @@
 context
-old
+new
`);
    expect(files).not.toBeNull();
    expect(files?.[0]?.path).toBe("demo.ts");
    expect(files?.[0]?.added).toBe(1);
    expect(files?.[0]?.removed).toBe(1);
    expect(summarizePatchFiles(files ?? [])).toContain("+1 -1");
  });

  it("parses Codex update patches", () => {
    const files = parsePatchDiff(`*** Begin Patch
*** Update File: flow.json
@@
- "supervision"
+ "v2"
*** End Patch`);
    expect(files?.[0]?.path).toBe("flow.json");
    expect(files?.[0]?.lines.some((line) => line.kind === "remove")).toBe(true);
    expect(files?.[0]?.lines.some((line) => line.kind === "add")).toBe(true);
  });
});

describe("toolPayload", () => {
  it("strips ANSI and projects command stdout", () => {
    expect(stripAnsi("\u001b[31mred\u001b[39m")).toBe("red");
    expect(formatShellCommand(["bash", "-lc", "echo hi"])).toContain("bash");
    expect(
      projectCommandOutput(
        JSON.stringify({
          stdout: { head: "\u001b[32mok\u001b[39m", tail: "", omitted_bytes: 0 },
          stderr: { head: "", tail: "", omitted_bytes: 0 },
        }),
      ),
    ).toBe("ok");
  });
});

describe("planActionPresentation", () => {
  it("gates execute behind questions then recovery then approval", () => {
    const base = {
      planStatus: "active",
      isExecutePlanPending: false,
      isPlanExecutionRequested: false,
      areMutationsDisabled: false,
      hasPendingUserInput: false,
      hasPendingApproval: false,
      hasPendingToolRecovery: false,
      hasPendingTimer: false,
      hasPendingQueue: false,
      isWaitingForInput: true,
      isWaitingForMessage: true,
    } as const;
    expect(planActionPresentation({ ...base, hasPendingUserInput: true }).label).toBe(
      "Answer questions first",
    );
    expect(
      planActionPresentation({ ...base, hasPendingToolRecovery: true }).label,
    ).toBe("Resolve tool recovery");
    expect(planActionPresentation({ ...base, hasPendingApproval: true }).label).toBe(
      "Resolve approval first",
    );
    expect(planActionPresentation(base).label).toBe("Continue plan");
  });
});

describe("viewStateMerge", () => {
  it("merges sequenced messages and dedupes activities", () => {
    expect(
      mergeSequencedMessages(
        [{ sequence: 1, message: { content: "a" } }],
        [{ sequence: 1, message: { content: "b" } }],
      )[0]?.message,
    ).toEqual({ content: "b" });
    expect(
      mergeActivityEvent(
        [{ resumeToken: "one" }],
        { resumeToken: "one" },
        10,
        true,
      ),
    ).toHaveLength(1);
  });
});

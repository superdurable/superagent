/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import {
  ToolRecoveryPanel,
  type PendingToolRecovery,
} from "./ToolRecoveryPanel";

const recovery: PendingToolRecovery = {
  recoveryId: "recovery-3",
  calls: [
    {
      callId: "call-a",
      toolName: "files__read",
      argumentsJson: '{"path":"a.txt"}',
      errorType: "timeout",
    },
    {
      callId: "call-b",
      toolName: "files__search",
      argumentsJson: '{"query":"Dex"}',
      errorType: "connection_error",
    },
  ],
};

describe("ToolRecoveryPanel", () => {
  it("focuses the warning and submits one ordered complete decision", () => {
    const onResolve = vi.fn();
    render(<ToolRecoveryPanel onResolve={onResolve} recovery={recovery} />);

    expect(
      screen.getByRole("heading", { name: "Execution outcome is unknown" }),
    ).toHaveFocus();
    const continueOptions = screen.getAllByRole("radio", {
      name: "Continue with unknown result",
    });
    const secondContinueOption = continueOptions.at(1);
    if (secondContinueOption === undefined) {
      throw new Error("second recovery option is missing");
    }
    fireEvent.click(secondContinueOption);
    fireEvent.click(
      screen.getByRole("button", { name: "Apply recovery decisions" }),
    );

    expect(onResolve).toHaveBeenCalledWith("recovery-3", {
      resolution: "resume",
      decisions: [
        { callId: "call-a", action: "retry" },
        { callId: "call-b", action: "continue_with_unknown" },
      ],
    });
  });

  it("submits stop without per-call decisions", () => {
    const onResolve = vi.fn();
    render(<ToolRecoveryPanel onResolve={onResolve} recovery={recovery} />);

    fireEvent.click(
      screen.getByRole("button", { name: "Stop current tool sequence" }),
    );

    expect(onResolve).toHaveBeenCalledWith("recovery-3", {
      resolution: "stop",
      decisions: [],
    });
  });

  it("disables every decision while reconciliation is pending", () => {
    const onResolve = vi.fn();
    render(
      <ToolRecoveryPanel
        disabled
        isSubmitting
        onResolve={onResolve}
        recovery={recovery}
      />,
    );

    for (const radio of screen.getAllByRole("radio")) {
      expect(radio).toBeDisabled();
    }
    expect(screen.getByRole("button", { name: "Submitting…" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Stop current tool sequence" }),
    ).toBeDisabled();
    expect(onResolve).not.toHaveBeenCalled();
  });
});

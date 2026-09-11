/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import {
  PendingMessageQueue,
  type PendingMessageQueueItem,
} from "./PendingMessageQueue";

const items: readonly PendingMessageQueueItem[] = [
  {
    id: "steered-1",
    kind: "steered",
    label: "Steering",
    modeLabel: "Chat",
    content: "Do this first",
  },
  {
    id: "queued-1",
    kind: "queued",
    label: "Plan",
    content: "Then plan this",
    actions: ["steer", "edit", "delete"],
  },
  {
    id: "local-1",
    kind: "submitting",
    label: "Submitting…",
    modeLabel: "Chat",
    content: "Still sending",
  },
];

describe("PendingMessageQueue", () => {
  it("renders ordered queue states and reports semantic actions", () => {
    const onAction = vi.fn();
    const { container } = render(
      <PendingMessageQueue items={items} onAction={onAction} />,
    );

    expect(screen.getByText("2 queued · 1 steering")).toBeInTheDocument();
    const messages = container.querySelectorAll(".queue-message");
    expect(messages).toHaveLength(3);
    expect(messages[0]).toHaveClass("steered", "sa-queue-message--steered");
    expect(messages[2]).toHaveClass(
      "submitting",
      "sa-queue-message--submitting",
    );
    fireEvent.click(screen.getByRole("button", { name: /Steer now/ }));
    expect(onAction).toHaveBeenCalledWith("queued-1", "steer");
  });

  it("collapses until queue identity changes, then reveals new work", () => {
    const { rerender } = render(
      <PendingMessageQueue items={items} onAction={vi.fn()} />,
    );
    const toggle = screen.getByRole("button", { name: /Message queue/ });
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Do this first")).not.toBeInTheDocument();

    rerender(
      <PendingMessageQueue
        items={[
          ...items,
          {
            id: "queued-2",
            kind: "queued",
            label: "Chat",
            content: "New work",
            actions: ["steer"],
          },
        ]}
        onAction={vi.fn()}
      />,
    );
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("New work")).toBeInTheDocument();
  });

  it("disables mutations and shows a pending action without leaking state", () => {
    render(
      <PendingMessageQueue
        disabled
        items={items}
        onAction={vi.fn()}
        pendingItemID="queued-1"
      />,
    );
    const updates = screen.getAllByRole("button", { name: "Updating…" });
    expect(updates).toHaveLength(3);
    for (const update of updates) expect(update).toBeDisabled();
  });
});

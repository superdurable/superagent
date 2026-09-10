/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";

import { ConversationComposer } from "./ConversationComposer";

describe("ConversationComposer", () => {
  it("preserves controlled input, focus, and modifier-enter submission", () => {
    const onChange = vi.fn();
    const onSubmit = vi.fn();
    const textareaRef = createRef<HTMLTextAreaElement>();
    render(
      <ConversationComposer
        onChange={onChange}
        onSubmit={onSubmit}
        textareaRef={textareaRef}
        value="Build it"
      />,
    );

    const input = screen.getByRole("textbox", { name: "Message" });
    textareaRef.current?.focus();
    expect(input).toHaveFocus();
    fireEvent.change(input, { target: { value: "Build this" } });
    expect(onChange).toHaveBeenCalledWith("Build this");

    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSubmit).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Enter", ctrlKey: true });
    fireEvent.keyDown(input, { key: "Enter", metaKey: true });
    fireEvent.keyDown(input, { key: "Enter", altKey: true });
    expect(onSubmit).toHaveBeenCalledTimes(3);
  });

  it("supports enter-to-send and keeps shift-enter for new lines", () => {
    const onSubmit = vi.fn();
    render(
      <ConversationComposer
        onChange={vi.fn()}
        onSubmit={onSubmit}
        shortcut="enter"
        value="Build it"
      />,
    );
    const input = screen.getByRole("textbox", { name: "Message" });

    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    expect(onSubmit).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSubmit).toHaveBeenCalledOnce();
  });

  it("separates input and submit loading states and rejects blank submit", () => {
    const { rerender } = render(
      <ConversationComposer
        inputDisabled
        onChange={vi.fn()}
        onSubmit={vi.fn()}
        submitDisabled={false}
        value="Ready"
      />,
    );

    expect(screen.getByRole("textbox")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
    rerender(
      <ConversationComposer
        onChange={vi.fn()}
        onSubmit={vi.fn()}
        value="   "
      />,
    );
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });
});

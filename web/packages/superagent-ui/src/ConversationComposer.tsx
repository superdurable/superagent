/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import type { KeyboardEvent, Ref } from "react";

export type ConversationSubmitShortcut = "modifier-enter" | "enter";

export interface ConversationComposerProps {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  inputDisabled?: boolean;
  submitDisabled?: boolean;
  placeholder?: string;
  submitLabel?: string;
  shortcut?: ConversationSubmitShortcut;
  rows?: number;
  textareaRef?: Ref<HTMLTextAreaElement>;
  ariaLabel?: string;
}

export function ConversationComposer({
  value,
  onChange,
  onSubmit,
  inputDisabled = false,
  submitDisabled = false,
  placeholder = "Message the Agent…",
  submitLabel = "Send",
  shortcut = "modifier-enter",
  rows = 3,
  textareaRef,
  ariaLabel = "Message",
}: ConversationComposerProps) {
  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (!isSubmitShortcut(event, shortcut)) return;
    event.preventDefault();
    onSubmit();
  };

  return (
    <div className="sa-conversation-composer composer-row">
      <textarea
        ref={textareaRef}
        aria-label={ariaLabel}
        value={value}
        disabled={inputDisabled}
        placeholder={placeholder}
        rows={rows}
        onChange={(event) => {
          onChange(event.target.value);
        }}
        onKeyDown={handleKeyDown}
      />
      <button
        type="button"
        className="sa-conversation-composer-submit"
        disabled={submitDisabled || value.trim() === ""}
        onClick={onSubmit}
      >
        {submitLabel}
      </button>
    </div>
  );
}

function isSubmitShortcut(
  event: KeyboardEvent<HTMLTextAreaElement>,
  shortcut: ConversationSubmitShortcut,
): boolean {
  if (event.key !== "Enter") return false;
  if (shortcut === "enter") return !event.shiftKey;
  return event.metaKey || event.ctrlKey || event.altKey;
}

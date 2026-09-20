/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

export function activityIcon(kind: string): string {
  switch (kind) {
    case "plan_started":
    case "plan_updated":
    case "plan_task_updated":
      return "☷";
    case "input_consumed":
      return "⇥";
    case "user_input_answered":
      return "✓";
    case "steering_applied":
      return "↪";
    case "model_started":
    case "model_failed":
    case "model_completed":
      return "✦";
    case "model_tool_call":
    case "tool_progress":
    case "tool_failed":
    case "tool_recovery_required":
    case "tool_recovery_resolved":
    case "tool_completed":
      return "⚙";
    case "user_input_requested":
      return "?";
    case "compaction_failed":
    case "compacted":
    case "snapshot_required":
      return "↻";
    default:
      return "•";
  }
}

export function activityLabel(kind: string, toolName: string | null): string {
  const label = kind
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
  return toolName === null || toolName === ""
    ? label
    : `${label} · ${toolName}`;
}

export function formatActivityTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat(undefined, {
        hour: "numeric",
        minute: "2-digit",
      }).format(date);
}

export interface ActivityRowProps {
  kind: string;
  message: string;
  createdAt: string;
  toolName?: string | null;
  className?: string;
}

export function ActivityRow({
  kind,
  message,
  createdAt,
  toolName = null,
  className = "sa-activity-row",
}: ActivityRowProps) {
  return (
    <article className={`${className} ${kind}`}>
      <span className="sa-activity-icon" aria-hidden="true">
        {activityIcon(kind)}
      </span>
      <div className="sa-activity-body">
        <strong>{activityLabel(kind, toolName)}</strong>
        <span>{message}</span>
      </div>
      <time dateTime={createdAt}>{formatActivityTime(createdAt)}</time>
    </article>
  );
}

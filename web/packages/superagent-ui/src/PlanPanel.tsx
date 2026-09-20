/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import { useState } from "react";

import {
  planActionPresentation,
  type PlanActionGates,
  type PlanStatusValue,
  type PlanTaskStatusValue,
} from "./planAction.js";

export interface PlanPanelTask {
  content: string;
  status: PlanTaskStatusValue;
}

export interface PlanPanelProps {
  revision: number;
  status: PlanStatusValue;
  tasks: readonly PlanPanelTask[];
  taskStatuses?: readonly PlanTaskStatusValue[];
  gates: PlanActionGates;
  onExecute: (revision: number) => void;
  className?: string;
}

export function PlanPanel({
  revision,
  status,
  tasks,
  taskStatuses,
  gates,
  onExecute,
  className = "sa-plan-panel",
}: PlanPanelProps) {
  const [isExpanded, setIsExpanded] = useState(false);
  const statuses =
    taskStatuses ?? tasks.map((task) => task.status);
  const completedCount = statuses.filter(
    (taskStatus) => taskStatus === "completed",
  ).length;
  const hasRunningTask = statuses.some(
    (taskStatus) => taskStatus === "in_progress",
  );
  const action = planActionPresentation({ ...gates, planStatus: status });
  return (
    <section className={className} aria-label="Agent plan">
      <button
        type="button"
        className="sa-plan-toggle"
        aria-expanded={isExpanded}
        onClick={() => {
          setIsExpanded((value) => !value);
        }}
      >
        <span>
          Plan · {String(completedCount)}/{String(tasks.length)} complete
        </span>
        {hasRunningTask && (
          <span className="sa-plan-running" aria-hidden="true">
            ●
          </span>
        )}
        <span aria-hidden="true">{isExpanded ? "▴" : "▾"}</span>
      </button>
      {isExpanded && (
        <div className="sa-plan-content">
          <div className="sa-plan-heading">
            <div>
              <p className="sa-plan-eyebrow">Plan revision {revision}</p>
              <h2>{statusLabel(status)}</h2>
            </div>
            {status !== "completed" && (
              <button
                type="button"
                disabled={action.isDisabled}
                onClick={() => {
                  onExecute(revision);
                }}
              >
                {action.label}
              </button>
            )}
          </div>
          {action.reason !== null && status !== "completed" && (
            <p className="sa-plan-action-reason">{action.reason}</p>
          )}
          <ol className="sa-plan-tasks">
            {tasks.map((task, index) => {
              const taskStatus = statuses[index] ?? task.status;
              return (
                <li
                  className={taskStatus}
                  key={`${String(index)}:${task.content}`}
                >
                  <span aria-hidden="true">{taskIcon(taskStatus)}</span>
                  <div>
                    <strong>{statusLabel(taskStatus)}</strong>
                    <p>{task.content}</p>
                  </div>
                </li>
              );
            })}
          </ol>
        </div>
      )}
    </section>
  );
}

function statusLabel(value: string): string {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function taskIcon(status: PlanTaskStatusValue): string {
  switch (status) {
    case "completed":
      return "✓";
    case "in_progress":
      return "●";
    case "pending":
      return "○";
  }
}

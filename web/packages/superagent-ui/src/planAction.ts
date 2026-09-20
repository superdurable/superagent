/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

export type PlanTaskStatusValue = "pending" | "in_progress" | "completed";
export type PlanStatusValue = string;

export interface PlanActionGates {
  planStatus: PlanStatusValue;
  isExecutePlanPending: boolean;
  isPlanExecutionRequested: boolean;
  areMutationsDisabled: boolean;
  hasPendingUserInput: boolean;
  hasPendingApproval: boolean;
  hasPendingToolRecovery: boolean;
  hasPendingTimer: boolean;
  hasPendingQueue: boolean;
  isWaitingForInput: boolean;
  isWaitingForMessage: boolean;
}

export interface PlanActionPresentation {
  label: string;
  isDisabled: boolean;
  reason: string | null;
}

export function planActionPresentation(
  gates: PlanActionGates,
): PlanActionPresentation {
  if (gates.planStatus === "completed") {
    return { label: "Plan completed", isDisabled: true, reason: null };
  }
  if (gates.isExecutePlanPending) {
    return {
      label: "Requesting execution…",
      isDisabled: true,
      reason: "Waiting for the execution request to finish.",
    };
  }
  if (gates.isPlanExecutionRequested) {
    return {
      label: "Execution requested",
      isDisabled: true,
      reason: "The Agent will start this Plan from its durable wait.",
    };
  }
  if (gates.areMutationsDisabled) {
    return {
      label: "Syncing plan…",
      isDisabled: true,
      reason: "Waiting for the current durable state reconciliation.",
    };
  }
  if (gates.hasPendingUserInput) {
    return {
      label: "Answer questions first",
      isDisabled: true,
      reason: "Submit the requested answers before continuing this Plan.",
    };
  }
  if (gates.hasPendingToolRecovery) {
    return {
      label: "Resolve tool recovery",
      isDisabled: true,
      reason: "Resolve the unknown tool outcomes before continuing this Plan.",
    };
  }
  if (gates.hasPendingApproval) {
    return {
      label: "Resolve approval first",
      isDisabled: true,
      reason: "Approve or reject the pending tool before continuing this Plan.",
    };
  }
  if (gates.hasPendingTimer) {
    return {
      label: "Timer is active",
      isDisabled: true,
      reason:
        "The Plan can continue after the durable Timer finishes or is steered.",
    };
  }
  if (gates.hasPendingQueue) {
    return {
      label: "Resolve queued messages",
      isDisabled: true,
      reason:
        "The Agent must consume or remove queued messages before continuing this Plan.",
    };
  }
  if (!gates.isWaitingForInput || !gates.isWaitingForMessage) {
    const isDraft = gates.planStatus === "draft";
    return {
      label: isDraft ? "Preparing plan…" : "Plan running…",
      isDisabled: true,
      reason: isDraft
        ? "Execute becomes available after the Agent reaches its next durable wait."
        : "Continue becomes available if unfinished tasks remain at the next durable wait.",
    };
  }
  return {
    label: gates.planStatus === "draft" ? "Execute plan" : "Continue plan",
    isDisabled: false,
    reason: null,
  };
}

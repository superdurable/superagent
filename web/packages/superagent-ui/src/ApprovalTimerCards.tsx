/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

export interface ApprovalCardProps {
  toolName: string;
  argumentsJson: string;
  disabled?: boolean;
  isSubmitting?: boolean;
  onApprove: () => void;
  onReject: () => void;
  className?: string;
}

export function ApprovalCard({
  toolName,
  argumentsJson,
  disabled = false,
  isSubmitting = false,
  onApprove,
  onReject,
  className = "sa-approval-card",
}: ApprovalCardProps) {
  const label = isSubmitting ? "Processing…" : null;
  return (
    <section className={className}>
      <p className="sa-card-eyebrow">Approval required</p>
      <h2>{toolName}</h2>
      <pre>{argumentsJson}</pre>
      <div className="sa-button-row">
        <button type="button" disabled={disabled} onClick={onApprove}>
          {label ?? "Approve"}
        </button>
        <button
          type="button"
          className="sa-danger-button"
          disabled={disabled}
          onClick={onReject}
        >
          {label ?? "Reject"}
        </button>
      </div>
    </section>
  );
}

export interface TimerCardProps {
  durationSeconds: number;
  reason: string;
  className?: string;
}

export function TimerCard({
  durationSeconds,
  reason,
  className = "sa-timer-card",
}: TimerCardProps) {
  return (
    <section className={className}>
      <p className="sa-card-eyebrow">Durable timer</p>
      <h2>{durationSeconds}s</h2>
      <p>{reason}</p>
      <small>Steering interrupts this wait at a safe boundary.</small>
    </section>
  );
}

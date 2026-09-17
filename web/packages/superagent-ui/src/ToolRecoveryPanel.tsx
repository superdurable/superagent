/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import { useEffect, useId, useRef, useState, type SyntheticEvent } from "react";

export type ToolRecoveryAction = "retry" | "continue_with_unknown";

export interface ToolRecoveryCall {
  callId: string;
  toolName: string;
  argumentsJson: string;
  errorType: string;
}

export interface PendingToolRecovery {
  recoveryId: string;
  calls: readonly ToolRecoveryCall[];
}

export interface ToolRecoveryDecision {
  callId: string;
  action: ToolRecoveryAction;
}

export type ToolRecoveryResolution =
  | {
      resolution: "resume";
      decisions: readonly ToolRecoveryDecision[];
    }
  | {
      resolution: "stop";
      decisions: readonly [];
    };

export interface ToolRecoveryPanelProps {
  recovery: PendingToolRecovery;
  disabled?: boolean;
  isSubmitting?: boolean;
  onResolve: (recoveryId: string, resolution: ToolRecoveryResolution) => void;
}

export function ToolRecoveryPanel({
  recovery,
  disabled = false,
  isSubmitting = false,
  onResolve,
}: ToolRecoveryPanelProps) {
  const headingRef = useRef<HTMLHeadingElement>(null);
  const headingID = useId();
  const [selections, setSelections] = useState<
    Record<string, ToolRecoveryAction>
  >(() => initialSelections(recovery.calls));
  const isDisabled = disabled || isSubmitting;

  useEffect(() => {
    headingRef.current?.focus();
  }, []);

  const submitResume = () => {
    if (isDisabled) return;
    onResolve(recovery.recoveryId, {
      resolution: "resume",
      decisions: recovery.calls.map((call) => ({
        callId: call.callId,
        action: selections[call.callId] ?? "retry",
      })),
    });
  };

  return (
    <section
      className="sa-tool-recovery recovery-card"
      aria-labelledby={headingID}
    >
      <p className="sa-tool-recovery-eyebrow">Tool recovery required</p>
      <h2 id={headingID} ref={headingRef} tabIndex={-1}>
        Execution outcome is unknown
      </h2>
      <p className="sa-tool-recovery-warning">
        These operations may already have produced external effects. Retrying
        keeps the same call ID, but the MCP server may not deduplicate it.
      </p>
      <div className="sa-tool-recovery-calls">
        {recovery.calls.map((call) => (
          <fieldset className="sa-tool-recovery-call" key={call.callId}>
            <legend>{call.toolName}</legend>
            <small>Error type: {call.errorType}</small>
            <details onToggle={revealOpenedDetails}>
              <summary>Arguments</summary>
              <pre>{call.argumentsJson}</pre>
            </details>
            <label>
              <input
                type="radio"
                name={`recovery-${call.callId}`}
                checked={selections[call.callId] === "retry"}
                disabled={isDisabled}
                onChange={() => {
                  setSelections((current) => ({
                    ...current,
                    [call.callId]: "retry",
                  }));
                }}
              />{" "}
              Retry
            </label>
            <label>
              <input
                type="radio"
                name={`recovery-${call.callId}`}
                checked={selections[call.callId] === "continue_with_unknown"}
                disabled={isDisabled}
                onChange={() => {
                  setSelections((current) => ({
                    ...current,
                    [call.callId]: "continue_with_unknown",
                  }));
                }}
              />{" "}
              Continue with unknown result
            </label>
          </fieldset>
        ))}
      </div>
      <div className="sa-tool-recovery-actions">
        <button type="button" disabled={isDisabled} onClick={submitResume}>
          {isSubmitting ? "Submitting…" : "Apply recovery decisions"}
        </button>
        <button
          type="button"
          className="sa-tool-recovery-stop danger-button"
          disabled={isDisabled}
          onClick={() => {
            onResolve(recovery.recoveryId, {
              resolution: "stop",
              decisions: [],
            });
          }}
        >
          Stop current tool sequence
        </button>
      </div>
    </section>
  );
}

function revealOpenedDetails(event: SyntheticEvent<HTMLDetailsElement>) {
  const details = event.currentTarget;
  if (!details.open) return;
  window.requestAnimationFrame(() => {
    details.scrollIntoView({ block: "nearest" });
  });
}

function initialSelections(
  calls: readonly ToolRecoveryCall[],
): Record<string, ToolRecoveryAction> {
  const selections: Record<string, ToolRecoveryAction> = {};
  for (const call of calls) selections[call.callId] = "retry";
  return selections;
}

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
  const panelRef = useRef<HTMLElement>(null);
  const headingID = useId();
  const [selections, setSelections] = useState<
    Record<string, ToolRecoveryAction>
  >(() => initialSelections(recovery.calls));
  const isDisabled = disabled || isSubmitting;

  useEffect(() => {
    panelRef.current?.focus();
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
      ref={panelRef}
      className="sa-tool-recovery recovery-card"
      aria-labelledby={headingID}
      tabIndex={-1}
    >
      <p className="sa-tool-recovery-eyebrow">Tool recovery required</p>
      <h2 id={headingID}>Execution outcome is unknown</h2>
      <p className="sa-tool-recovery-warning">
        These operations may already have produced external effects. Retrying
        keeps the same call ID, but the MCP server may not deduplicate it.
      </p>
      <div className="sa-tool-recovery-calls">
        {recovery.calls.map((call, index) => {
          const callHeadingID = `${headingID}-call-${String(index)}`;
          const retryInputID = `${callHeadingID}-retry`;
          const continueInputID = `${callHeadingID}-continue`;
          const isRetrySelected = selections[call.callId] === "retry";
          return (
            <article className="sa-tool-recovery-call" key={call.callId}>
              <div className="sa-tool-recovery-call-header">
                <h3 id={callHeadingID}>{call.toolName}</h3>
                <span className="sa-tool-recovery-error">
                  Error · {call.errorType}
                </span>
              </div>
              <details
                className="sa-tool-recovery-arguments"
                onToggle={revealOpenedDetails}
              >
                <summary>
                  <span>Arguments</span>
                  <span aria-hidden="true" className="sa-tool-recovery-chevron">
                    ›
                  </span>
                </summary>
                <pre>{formatArguments(call.argumentsJson)}</pre>
              </details>
              <div
                aria-labelledby={callHeadingID}
                className="sa-tool-recovery-choices"
                role="radiogroup"
              >
                <label
                  className={`sa-tool-recovery-choice${isRetrySelected ? " is-selected" : ""}`}
                  htmlFor={retryInputID}
                >
                  <input
                    id={retryInputID}
                    type="radio"
                    name={`recovery-${call.callId}`}
                    checked={isRetrySelected}
                    disabled={isDisabled}
                    onChange={() => {
                      setSelections((current) => ({
                        ...current,
                        [call.callId]: "retry",
                      }));
                    }}
                  />
                  <strong>Retry</strong>
                  <small>Run the same call again</small>
                </label>
                <label
                  className={`sa-tool-recovery-choice${isRetrySelected ? "" : " is-selected"}`}
                  htmlFor={continueInputID}
                >
                  <input
                    id={continueInputID}
                    type="radio"
                    name={`recovery-${call.callId}`}
                    checked={!isRetrySelected}
                    disabled={isDisabled}
                    onChange={() => {
                      setSelections((current) => ({
                        ...current,
                        [call.callId]: "continue_with_unknown",
                      }));
                    }}
                  />
                  <strong>Continue with unknown result</strong>
                  <small>Assume the call may have completed</small>
                </label>
              </div>
            </article>
          );
        })}
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

function formatArguments(argumentsJson: string): string {
  try {
    return JSON.stringify(JSON.parse(argumentsJson) as unknown, null, 2);
  } catch {
    return argumentsJson;
  }
}

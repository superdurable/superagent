/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import { useState } from "react";

export type PendingMessageAction = "steer" | "edit" | "delete";

interface PendingMessageQueueItemBase {
  id: string;
  content: string;
  label: string;
  modeLabel?: string;
}

export type PendingMessageQueueItem =
  | (PendingMessageQueueItemBase & {
      kind: "queued";
      actions: readonly PendingMessageAction[];
    })
  | (PendingMessageQueueItemBase & {
      kind: "steered" | "submitting";
    });

export interface PendingMessageQueueProps {
  items: readonly PendingMessageQueueItem[];
  disabled?: boolean;
  pendingItemID?: string | null;
  onAction: (itemID: string, action: PendingMessageAction) => void;
  id?: string;
}

export function PendingMessageQueue({
  items,
  disabled = false,
  pendingItemID = null,
  onAction,
  id = "message-queue",
}: PendingMessageQueueProps) {
  const [isExpanded, setIsExpanded] = useState(true);
  if (items.length === 0) return null;

  const queuedCount = items.filter((item) => item.kind !== "steered").length;
  const steeringCount = items.length - queuedCount;
  const itemsID = `${id}-items`;
  return (
    <section className="sa-message-queue queue-tray" aria-label="Message queue">
      <button
        type="button"
        className="sa-message-queue-toggle queue-toggle"
        aria-controls={itemsID}
        aria-expanded={isExpanded}
        onClick={() => {
          setIsExpanded((current) => !current);
        }}
      >
        <span>Message queue</span>
        <strong>
          {String(queuedCount)} queued · {String(steeringCount)} steering
        </strong>
        <span aria-hidden="true">{isExpanded ? "▴" : "▾"}</span>
      </button>
      {isExpanded && (
        <div className="sa-message-queue-items queue-items" id={itemsID}>
          {items.map((item) => (
            <QueueItem
              disabled={disabled}
              item={item}
              isPending={pendingItemID === item.id}
              key={`${item.kind}:${item.id}`}
              onAction={onAction}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function QueueItem({
  item,
  disabled,
  isPending,
  onAction,
}: {
  item: PendingMessageQueueItem;
  disabled: boolean;
  isPending: boolean;
  onAction: PendingMessageQueueProps["onAction"];
}) {
  const stateClass = item.kind === "queued" ? "" : ` ${item.kind}`;
  return (
    <div
      className={`sa-queue-message sa-queue-message--${item.kind} queue-message${stateClass}`}
    >
      <div className="sa-queue-message-meta">
        <strong>{item.label}</strong>
        {item.modeLabel !== undefined && <small>{item.modeLabel}</small>}
      </div>
      <p>{item.content}</p>
      {item.kind === "queued" && (
        <div className="sa-queue-actions queue-actions">
          {item.actions.map((action) => (
            <button
              type="button"
              className={
                action === "steer"
                  ? "sa-queue-action sa-queue-action--steer queue-action steer-action"
                  : "sa-queue-action sa-queue-action--text queue-action text-button"
              }
              disabled={disabled}
              key={action}
              onClick={() => {
                onAction(item.id, action);
              }}
            >
              {isPending ? (
                "Updating…"
              ) : action === "steer" ? (
                <>
                  <span aria-hidden="true">↪</span>
                  Steer now
                </>
              ) : (
                actionLabel(action)
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function actionLabel(action: PendingMessageAction): string {
  return action.charAt(0).toUpperCase() + action.slice(1);
}

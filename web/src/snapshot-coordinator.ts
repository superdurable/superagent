/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import type { AgentSnapshot } from "./api/generated";
import type { ActiveConnectionState } from "./conversation-state";

export const snapshotFreshnessMilliseconds = 10_000;

export interface SnapshotTrigger {
  blocking: boolean;
  connection?: ActiveConnectionState;
}

interface SnapshotCallbacks {
  load: (signal: AbortSignal) => Promise<AgentSnapshot>;
  requested: (trigger: Required<SnapshotTrigger>) => void;
  loaded: (snapshot: AgentSnapshot) => void;
  failed: (message: string) => void;
}

interface SnapshotClock {
  now: () => number;
  setTimeout: (callback: () => void, delay: number) => number;
  clearTimeout: (timeout: number) => void;
}

const browserClock: SnapshotClock = {
  now: () => Date.now(),
  setTimeout: (callback, delay) => window.setTimeout(callback, delay),
  clearTimeout: (timeout) => {
    window.clearTimeout(timeout);
  },
};

export class SnapshotCoordinator {
  private readonly callbacks: SnapshotCallbacks;
  private readonly clock: SnapshotClock;
  private controller: AbortController | null = null;
  private timeout: number | null = null;
  private trailing: Required<SnapshotTrigger> | null = null;
  private requiredEpoch = 0;
  private dueAt: number | null = null;
  private isVisible = true;
  private isStopped = false;

  public constructor(
    callbacks: SnapshotCallbacks,
    clock: SnapshotClock = browserClock,
  ) {
    this.callbacks = callbacks;
    this.clock = clock;
  }

  public start(): void {
    this.request({ blocking: true });
  }

  public request(trigger: SnapshotTrigger): void {
    if (this.isStopped) return;
    const completeTrigger: Required<SnapshotTrigger> = {
      blocking: trigger.blocking,
      connection: trigger.connection ?? "live",
    };
    this.cancelTimeout();
    if (completeTrigger.blocking) this.requiredEpoch += 1;
    this.callbacks.requested(completeTrigger);
    if (this.controller !== null) {
      this.trailing = mergeTriggers(this.trailing, completeTrigger);
      return;
    }
    void this.read(this.requiredEpoch);
  }

  public setVisible(isVisible: boolean): void {
    this.isVisible = isVisible;
    this.cancelTimeout();
    if (!isVisible || this.isStopped || this.controller !== null) return;
    if (this.dueAt === null || this.dueAt <= this.clock.now()) {
      this.request({ blocking: false });
      return;
    }
    this.scheduleAt(this.dueAt);
  }

  public stop(): void {
    this.isStopped = true;
    this.trailing = null;
    this.cancelTimeout();
    this.controller?.abort();
    this.controller = null;
  }

  private async read(epoch: number): Promise<void> {
    const controller = new AbortController();
    this.controller = controller;
    try {
      const snapshot = await this.callbacks.load(controller.signal);
      if (this.isStopped || controller.signal.aborted) return;
      if (epoch < this.requiredEpoch) {
        this.trailing = mergeTriggers(this.trailing, {
          blocking: false,
          connection: "live",
        });
      } else {
        this.callbacks.loaded(snapshot);
      }
    } catch (reason: unknown) {
      if (
        !this.isStopped &&
        !controller.signal.aborted &&
        epoch >= this.requiredEpoch
      ) {
        this.callbacks.failed(errorMessage(reason));
      }
    } finally {
      if (this.controller === controller) this.controller = null;
    }
    if (this.isStopped) return;
    const trailing = this.trailing;
    this.trailing = null;
    if (trailing !== null) {
      void this.read(this.requiredEpoch);
      return;
    }
    this.dueAt = this.clock.now() + snapshotFreshnessMilliseconds;
    if (this.isVisible) this.scheduleAt(this.dueAt);
  }

  private scheduleAt(dueAt: number): void {
    this.cancelTimeout();
    this.timeout = this.clock.setTimeout(
      () => {
        this.timeout = null;
        this.request({ blocking: false });
      },
      Math.max(0, dueAt - this.clock.now()),
    );
  }

  private cancelTimeout(): void {
    if (this.timeout === null) return;
    this.clock.clearTimeout(this.timeout);
    this.timeout = null;
  }
}

function mergeTriggers(
  current: Required<SnapshotTrigger> | null,
  incoming: Required<SnapshotTrigger>,
): Required<SnapshotTrigger> {
  if (current === null) return incoming;
  return {
    blocking: current.blocking || incoming.blocking,
    connection:
      current.connection === "stale" || incoming.connection === "stale"
        ? "stale"
        : current.connection === "reconnecting" ||
            incoming.connection === "reconnecting"
          ? "reconnecting"
          : "live",
  };
}

function errorMessage(reason: unknown): string {
  if (reason instanceof Error) return reason.message;
  if (
    typeof reason === "object" &&
    reason !== null &&
    "detail" in reason &&
    typeof reason.detail === "string"
  ) {
    return reason.detail;
  }
  return "The Snapshot could not be loaded.";
}

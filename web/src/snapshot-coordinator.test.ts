/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { afterEach, describe, expect, it, vi } from "vitest";

import type { AgentSnapshot } from "./api/generated";
import {
  SnapshotCoordinator,
  snapshotFreshnessMilliseconds,
} from "./snapshot-coordinator";

describe("SnapshotCoordinator", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("resets the fallback deadline from the latest completed read", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const reads: Deferred<AgentSnapshot>[] = [];
    const coordinator = new SnapshotCoordinator({
      load: () => {
        const read = deferred<AgentSnapshot>();
        reads.push(read);
        return read.promise;
      },
      requested: vi.fn(),
      loaded: vi.fn(),
      failed: vi.fn(),
    });

    coordinator.start();
    expect(reads).toHaveLength(1);
    vi.setSystemTime(2_000);
    reads[0]?.resolve(snapshot("initial"));
    await flushPromises();

    await vi.advanceTimersByTimeAsync(snapshotFreshnessMilliseconds - 1);
    expect(reads).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(reads).toHaveLength(2);
    coordinator.stop();
  });

  it("postpones a nine-second-old fallback after a mutation read", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const reads: Deferred<AgentSnapshot>[] = [];
    const coordinator = new SnapshotCoordinator({
      load: () => {
        const read = deferred<AgentSnapshot>();
        reads.push(read);
        return read.promise;
      },
      requested: vi.fn(),
      loaded: vi.fn(),
      failed: vi.fn(),
    });
    coordinator.start();
    reads[0]?.resolve(snapshot("initial"));
    await flushPromises();

    await vi.advanceTimersByTimeAsync(9_000);
    coordinator.request({ blocking: true });
    expect(reads).toHaveLength(2);
    reads[1]?.resolve(snapshot("mutation"));
    await flushPromises();

    await vi.advanceTimersByTimeAsync(1_000);
    expect(reads).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(9_000);
    expect(reads).toHaveLength(3);
    coordinator.stop();
  });

  it("discards a pre-mutation response and performs one trailing read", async () => {
    const reads: Deferred<AgentSnapshot>[] = [];
    const loaded = vi.fn();
    const coordinator = new SnapshotCoordinator({
      load: () => {
        const read = deferred<AgentSnapshot>();
        reads.push(read);
        return read.promise;
      },
      requested: vi.fn(),
      loaded,
      failed: vi.fn(),
    });
    coordinator.start();
    coordinator.request({ blocking: true });
    coordinator.request({ blocking: true });

    reads[0]?.resolve(snapshot("stale"));
    await flushPromises();
    expect(loaded).not.toHaveBeenCalled();
    expect(reads).toHaveLength(2);

    reads[1]?.resolve(snapshot("current"));
    await flushPromises();
    expect(loaded).toHaveBeenCalledTimes(1);
    expect(loaded).toHaveBeenCalledWith(snapshot("current"));
    coordinator.stop();
  });

  it("retries ten seconds after a failed read and pauses while hidden", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const load = vi
      .fn<() => Promise<AgentSnapshot>>()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue(snapshot("recovered"));
    const failed = vi.fn();
    const coordinator = new SnapshotCoordinator({
      load,
      requested: vi.fn(),
      loaded: vi.fn(),
      failed,
    });
    coordinator.start();
    await flushPromises();
    expect(failed).toHaveBeenCalledWith("offline");

    coordinator.setVisible(false);
    await vi.advanceTimersByTimeAsync(snapshotFreshnessMilliseconds + 500);
    expect(load).toHaveBeenCalledTimes(1);
    coordinator.setVisible(true);
    expect(load).toHaveBeenCalledTimes(2);
    coordinator.stop();
  });
});

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

function deferred<T>(): Deferred<T> {
  let resolvePromise: (value: T) => void = () => undefined;
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve;
  });
  return { promise, resolve: resolvePromise };
}

async function flushPromises(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

function snapshot(runId: string): AgentSnapshot {
  return {
    runId,
    flowStatus: "running",
    errorType: null,
    errorMessage: null,
    history: { messages: [], nextBeforeSequence: null },
    description: null,
    queued: [],
    steered: [],
  };
}

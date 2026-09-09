/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { defineConfig } from "@playwright/test";

const testLogDirectory = process.env["TEST_LOG_DIR"];

export default defineConfig({
  testDir: "./tests",
  testMatch: "full-stack.spec.ts",
  outputDir:
    testLogDirectory === undefined
      ? "test-results/full-stack"
      : `${testLogDirectory}/browser`,
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  reporter: "line",
  timeout: 60_000,
  use: {
    baseURL: "http://127.0.0.1:4173",
    browserName: "chromium",
    trace: "retain-on-failure",
  },
});

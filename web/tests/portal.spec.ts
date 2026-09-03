/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { readFile } from "node:fs/promises";

import { expect, test } from "@playwright/test";

const applicationOrigin = "http://localhost";
const assetDirectory = new URL("../../internal/webui/assets/", import.meta.url);

test("starts a Flow without calling a deferred read API", async ({ page }) => {
  const requests: string[] = [];
  let startBody: unknown;
  await page.route(`${applicationOrigin}/**`, async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    requests.push(path);

    switch (path) {
      case "/":
        await route.fulfill({
          contentType: "text/html; charset=utf-8",
          body: await readFile(new URL("index.html", assetDirectory)),
        });
        return;
      case "/products/ai-agent/assets/bundle.js":
        await route.fulfill({
          contentType: "text/javascript; charset=utf-8",
          body: await readFile(new URL("bundle.js", assetDirectory)),
        });
        return;
      case "/products/ai-agent/assets/styles.css":
        await route.fulfill({
          contentType: "text/css; charset=utf-8",
          body: await readFile(new URL("styles.css", assetDirectory)),
        });
        return;
      case "/products/ai-agent/portal":
        await route.fulfill({
          contentType: "application/json",
          json: {
            providers: [
              {
                id: "mock",
                label: "Mock",
                modelPrefix: "mock/",
                defaultModel: "mock/reliable",
                credentialEnvironmentVariable: null,
                configured: true,
              },
            ],
            mcpServers: [],
            tools: [],
            builtInTools: ["ask_user", "wait"],
          },
        });
        return;
      case "/products/ai-agent/start": {
        startBody = request.postDataJSON();
        await route.fulfill({
          status: 201,
          contentType: "application/json",
          json: { flowId: "browser-flow" },
        });
        return;
      }
      default:
        await route.fulfill({ status: 404 });
    }
  });

  await page.goto(applicationOrigin);
  await expect(
    page.getByRole("heading", { name: "Start a Superagent" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Start agent" }).click();
  await expect(
    page.getByRole("heading", { name: "Waiting for Dex Snapshot API" }),
  ).toBeVisible();
  await expect(page.getByText("browser-flow")).toBeVisible();

  expect(startBody).toMatchObject({
    provider: "mock",
    model: "mock/reliable",
  });
  expect(requests).toContain("/products/ai-agent/portal");
  expect(requests).toContain("/products/ai-agent/start");
  expect(
    requests.filter((path) =>
      [
        "/products/ai-agent/history",
        "/products/ai-agent/message-queue",
        "/products/ai-agent/describe",
        "/products/ai-agent/status",
        "/products/ai-agent/snapshot",
      ].includes(path),
    ),
  ).toEqual([]);
});

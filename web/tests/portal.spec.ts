/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { readFile } from "node:fs/promises";

import { expect, test } from "@playwright/test";

const applicationOrigin = "https://app.example.test";
const apiOrigin = "https://api.example.test";
const assetDirectory = new URL("../dist/", import.meta.url);

test("starts a Flow against a separately deployed API", async ({ page }) => {
  const applicationRequests: string[] = [];
  const apiRequests: string[] = [];
  let startBody: unknown;

  await page.route(`${applicationOrigin}/**`, async (route) => {
    const path = new URL(route.request().url()).pathname;
    applicationRequests.push(path);
    switch (path) {
      case "/":
        await route.fulfill({
          contentType: "text/html; charset=utf-8",
          body: await readFile(new URL("index.html", assetDirectory)),
        });
        return;
      case "/bundle.js":
        await route.fulfill({
          contentType: "text/javascript; charset=utf-8",
          body: await readFile(new URL("bundle.js", assetDirectory)),
        });
        return;
      case "/styles.css":
        await route.fulfill({
          contentType: "text/css; charset=utf-8",
          body: await readFile(new URL("styles.css", assetDirectory)),
        });
        return;
      case "/config.json":
        await route.fulfill({
          contentType: "application/json",
          json: { apiOrigin },
        });
        return;
      default:
        await route.fulfill({ status: 404 });
    }
  });

  await page.route(`${apiOrigin}/**`, async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (request.method() === "OPTIONS") {
      await route.fulfill({
        status: 204,
        headers: {
          "Access-Control-Allow-Origin": applicationOrigin,
          "Access-Control-Allow-Methods": "GET, POST",
          "Access-Control-Allow-Headers": "Content-Type",
        },
      });
      return;
    }
    apiRequests.push(path);
    const headers = { "Access-Control-Allow-Origin": applicationOrigin };
    switch (path) {
      case "/products/ai-agent/portal":
        await route.fulfill({
          headers,
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
      case "/products/ai-agent/start":
        startBody = request.postDataJSON();
        await route.fulfill({
          status: 201,
          headers,
          contentType: "application/json",
          json: { flowId: "browser-flow" },
        });
        return;
      default:
        await route.fulfill({ status: 404, headers });
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
  expect(applicationRequests).toContain("/config.json");
  expect(apiRequests).toContain("/products/ai-agent/portal");
  expect(apiRequests).toContain("/products/ai-agent/start");
  expect(
    apiRequests.filter((path) =>
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

/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { expect, test } from "@playwright/test";

const apiOrigin = "http://127.0.0.1:8080";

test("renders durable submit, history, activity, queue, edit, delete, and steer", async ({
  page,
}) => {
  const snapshots: number[] = [];
  const commandStatuses: number[] = [];
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (path === "/products/ai-agent/snapshot") {
      snapshots.push(response.status());
    }
    if (
      path === "/products/ai-agent/messages" ||
      path.startsWith("/products/ai-agent/message-queue/")
    ) {
      commandStatuses.push(response.status());
    }
  });

  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Start a SuperAgent" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Start agent" }).click();
  await expect(page.getByRole("heading", { name: "SuperAgent" })).toBeVisible();
  await expect(page.locator(".flow-identity code")).toHaveCount(2);
  await expect(page.locator(".flow-identity code").first()).not.toBeEmpty();
  await expect(page.locator(".flow-identity code").last()).not.toBeEmpty();
  await expect(page.getByText("Waiting For Message").first()).toBeVisible();
  expect(snapshots).toEqual([200]);

  const composer = page.getByRole("textbox", { name: "Message" });
  await composer.fill("**durable** hello");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Submitting…")).toBeVisible();
  await expect(page.getByText("Queued", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("article").filter({ hasText: "durable hello" }),
  ).toBeVisible();
  await expect(
    page
      .getByRole("article")
      .filter({ hasText: "Local demo response: durable hello" }),
  ).toBeVisible();
  await expect(page.locator(".queue-message.submitting")).toHaveCount(0);
  await expect(
    page
      .getByRole("region", { name: "Agent activity" })
      .locator(".activity-summary"),
  ).toHaveCount(1);
  await expect(page.locator(".activity-card li")).toHaveCount(0);
  expect(commandStatuses).toContain(202);

  await composer.fill("/wait 90 browser queue test");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("90s")).toBeVisible();
  await page.getByRole("checkbox", { name: "Plan mode" }).check();
  await composer.fill("edit this queued message");
  await page.getByRole("button", { name: "Create plan" }).click();
  await expect(page.getByText("Queued", { exact: true })).toBeVisible();
  await expect(page.getByText("Plan", { exact: true })).toBeVisible();
  await expect(page.getByText("edit this queued message")).toBeVisible();
  await composer.fill("delete this queued message");
  await composer.press("Control+Enter");
  await expect(page.locator(".queue-message")).toHaveCount(2);
  await expect(page.getByRole("heading", { name: /2 queued/u })).toBeVisible();
  await expect(page.getByText("Chat", { exact: true })).toBeVisible();
  expect(await page.locator(".queue-message p").allTextContents()).toEqual([
    "edit this queued message",
    "delete this queued message",
  ]);

  const editRow = page
    .locator(".queue-message")
    .filter({ hasText: "edit this queued message" });
  await expect(editRow.getByRole("button", { name: "Edit" })).toBeVisible({
    timeout: 15_000,
  });
  await editRow.getByRole("button", { name: "Edit" }).click();
  await expect(composer).toHaveValue("edit this queued message");
  await expect(composer).toBeFocused();
  await expect(page.getByRole("checkbox", { name: "Plan mode" })).toBeChecked();
  await composer.fill("");
  await page.getByRole("checkbox", { name: "Plan mode" }).uncheck();
  const deleteRow = page
    .locator(".queue-message")
    .filter({ hasText: "delete this queued message" });
  await deleteRow.getByRole("button", { name: "Delete" }).click();
  await expect(page.getByText("delete this queued message")).toHaveCount(0);

  await composer.fill("steer the timer now");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByRole("button", { name: "Steer" })).toBeVisible({
    timeout: 15_000,
  });
  await page.getByRole("button", { name: "Steer" }).click();
  await expect(page.getByText("Steering", { exact: true })).toBeVisible();
  await expect(page.getByText("90s")).toHaveCount(0);
  await expect(
    page.getByRole("article").filter({ hasText: "steer the timer now" }),
  ).toHaveCount(2);
  await expect(
    page
      .getByRole("article")
      .filter({ hasText: "Local demo response: steer the timer now" }),
  ).toBeVisible();
});

test("loads one adjacent archive chunk into the narrow-screen DOM on top scroll", async ({
  page,
  request,
}) => {
  test.setTimeout(90_000);
  const flowId = `archive-ui-${String(Date.now())}`;
  const start = await request.post(`${apiOrigin}/products/ai-agent/start`, {
    data: {
      flowId,
      provider: "mock",
      model: "mock/dex",
      systemPrompt: "Archive UI integration",
      maxContextTokens: 1_000_000,
      messageRetentionLimit: 1_000,
      mcpEnabled: false,
      enabledMcpServers: [],
      enabledTools: [],
    },
  });
  expect(start.status()).toBe(201);
  for (let index = 1; index <= 15; index += 1) {
    const sent = await request.post(`${apiOrigin}/products/ai-agent/messages`, {
      data: {
        flowId,
        content: `archive ui ${String(index).padStart(2, "0")}`,
        planMode: false,
      },
    });
    expect(sent.status()).toBe(202);
    await expect
      .poll(
        async () => {
          const response = await request.get(
            `${apiOrigin}/products/ai-agent/snapshot?flowId=${flowId}`,
          );
          const body = await response.text();
          const match = /"lastSequence":(\d+)/u.exec(body);
          return match === null ? 0 : Number(match[1]);
        },
        { timeout: 15_000, intervals: [100, 250, 500] },
      )
      .toBe(index * 2);
  }

  const archiveRequests: string[] = [];
  const snapshotResponses: number[] = [];
  page.on("request", (browserRequest) => {
    const url = new URL(browserRequest.url());
    if (url.pathname === "/products/ai-agent/archived-messages") {
      archiveRequests.push(url.search);
    }
  });
  page.on("response", (response) => {
    if (new URL(response.url()).pathname === "/products/ai-agent/snapshot") {
      snapshotResponses.push(response.status());
    }
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/?flowId=${flowId}`);
  await expect(page.getByRole("article")).toHaveCount(10);
  expect(archiveRequests).toHaveLength(0);

  await page.evaluate(() => {
    window.scrollTo(0, 0);
    window.dispatchEvent(new Event("scroll"));
  });
  await expect(page.getByRole("article")).toHaveCount(20);
  expect(archiveRequests).toHaveLength(1);
  expect(archiveRequests[0]).toContain("beforeSequence=21");
  await expect(page.getByText("archive ui 06", { exact: true })).toHaveCount(1);
  await expect(
    page.getByText("Local demo response: archive ui 15", { exact: true }),
  ).toHaveCount(1);
  const scrollPosition = await page.evaluate(() => ({
    top: window.scrollY,
    distanceToBottom:
      document.documentElement.scrollHeight -
      window.scrollY -
      window.innerHeight,
  }));
  expect(scrollPosition.top).toBeGreaterThan(0);
  expect(scrollPosition.distanceToBottom).toBeGreaterThan(100);
  await expect
    .poll(() => snapshotResponses.length, { timeout: 15_000 })
    .toBe(2);
  await expect(page.getByRole("article")).toHaveCount(20);
  await expect(page.getByText("archive ui 06", { exact: true })).toHaveCount(1);
  expect(archiveRequests).toHaveLength(1);
});

test("renders plan, input choices, and real MCP approval outcomes", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Start agent" }).click();
  await expect(page.getByRole("heading", { name: "SuperAgent" })).toBeVisible();
  const composer = page.getByRole("textbox", { name: "Message" });

  await page.getByRole("checkbox", { name: "Plan mode" }).check();
  await composer.fill("verify full-stack planning");
  await page.getByRole("button", { name: "Create plan" }).click();
  await expect(page.getByText("Plan revision 1")).toBeVisible();
  await expect(page.locator(".plan-tasks li")).toHaveCount(2);
  await page.getByRole("button", { name: "Execute plan" }).click();
  await expect(page.getByRole("button", { name: /execution/i })).toBeDisabled();
  await expect(page.getByRole("heading", { name: "Completed" })).toBeVisible({
    timeout: 20_000,
  });
  await expect(page.locator(".plan-tasks li.completed")).toHaveCount(2);

  await composer.fill("/choose Region? | us-west | eu-central");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Region?", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "us-west" })).toBeVisible();
  await expect(page.getByRole("button", { name: "eu-central" })).toBeVisible();
  await page.getByRole("button", { name: "us-west" }).click();
  await expect(
    page.getByRole("textbox", { name: "Answer Agent question" }),
  ).toHaveValue("us-west");
  await page.getByRole("button", { name: "Submit answer" }).click();
  await expect(page.getByText("Region?", { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("article").filter({ hasText: "us-west" }).first(),
  ).toBeVisible();

  await composer.fill('/tool fixture__echo {"value":"approved"}');
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Approval required")).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "fixture__echo" }),
  ).toBeVisible();
  await expect(page.locator(".approval-card pre")).toContainText(
    '"value":"approved"',
  );
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText("Approval required")).toHaveCount(0);
  await expect(
    page
      .locator(".message-bubble.tool")
      .filter({ hasText: '"echo":"approved"' }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Agent activity" }),
  ).not.toContainText('"value":"approved"');

  await composer.fill('/tool fixture__echo {"value":"rejected"}');
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Approval required")).toBeVisible();
  await page.getByRole("button", { name: "Reject" }).click();
  await expect(page.getByText("Approval required")).toHaveCount(0);
  await expect(
    page
      .locator(".message-bubble.tool")
      .filter({ hasText: "rejected_by_user" }),
  ).toBeVisible();
});

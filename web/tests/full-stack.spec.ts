/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  expect,
  test,
  type Locator,
  type Page,
  type Request,
} from "@playwright/test";

import {
  EventStream,
  FlowStatus,
  type AgentSnapshot,
} from "../src/api/generated/index";

const apiOrigin =
  process.env["SUPERAGENT_E2E_API_ORIGIN"] ?? "http://127.0.0.1:8080";

test("renders chronological transient activity and durable queue interactions", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const snapshots: number[] = [];
  const commandStatuses: number[] = [];
  let shouldHoldMessage = true;
  let releaseMessage: () => void = () => undefined;
  const heldMessage = new Promise<void>((resolve) => {
    releaseMessage = resolve;
  });
  await page.route("**/products/ai-agent/messages", async (route) => {
    if (shouldHoldMessage) {
      shouldHoldMessage = false;
      await heldMessage;
    }
    await route.continue();
  });
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (path === "/products/ai-agent/snapshot")
      snapshots.push(response.status());
    if (
      path === "/products/ai-agent/messages" ||
      path.startsWith("/products/ai-agent/message-queue/")
    ) {
      commandStatuses.push(response.status());
    }
  });

  await startAgent(page);
  expect(snapshots).toEqual([200]);

  const composerCard = page.locator(".composer-card");
  const agentStatus = page.getByRole("group", { name: "Agent status" });
  const statusBox = await agentStatus.boundingBox();
  const composerCardBox = await composerCard.boundingBox();
  const sendBox = await page
    .getByRole("button", { name: "Send" })
    .boundingBox();
  expect(statusBox).not.toBeNull();
  expect(composerCardBox).not.toBeNull();
  expect(sendBox).not.toBeNull();
  await expect(agentStatus).toHaveCSS("position", "static");
  await expect(
    composerCard.getByRole("group", { name: "Agent status" }),
  ).toBeVisible();
  expect(statusBox?.x).toBeGreaterThanOrEqual(composerCardBox?.x ?? 0);
  expect((statusBox?.y ?? 0) + (statusBox?.height ?? 0)).toBeLessThan(
    sendBox?.y ?? 0,
  );

  const composer = page.getByRole("textbox", { name: "Message" });
  await composer.fill("/reason Checked the constraints | Durable answer");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(composer).toBeFocused();
  const optimisticQueue = page.getByRole("region", { name: "Message queue" });
  await expect(optimisticQueue).toContainText("1 queued · 0 steering");
  await expect(
    optimisticQueue.getByRole("button", { name: /Message queue/ }),
  ).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByText("Submitting…")).toBeVisible();
  await expect(page.locator(".queue-message.submitting")).toHaveCSS(
    "border-color",
    "rgb(245, 158, 11)",
  );
  await expect(page.locator(".queue-message.submitting")).toHaveCSS(
    "background-color",
    "rgb(255, 251, 235)",
  );
  await expect(
    page.getByRole("button", { name: "Jump to latest message" }),
  ).toHaveCount(0);
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.documentElement.scrollHeight -
          window.scrollY -
          window.innerHeight,
      ),
    )
    .toBeLessThanOrEqual(12);
  releaseMessage();

  const history = page.getByRole("region", { name: "Conversation history" });
  await expect(history.getByText("Checked the constraints")).toBeVisible();
  await expect(
    history
      .locator(".message-bubble.assistant")
      .filter({ hasText: "Durable answer" }),
  ).toHaveCount(1);
  await expect(
    history.locator(".message-bubble.user").filter({
      hasText: "/reason Checked the constraints | Durable answer",
    }),
  ).toHaveCount(1);
  await expect(history.locator(".live-message")).toHaveCount(0);
  await expect(history.locator(".activity-entry")).toHaveCount(2);
  await expect(composer).toBeFocused();
  await expect(history.locator(".activity-entry").nth(0)).toContainText(
    "Calling mock/dex.",
  );
  await expect(history.locator(".activity-entry").nth(1)).toContainText(
    "Model response completed.",
  );
  const timelineText = await directTimelineText(history);
  const reasoningIndex = timelineText.findIndex((text) =>
    text.includes("Reasoning summary"),
  );
  const assistantIndex = timelineText.findIndex(
    (text) => text.includes("Durable answer") && text.includes("Assistant"),
  );
  expect(timelineText[0]).toContain(
    "/reason Checked the constraints | Durable answer",
  );
  expect(reasoningIndex).toBeGreaterThan(0);
  expect(reasoningIndex).toBeLessThan(assistantIndex);
  const timelineTimes = await directTimelineTimes(history);
  expect(timelineTimes).toEqual(
    [...timelineTimes].sort((left, right) => left - right),
  );
  await expect(page.locator(".activity-card")).toHaveCount(0);
  await expect(page.locator(".queue-message.submitting")).toHaveCount(0);
  expect(commandStatuses).toContain(202);

  await composer.fill("/wait 90 browser queue test");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("90s")).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "SuperAgent" })).toBeVisible();
  await expect(page.getByText("90s")).toBeVisible();
  await page.getByRole("checkbox", { name: "Plan mode" }).check();
  await composer.fill("edit this queued message");
  await page.getByRole("button", { name: "Create plan" }).click();
  await composer.fill("delete this queued message");
  await expect(page.getByRole("button", { name: "Send" })).toBeEnabled();
  await composer.press("Control+Enter");

  const queue = page.getByRole("region", { name: "Message queue" });
  await expect(queue).toContainText("2 queued · 0 steering");
  await expectMessageQueueExpanded(queue);
  await expect(queue.getByText("Plan", { exact: true })).toBeVisible();
  await expect(queue.getByText("Chat", { exact: true })).toBeVisible();
  expect(await queue.locator(".queue-message p").allTextContents()).toEqual([
    "edit this queued message",
    "delete this queued message",
  ]);
  const flowId = await displayedFlowID(page);
  const originalEditMessageID = await pendingMessageID(
    page,
    flowId,
    "edit this queued message",
  );

  const editRow = queue
    .locator(".queue-message")
    .filter({ hasText: "edit this queued message" });
  await expect(editRow.getByRole("button", { name: "Edit" })).toBeVisible({
    timeout: 15_000,
  });
  await editRow.getByRole("button", { name: "Edit" }).click();
  await expect(composer).toHaveValue("edit this queued message");
  await expect(composer).toBeFocused();
  await expect(page.getByRole("checkbox", { name: "Plan mode" })).toBeChecked();
  await expect(editRow).toHaveCount(0);
  await composer.fill("edited queued message");
  await page.getByRole("button", { name: "Create plan" }).click();
  const editedRow = queue
    .locator(".queue-message")
    .filter({ hasText: "edited queued message" });
  await expect(editedRow).toBeVisible();
  const editedMessageID = await pendingMessageID(
    page,
    flowId,
    "edited queued message",
  );
  expect(editedMessageID).not.toBe(originalEditMessageID);
  await page.getByRole("checkbox", { name: "Plan mode" }).uncheck();

  const deleteRow = queue
    .locator(".queue-message")
    .filter({ hasText: "delete this queued message" });
  await deleteRow.getByRole("button", { name: "Delete" }).click();
  await expect(queue.getByText("delete this queued message")).toHaveCount(0);

  await composer.fill("steer the timer now");
  await page.getByRole("button", { name: "Send" }).click();
  const steerRow = queue
    .locator(".queue-message")
    .filter({ hasText: "steer the timer now" });
  const steerButton = steerRow.getByRole("button", { name: "Steer now" });
  await expect(steerButton).toBeVisible({ timeout: 15_000 });
  await expect(steerButton).toHaveCSS("background-color", "rgb(53, 65, 169)");
  await expect(steerButton).toHaveCSS("color", "rgb(255, 255, 255)");
  await steerButton.focus();
  await expect(steerButton).toBeFocused();
  await steerButton.press("Enter");
  await expect(queue.getByText("Steering", { exact: true })).toBeVisible();
  await expect(page.getByText("90s")).toHaveCount(0);
  await expect(
    history
      .locator(".message-bubble.user")
      .filter({ hasText: "steer the timer now" }),
  ).toHaveCount(1);
  await expect(
    history
      .locator(".message-bubble.assistant")
      .filter({ hasText: "Local demo response: steer the timer now" }),
  ).toHaveCount(1);
  await expect(history.locator(".activity-entry")).not.toHaveCount(1);
  await page.reload();
  await expect(page.getByRole("heading", { name: "SuperAgent" })).toBeVisible();
  await expect(
    page
      .locator(".message-bubble.user")
      .filter({ hasText: "steer the timer now" }),
  ).toHaveCount(1);
  await expect(
    page
      .locator(".message-bubble.assistant")
      .filter({ hasText: "Local demo response: steer the timer now" }),
  ).toHaveCount(1);
});

test("accepts the first message after Start and retains it across refresh", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await startAgent(page);

  const message = `first-message-${crypto.randomUUID()}`;
  const accepted = page.waitForResponse(
    (response) =>
      response.status() === 202 &&
      new URL(response.url()).pathname === "/products/ai-agent/messages",
    { timeout: 5_000 },
  );
  const composer = page.getByRole("textbox", { name: "Message" });
  await composer.fill(message);
  await page.getByRole("button", { name: "Send" }).click();
  await accepted;

  await page.reload();
  await expect(page.getByText(message, { exact: true })).toBeVisible();
});

test("recovers a committed first message when refresh loses its response", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await startAgent(page);

  const sendPath = "/products/ai-agent/messages";
  let markCommitted: () => void = () => undefined;
  const committed = new Promise<void>((resolve) => {
    markCommitted = resolve;
  });
  await page.route(`**${sendPath}`, async (route) => {
    const response = await route.fetch();
    expect(response.status()).toBe(202);
    await route.abort("failed");
    markCommitted();
  });

  const message = `lost-first-response-${crypto.randomUUID()}`;
  const composer = page.getByRole("textbox", { name: "Message" });
  await composer.fill(message);
  await page.getByRole("button", { name: "Send" }).click();
  await committed;

  await page.reload();
  await expect(page.getByText(message, { exact: true })).toBeVisible();
  await page.unroute(`**${sendPath}`);
});

test("prioritizes a first message while another Agent tab is polling", async ({
  page,
  context,
}) => {
  test.setTimeout(90_000);
  await startAgent(page);
  const secondPage = await context.newPage();
  try {
    const pendingLiveReads = new Set<Request>();
    let didSendAfterLiveReadsSettled = false;
    const isLiveRead = (request: Request) => {
      const path = new URL(request.url()).pathname;
      return (
        path === "/products/ai-agent/events" ||
        path === "/products/ai-agent/interaction-status"
      );
    };
    secondPage.on("request", (request) => {
      if (isLiveRead(request)) pendingLiveReads.add(request);
      if (new URL(request.url()).pathname === "/products/ai-agent/messages") {
        didSendAfterLiveReadsSettled = pendingLiveReads.size === 0;
      }
    });
    secondPage.on("requestfinished", (request) => {
      pendingLiveReads.delete(request);
    });
    secondPage.on("requestfailed", (request) => {
      pendingLiveReads.delete(request);
    });
    await startAgent(secondPage);
    await expect.poll(() => pendingLiveReads.size).toBeGreaterThan(0);
    const message = `multi-tab-first-message-${crypto.randomUUID()}`;
    const accepted = secondPage.waitForResponse(
      (response) =>
        response.status() === 202 &&
        new URL(response.url()).pathname === "/products/ai-agent/messages",
      { timeout: 5_000 },
    );
    const composer = secondPage.getByRole("textbox", { name: "Message" });
    await composer.fill(message);
    await secondPage.getByRole("button", { name: "Send" }).click();
    await accepted;
    expect(didSendAfterLiveReadsSettled).toBe(true);

    await secondPage.reload();
    await expect(secondPage.getByText(message, { exact: true })).toBeVisible();
  } finally {
    await secondPage.close();
  }
});

test("keeps mutations gated until the post-command Snapshot completes", async ({
  page,
}) => {
  test.setTimeout(90_000);
  let shouldHoldSnapshot = false;
  let snapshotRequests = 0;
  let releaseSnapshot: () => void = () => undefined;
  const heldSnapshot = new Promise<void>((resolve) => {
    releaseSnapshot = resolve;
  });
  await page.route("**/products/ai-agent/snapshot?**", async (route) => {
    snapshotRequests += 1;
    if (shouldHoldSnapshot) {
      shouldHoldSnapshot = false;
      await heldSnapshot;
    }
    await route.continue();
  });
  await startAgent(page);
  await page.waitForTimeout(9_000);
  shouldHoldSnapshot = true;
  const composer = page.getByRole("textbox", { name: "Message" });
  await composer.fill("verify reconciliation gate");
  const accepted = page.waitForResponse(
    (response) =>
      response.status() === 202 &&
      new URL(response.url()).pathname === "/products/ai-agent/messages",
  );
  await page.getByRole("button", { name: "Send" }).click();
  await accepted;

  await expect(page.getByRole("status")).toHaveText("Syncing durable state…");
  const queue = page.getByRole("region", { name: "Message queue" });
  await expectMessageQueueExpanded(queue);
  await expect(queue.getByText("Queued", { exact: true })).toBeVisible();
  await expect(queue.getByText("verify reconciliation gate")).toBeVisible();
  await composer.fill("editable draft while syncing");
  await expect(composer).toBeEnabled();
  await expect(page.getByRole("button", { name: "Send" })).toBeDisabled();
  releaseSnapshot();
  await expect.poll(() => snapshotRequests).toBeGreaterThanOrEqual(3);
  await expect(page.getByText("Syncing durable state…")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Send" })).toBeEnabled();
  const requestsAfterReconciliation = snapshotRequests;
  await page.waitForTimeout(1_500);
  expect(snapshotRequests).toBe(requestsAfterReconciliation);
});

test("recovers an initial Snapshot network failure through the real API", async ({
  page,
}) => {
  let shouldAbortSnapshot = true;
  await page.route("**/products/ai-agent/snapshot?**", async (route) => {
    if (shouldAbortSnapshot) {
      shouldAbortSnapshot = false;
      await route.abort("failed");
      return;
    }
    await route.continue();
  });
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Start a SuperAgent" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Start agent" }).click();
  await expect(
    page.getByRole("heading", { name: "Snapshot unavailable" }),
  ).toBeVisible();

  await page.getByRole("button", { name: "Retry Snapshot" }).click();
  await expect(page.getByRole("heading", { name: "SuperAgent" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Message" })).toBeEnabled();
});

test("reconciles accepted commands when their browser responses are lost", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const requestCounts = new Map<string, number>();
  page.on("request", (request) => {
    if (request.method() !== "POST") return;
    const path = new URL(request.url()).pathname;
    requestCounts.set(path, (requestCounts.get(path) ?? 0) + 1);
  });

  await startAgent(page);
  const composer = page.getByRole("textbox", { name: "Message" });
  const history = page.getByRole("region", { name: "Conversation history" });

  const sendPath = "/products/ai-agent/messages";
  await abortSuccessfulResponseOnce(page, sendPath, 202);
  const sendsBefore = requestCounts.get(sendPath) ?? 0;
  await composer.fill("ambiguous send is reconciled");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(
    history.locator(".message-bubble.user").filter({
      hasText: "ambiguous send is reconciled",
    }),
  ).toHaveCount(1);
  await expect(
    history.locator(".message-bubble.assistant").filter({
      hasText: "Local demo response: ambiguous send is reconciled",
    }),
  ).toHaveCount(1);
  expect(requestCounts.get(sendPath)).toBe(sendsBefore + 1);
  await page.unroute(`**${sendPath}`);

  await composer.fill("/questions");
  await page.getByRole("button", { name: "Send" }).click();
  const questions = page.getByRole("region", { name: "Agent questions" });
  await expect(questions).toBeVisible();
  await fillQuestionBatch(questions);
  const answerPath = "/products/ai-agent/questions/answer";
  await abortSuccessfulResponseOnce(page, answerPath, 202);
  const answersBefore = requestCounts.get(answerPath) ?? 0;
  await questions.getByRole("button", { name: "Submit all" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(questions).toHaveCount(0);
  await expect(
    history.locator(".message-bubble.user").filter({ hasText: "Summary" }),
  ).toHaveCount(1);
  expect(requestCounts.get(answerPath)).toBe(answersBefore + 1);
  await page.unroute(`**${answerPath}`);

  await composer.fill('/tool fixture__echo {"value":"ambiguous approval"}');
  await page.getByRole("button", { name: "Send" }).click();
  const approval = page.locator(".approval-card");
  await expect(approval).toBeVisible();
  const approvalPath = "/products/ai-agent/tool-approvals";
  await abortSuccessfulResponseOnce(page, approvalPath, 202);
  const approvalsBefore = requestCounts.get(approvalPath) ?? 0;
  await approval.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(approval).toHaveCount(0);
  await expect(
    history.locator(".message-bubble.tool").filter({
      hasText: '"echo":"ambiguous approval"',
    }),
  ).toHaveCount(1);
  expect(requestCounts.get(approvalPath)).toBe(approvalsBefore + 1);
  await page.unroute(`**${approvalPath}`);

  await composer.fill("/wait 90 ambiguous steering");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("90s")).toBeVisible();
  await composer.fill("ambiguous steer is reconciled");
  await page.getByRole("button", { name: "Send" }).click();
  await expectMessageQueueExpanded(
    page.getByRole("region", { name: "Message queue" }),
  );
  const queued = page
    .locator(".queue-message")
    .filter({ hasText: "ambiguous steer is reconciled" });
  await expect(queued).toBeVisible();
  const steerPath = "/products/ai-agent/message-queue/steer";
  await abortSuccessfulResponseOnce(page, steerPath, 200);
  const steersBefore = requestCounts.get(steerPath) ?? 0;
  await queued.getByRole("button", { name: "Steer now" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page.getByText("90s")).toHaveCount(0);
  await expect(
    history.locator(".message-bubble.user").filter({
      hasText: "ambiguous steer is reconciled",
    }),
  ).toHaveCount(1);
  expect(requestCounts.get(steerPath)).toBe(steersBefore + 1);
  await page.unroute(`**${steerPath}`);
});

test("reconciles stale queue, question, and approval controls without damaging the Flow", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const staleStatuses: number[] = [];
  page.on("response", (response) => {
    if ([404, 409].includes(response.status())) {
      staleStatuses.push(response.status());
    }
  });

  await startAgent(page);
  const flowId = await displayedFlowID(page);
  const composer = page.getByRole("textbox", { name: "Message" });
  await composer.fill("/wait 90 stale controls");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("90s")).toBeVisible();
  await composer.fill("stale queue target");
  await page.getByRole("button", { name: "Send" }).click();
  await expectMessageQueueExpanded(
    page.getByRole("region", { name: "Message queue" }),
  );
  const staleQueue = page
    .locator(".queue-message")
    .filter({ hasText: "stale queue target" });
  await expect(staleQueue).toBeVisible();
  const deletePath = "/products/ai-agent/message-queue/delete";
  await consumeBeforeBrowserRequest(page, deletePath, 200);
  await staleQueue.getByRole("button", { name: "Delete" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(staleQueue).toHaveCount(0);
  await page.unroute(`**${deletePath}`);

  await composer.fill("/questions");
  await page.getByRole("button", { name: "Send" }).click();
  const questionQueue = page
    .locator(".queue-message")
    .filter({ hasText: "/questions" });
  await expect(questionQueue).toBeVisible();
  await questionQueue.getByRole("button", { name: "Steer now" }).click();
  const questions = page.getByRole("region", { name: "Agent questions" });
  await expect(questions).toBeVisible();
  await fillQuestionBatch(questions);
  const answerPath = "/products/ai-agent/questions/answer";
  await consumeBeforeBrowserRequest(page, answerPath, 202);
  await questions.getByRole("button", { name: "Submit all" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(questions).toHaveCount(0);
  await page.unroute(`**${answerPath}`);

  await composer.fill('/tool fixture__echo {"value":"stale approval"}');
  await page.getByRole("button", { name: "Send" }).click();
  const approval = page.locator(".approval-card");
  await expect(approval).toBeVisible();
  const approvalPath = "/products/ai-agent/tool-approvals";
  await consumeBeforeBrowserRequest(page, approvalPath, 202);
  await approval.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(approval).toHaveCount(0);
  await page.unroute(`**${approvalPath}`);

  expect(staleStatuses).toHaveLength(3);
  for (const status of staleStatuses) expect([404, 409]).toContain(status);
  await composer.fill("Flow continues after stale controls");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(
    page.locator(".message-bubble.assistant").filter({
      hasText: "Local demo response: Flow continues after stale controls",
    }),
  ).toHaveCount(1);
  const snapshot = await readAgentSnapshot(page, flowId);
  expect(snapshot.flowStatus).toBe(FlowStatus.RUNNING);
});

test("resumes each live Stream after interruption without duplicate timeline entries", async ({
  page,
}) => {
  test.setTimeout(180_000);
  for (const stream of [
    EventStream.REASONING,
    EventStream.ASSISTANT,
    EventStream.ACTIVITY,
  ]) {
    let deliveredEvent = false;
    let abortedPoll = false;
    let recentReads = 0;
    const resumeTokens: string[] = [];
    await page.route("**/products/ai-agent/events/recent?**", async (route) => {
      const url = new URL(route.request().url());
      if (url.searchParams.get("stream") === stream) recentReads++;
      await route.continue();
    });
    await page.route("**/products/ai-agent/events?**", async (route) => {
      const url = new URL(route.request().url());
      if (url.searchParams.get("stream") !== stream) {
        await route.continue();
        return;
      }
      const resumeToken = url.searchParams.get("resumeToken");
      if (resumeToken !== null) resumeTokens.push(resumeToken);
      if (abortedPoll) {
        await route.continue();
        return;
      }
      if (deliveredEvent) {
        abortedPoll = true;
        await route.abort("failed");
        return;
      }
      const response = await route.fetch();
      if (response.status() === 200) deliveredEvent = true;
      await route.fulfill({ response });
    });

    await startAgent(page);
    const message = `reconnect ${stream}`;
    const composer = page.getByRole("textbox", { name: "Message" });
    await composer.fill(`/reason ${message} | ${message}`);
    await page.getByRole("button", { name: "Send" }).click();
    await expect.poll(() => abortedPoll).toBe(true);
    await expect.poll(() => resumeTokens.length).toBeGreaterThan(0);
    const history = page.getByRole("region", { name: "Conversation history" });
    await expect(
      history.locator(".message-bubble.assistant").filter({ hasText: message }),
    ).toHaveCount(1);
    await expect(history.locator(".live-message")).toHaveCount(0);
    await expect(history.locator(".activity-entry")).toHaveCount(2);
    const activityRows = await history
      .locator(".activity-entry")
      .allTextContents();
    expect(new Set(activityRows).size).toBe(activityRows.length);
    recentReads = 0;
    resumeTokens.length = 0;
    await page.reload();
    await expect.poll(() => recentReads).toBeGreaterThan(0);
    await expect.poll(() => resumeTokens.length).toBeGreaterThan(0);
    await expect(
      page.locator(".message-bubble.assistant").filter({ hasText: message }),
    ).toHaveCount(1);
    await page.unroute("**/products/ai-agent/events/recent?**");
    await page.unroute("**/products/ai-agent/events?**");
    await page.getByRole("button", { name: "Start another agent" }).click();
  }
});

test("preserves a reading position and jumps to new content on a narrow screen", async ({
  page,
  request,
}) => {
  test.setTimeout(120_000);
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
  page.on("request", (browserRequest) => {
    const url = new URL(browserRequest.url());
    if (url.pathname === "/products/ai-agent/archived-messages") {
      archiveRequests.push(url.search);
    }
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/?flowId=${flowId}`);
  await expect(page.locator(".message-bubble")).toHaveCount(10);
  expect(archiveRequests).toHaveLength(0);
  await expect(
    page.getByRole("button", { name: "Jump to latest message" }),
  ).toHaveCount(0);

  const status = page.getByRole("group", { name: "Agent status" });
  const initialStatusBox = await status.boundingBox();
  const initialComposerBox = await page.locator(".composer-card").boundingBox();
  const initialSendBox = await page
    .getByRole("button", { name: "Send" })
    .boundingBox();
  expect(initialStatusBox).not.toBeNull();
  expect(initialComposerBox).not.toBeNull();
  expect(initialSendBox).not.toBeNull();
  await expect(status).toHaveCSS("position", "static");
  expect(initialStatusBox?.x).toBeGreaterThanOrEqual(
    initialComposerBox?.x ?? 0,
  );
  expect(
    (initialStatusBox?.y ?? 0) + (initialStatusBox?.height ?? 0),
  ).toBeLessThan(initialSendBox?.y ?? 0);

  await page.evaluate(() => {
    window.scrollTo(0, 0);
    window.dispatchEvent(new Event("scroll"));
  });
  await expect(page.locator(".message-bubble")).toHaveCount(20);
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
  await expect(page.locator(".message-bubble")).toHaveCount(20);
  await expect(page.getByText("archive ui 06", { exact: true })).toHaveCount(1);
  expect(archiveRequests).toHaveLength(1);

  const readingPosition = scrollPosition.top;
  const sentWhileReading = await request.post(
    `${apiOrigin}/products/ai-agent/messages`,
    {
      data: {
        flowId,
        content: "new content while reading history",
        planMode: false,
      },
    },
  );
  expect(sentWhileReading.status()).toBe(202);
  await expect(
    page.getByRole("button", { name: "Jump to latest message" }),
  ).toBeVisible();
  await expect(
    page.locator(".message-bubble.assistant").filter({
      hasText: "Local demo response: new content while reading history",
    }),
  ).toHaveCount(1);
  await expect
    .poll(() => page.evaluate(() => window.scrollY))
    .toBe(readingPosition);

  const statusWhileReadingBox = await status.boundingBox();
  const composerWhileReadingBox = await page
    .locator(".composer-card")
    .boundingBox();
  expect(statusWhileReadingBox).not.toBeNull();
  expect(composerWhileReadingBox).not.toBeNull();
  expect(statusWhileReadingBox?.x).toBeGreaterThanOrEqual(
    composerWhileReadingBox?.x ?? 0,
  );
  expect(
    (statusWhileReadingBox?.y ?? 0) + (statusWhileReadingBox?.height ?? 0),
  ).toBeLessThanOrEqual(
    (composerWhileReadingBox?.y ?? 0) + (composerWhileReadingBox?.height ?? 0),
  );

  await page.getByRole("button", { name: "Jump to latest message" }).click();
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.documentElement.scrollHeight -
          window.scrollY -
          window.innerHeight,
      ),
    )
    .toBeLessThanOrEqual(12);
  await expect(
    page.getByRole("button", { name: "Jump to latest message" }),
  ).toHaveCount(0);

  const sentWhileFollowing = await request.post(
    `${apiOrigin}/products/ai-agent/messages`,
    {
      data: {
        flowId,
        content: "follow this newer content",
        planMode: false,
      },
    },
  );
  expect(sentWhileFollowing.status()).toBe(202);
  await expect(
    page
      .locator(".message-bubble.assistant")
      .filter({ hasText: "Local demo response: follow this newer content" }),
  ).toHaveCount(1);
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.documentElement.scrollHeight -
          window.scrollY -
          window.innerHeight,
      ),
    )
    .toBeLessThanOrEqual(12);
  await expect(
    page.getByRole("button", { name: "Jump to latest message" }),
  ).toHaveCount(0);
});

test("renders Plan progress, clears an accepted input, and shows safe tool activity", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const snapshotStatuses: number[] = [];
  const answerStatuses: number[] = [];
  const answerBodies: unknown[] = [];
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (path === "/products/ai-agent/snapshot") {
      snapshotStatuses.push(response.status());
    }
    if (path === "/products/ai-agent/questions/answer") {
      answerStatuses.push(response.status());
    }
  });
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/products/ai-agent/questions/answer"
    ) {
      answerBodies.push(request.postDataJSON());
    }
  });

  await startAgent(page);
  const composer = page.getByRole("textbox", { name: "Message" });

  await page.getByRole("checkbox", { name: "Plan mode" }).check();
  await composer.fill("verify full-stack planning");
  await page.getByRole("button", { name: "Create plan" }).click();
  const plan = page.getByRole("region", { name: "Agent plan" });
  await expect(plan.getByText("Plan revision 1")).toBeVisible({
    timeout: 20_000,
  });
  await expect(plan.locator("xpath=ancestor::aside")).toHaveCount(1);
  await expect(plan.locator(".plan-tasks li")).toHaveCount(2);

  await page.emulateMedia({ reducedMotion: "reduce" });
  await plan.getByRole("button", { name: "Execute plan" }).click();
  await expect(plan.getByRole("button", { name: /execution/i })).toBeDisabled();
  const spinner = plan.getByLabel("In progress").first();
  await expect(spinner).toBeVisible();
  await expect(spinner.locator(".task-spinner")).toHaveCSS(
    "animation-name",
    "none",
  );
  await expect(plan.getByRole("heading", { name: "Completed" })).toBeVisible({
    timeout: 20_000,
  });
  await expect(plan.locator(".plan-tasks li.completed")).toHaveCount(2);
  const activity = page.locator(".activity-entry");
  await expect(
    activity.filter({ hasText: "Started plan task 1." }),
  ).toBeVisible();
  await expect(
    activity.filter({ hasText: "Completed plan task 2." }),
  ).toBeVisible();

  await composer.fill("/questions");
  await page.getByRole("button", { name: "Send" }).click();
  const inputCard = page.locator(".pending-input");
  await expect(
    inputCard.getByText("Which region should I use?", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    inputCard.getByText("Which region should I use?", { exact: true }),
  ).toBeVisible();
  await inputCard.getByRole("button", { name: /^US West/u }).click();
  const regionDetails = inputCard.getByRole("textbox", {
    name: "Additional details for Region",
  });
  await expect(regionDetails).toBeVisible();
  await regionDetails.fill("Depart 2026-07-01, return 2026-07-18");
  await inputCard.getByRole("button", { name: "Next" }).click();
  await expect(
    inputCard.getByText("How quickly should I proceed?", { exact: true }),
  ).toBeVisible();
  await inputCard.getByRole("button", { name: /^Fast/u }).click();
  await inputCard.getByRole("button", { name: "Next" }).click();
  await expect(
    inputCard.getByText("Which output format should I use?", { exact: true }),
  ).toBeVisible();
  await inputCard.getByRole("button", { name: /^Other/u }).click();
  await inputCard
    .getByRole("textbox", { name: "Other answer for Format" })
    .fill("Checklist");
  await inputCard.getByRole("button", { name: /^Pace/u }).click();
  await inputCard.getByRole("button", { name: /^Careful/u }).click();
  await inputCard.getByRole("button", { name: /^Format/u }).click();
  const snapshotsBeforeAnswer = snapshotStatuses.length;
  const acceptedAnswer = page.waitForResponse(
    (response) =>
      response.status() === 202 &&
      new URL(response.url()).pathname ===
        "/products/ai-agent/questions/answer",
  );
  await page.getByRole("button", { name: "Submit all" }).click();
  await acceptedAnswer;
  await expect(inputCard).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Submit all" })).toHaveCount(0);
  await expect
    .poll(() => snapshotStatuses.length)
    .toBeGreaterThan(snapshotsBeforeAnswer);
  expect(answerStatuses).toEqual([202]);
  expect(
    answerBodies.filter(
      (body) =>
        typeof body === "object" &&
        body !== null &&
        "answers" in body &&
        JSON.stringify(body.answers) ===
          JSON.stringify([
            {
              questionId: "region",
              answer: "US West: Depart 2026-07-01, return 2026-07-18",
            },
            { questionId: "pace", answer: "Careful" },
            { questionId: "format", answer: "Checklist" },
          ]) &&
        "callId" in body &&
        typeof body.callId === "string" &&
        body.callId.length > 0 &&
        !("messageId" in body),
    ),
  ).toHaveLength(1);
  await expect(
    page.locator(".message-bubble.user").filter({ hasText: "Checklist" }),
  ).toHaveCount(1);
  await expect(
    page.locator(".message-bubble.user").filter({ hasText: "2026-07-18" }),
  ).toHaveCount(1);
  await expect(
    page
      .locator(".message-bubble.assistant")
      .filter({ hasText: "Local demo response:" })
      .filter({ hasText: "Checklist" }),
  ).toHaveCount(1);

  await composer.fill('/tool fixture__echo {"value":"approved"}');
  await page.getByRole("button", { name: "Send" }).click();
  const approval = page.locator(".approval-card");
  await expect(approval.getByText("Approval required")).toBeVisible();
  await expect(
    approval.getByRole("heading", { name: "fixture__echo" }),
  ).toBeVisible();
  await expect(approval.locator("pre")).toContainText('"value":"approved"');
  await page.reload();
  await expect(
    approval.getByRole("heading", { name: "fixture__echo" }),
  ).toBeVisible();
  await approval.getByRole("button", { name: "Approve" }).click();
  await expect(approval).toHaveCount(0);
  await expect(
    page
      .locator(".message-bubble.tool")
      .filter({ hasText: '"echo":"approved"' }),
  ).toBeVisible();
  await expect(
    activity.filter({ hasText: "Model requested fixture__echo." }),
  ).toBeVisible();
  await expect(
    activity.filter({ hasText: "Calling fixture__echo (attempt 1)." }),
  ).toBeVisible();
  await expect(
    activity.filter({ hasText: "Completed fixture__echo." }),
  ).toBeVisible();
  const activityText = (await activity.allTextContents()).join("\n");
  expect(activityText).not.toContain('"value":"approved"');
  expect(activityText).not.toContain('"echo":"approved"');

  await composer.fill('/tool fixture__echo {"value":"rejected"}');
  await page.getByRole("button", { name: "Send" }).click();
  await expect(approval.getByText("Approval required")).toBeVisible();
  await approval.getByRole("button", { name: "Reject" }).click();
  await expect(approval).toHaveCount(0);
  await expect(
    page
      .locator(".message-bubble.tool")
      .filter({ hasText: "rejected_by_user" }),
  ).toBeVisible();
});

test("disables busy Plan actions and continues a stalled active Plan", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const executeStatuses: number[] = [];
  const executeBodies: { flowId?: string; revision?: number }[] = [];
  const snapshotStatuses: number[] = [];
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (path === "/products/ai-agent/plans/execute") {
      executeStatuses.push(response.status());
    }
    if (path === "/products/ai-agent/snapshot") {
      snapshotStatuses.push(response.status());
    }
  });
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/products/ai-agent/plans/execute"
    ) {
      executeBodies.push(
        request.postDataJSON() as { flowId?: string; revision?: number },
      );
    }
  });

  await startAgent(page);
  const composer = page.getByRole("textbox", { name: "Message" });
  await page.getByRole("checkbox", { name: "Plan mode" }).check();
  await composer.fill("/plan-slow-stop verify recovery boundary");
  await page.getByRole("button", { name: "Create plan" }).click();

  const plan = page.getByRole("region", { name: "Agent plan" });
  const activity = page.locator(".activity-entry");
  await expect(plan.getByText("Plan revision 1")).toBeVisible({
    timeout: 20_000,
  });
  const callsBeforeExecution = await activity
    .filter({ hasText: "Calling mock/dex." })
    .count();
  const snapshotsBeforeExecution = snapshotStatuses.length;

  const firstAccepted = page.waitForResponse(
    (response) =>
      response.status() === 202 &&
      new URL(response.url()).pathname === "/products/ai-agent/plans/execute",
  );
  await plan.getByRole("button", { name: "Execute plan" }).click();
  await firstAccepted;
  const busyAction = plan.getByRole("button", {
    name: /Requesting execution|Execution requested|Plan running…/u,
  });
  await expect(busyAction).toBeDisabled();
  await busyAction.evaluate((element) => {
    (element as HTMLButtonElement).click();
  });

  const continueAction = plan.getByRole("button", { name: "Continue plan" });
  await expect(continueAction).toBeEnabled({ timeout: 20_000 });
  await expect
    .poll(() => activity.filter({ hasText: "Calling mock/dex." }).count())
    .toBe(callsBeforeExecution + 2);
  expect(executeStatuses).toEqual([202]);
  expect(executeBodies).toEqual([{ flowId: expect.any(String), revision: 1 }]);
  expect(snapshotStatuses.length).toBeGreaterThan(snapshotsBeforeExecution);
  const flowId = executeBodies[0]?.flowId;
  if (flowId === undefined) throw new Error("missing Plan Flow ID");

  await page.reload();
  await expect(page.getByRole("heading", { name: "SuperAgent" })).toBeVisible();
  await expect(plan.getByText("Plan revision 1")).toBeVisible();
  await expect(continueAction).toBeEnabled({ timeout: 20_000 });
  const beforeContinue = await readAgentSnapshot(page, flowId);
  if (beforeContinue.description === null) {
    throw new Error("Plan Flow became terminal before Continue");
  }
  const sequenceBeforeContinue = beforeContinue.description.lastSequence;

  const snapshotsBeforeContinue = snapshotStatuses.length;
  const continued = page.waitForResponse(
    (response) =>
      response.status() === 202 &&
      new URL(response.url()).pathname === "/products/ai-agent/plans/execute",
  );
  await continueAction.focus();
  await expect(continueAction).toBeFocused();
  await continueAction.press("Enter");
  await continued;
  await expect(
    plan.getByRole("button", {
      name: /Requesting execution|Execution requested|Plan running…/u,
    }),
  ).toBeDisabled();
  await expect
    .poll(() => snapshotStatuses.length)
    .toBeGreaterThan(snapshotsBeforeContinue);
  await expect(continueAction).toBeEnabled({ timeout: 20_000 });
  await expect
    .poll(async () => {
      const snapshot = await readAgentSnapshot(page, flowId);
      return snapshot.description?.lastSequence ?? sequenceBeforeContinue;
    })
    .toBeGreaterThan(sequenceBeforeContinue);
  expect(executeStatuses).toEqual([202, 202]);
  expect(executeBodies).toEqual([
    { flowId: expect.any(String), revision: 1 },
    { flowId: expect.any(String), revision: 1 },
  ]);

  let shouldInjectRace = true;
  await page.route("**/products/ai-agent/plans/execute", async (route) => {
    if (shouldInjectRace) {
      shouldInjectRace = false;
      const queued = await page.request.post(
        `${apiOrigin}/products/ai-agent/messages`,
        {
          data: {
            flowId,
            content: "replace the Plan before the stale Continue arrives",
            planMode: true,
          },
        },
      );
      expect(queued.status()).toBe(202);
      await expect
        .poll(async () => {
          const response = await page.request.get(
            `${apiOrigin}/products/ai-agent/snapshot?flowId=${flowId}`,
          );
          const body = (await response.json()) as {
            description?: { plan?: { revision?: number } | null } | null;
          };
          return body.description?.plan?.revision;
        })
        .toBe(2);
    }
    await route.continue();
  });
  const snapshotsBeforeConflict = snapshotStatuses.length;
  const staleConflict = page.waitForResponse(
    (response) =>
      response.status() === 409 &&
      new URL(response.url()).pathname === "/products/ai-agent/plans/execute",
  );
  await continueAction.click();
  await staleConflict;
  await expect(page.getByRole("alert")).toContainText(
    "the Agent is not at an executable wait or the Plan revision changed",
  );
  await expect
    .poll(() => snapshotStatuses.length)
    .toBeGreaterThan(snapshotsBeforeConflict);
  expect(executeStatuses).toEqual([202, 202, 409]);
  await page.unroute("**/products/ai-agent/plans/execute");
});

test("shows a collapsed Plan above the conversation on a narrow screen", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await startAgent(page);
  const composer = page.getByRole("textbox", { name: "Message" });
  await page.getByRole("checkbox", { name: "Plan mode" }).check();
  await composer.fill("narrow layout objective");
  await page.getByRole("button", { name: "Create plan" }).click();

  const plan = page.getByRole("region", { name: "Agent plan" });
  const toggle = plan.getByRole("button", { name: /Plan · 0\/2 complete/u });
  await expect(toggle).toHaveAttribute("aria-expanded", "false", {
    timeout: 20_000,
  });
  await expect(plan.getByText("Complete the objective:")).toHaveCount(0);
  const planBox = await plan.boundingBox();
  const historyBox = await page
    .getByRole("region", { name: "Conversation history" })
    .boundingBox();
  expect(planBox).not.toBeNull();
  expect(historyBox).not.toBeNull();
  expect(planBox?.y ?? Number.POSITIVE_INFINITY).toBeLessThan(
    historyBox?.y ?? Number.NEGATIVE_INFINITY,
  );

  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(
    plan.getByText("Complete the objective: narrow layout objective"),
  ).toBeVisible();

  await page.getByRole("button", { name: "Start another agent" }).click();
  await expect(
    page.getByRole("heading", { name: "Start a SuperAgent" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Start agent" }).click();
  await expect(page.getByRole("heading", { name: "SuperAgent" })).toBeVisible();
  await composer.fill("/questions");
  await composer.press("Control+Enter");
  const questions = page.getByRole("region", { name: "Agent questions" });
  await expect(questions).toBeVisible();
  const firstChoice = questions.getByRole("button", { name: /^US West/u });
  await firstChoice.focus();
  await firstChoice.press("Enter");
  const narrowDetails = questions.getByRole("textbox", {
    name: "Additional details for Region",
  });
  await expect(narrowDetails).toBeVisible();
  await narrowDetails.fill("Near Seattle");
  await questions.getByRole("button", { name: "Next" }).click();
  await questions.getByRole("button", { name: /^Careful/u }).click();
  await questions.getByRole("button", { name: "Next" }).click();
  await questions.getByRole("button", { name: /^Summary/u }).click();
  const submit = questions.getByRole("button", { name: "Submit all" });
  await submit.focus();
  await expect(submit).toBeFocused();
  await submit.press("Enter");
  await expect(questions).toHaveCount(0);
  await expect(
    page.locator(".message-bubble.user").filter({ hasText: "Summary" }),
  ).toHaveCount(1);
});

test("keeps narrow queue actions visible and restores keyboard focus", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 390, height: 844 });
  await startAgent(page);
  const composer = page.getByRole("textbox", { name: "Message" });
  await composer.fill("/wait 90 narrow queue controls");
  await composer.press("Control+Enter");
  await expect(page.getByText("90s")).toBeVisible();
  const queue = page.getByRole("region", { name: "Message queue" });
  const longEditMessage =
    "narrow edit message whose complete content is only available through Edit";
  const queueMessages = [longEditMessage, "narrow delete", "narrow steer"];
  for (const content of queueMessages) {
    await composer.fill(content);
    await expect(page.getByRole("button", { name: "Send" })).toBeEnabled();
    await composer.press("Control+Enter");
  }
  await expect(queue).toContainText("3 queued · 0 steering");
  await expectMessageQueueExpanded(queue);
  for (const content of queueMessages) {
    await expect(
      queue.locator(".queue-message").filter({ hasText: content }),
    ).toBeVisible();
  }

  const editRow = queue
    .locator(".queue-message")
    .filter({ hasText: longEditMessage });
  const deleteRow = queue
    .locator(".queue-message")
    .filter({ hasText: "narrow delete" });
  const steerRow = queue
    .locator(".queue-message")
    .filter({ hasText: "narrow steer" });
  const edit = editRow.getByRole("button", { name: "Edit" });
  const remove = deleteRow.getByRole("button", { name: "Delete" });
  const steer = steerRow.getByRole("button", { name: "Steer now" });
  const truncatedContent = editRow.locator("p");
  await expect(truncatedContent).toHaveCSS("white-space", "nowrap");
  await expect(truncatedContent).toHaveCSS("text-overflow", "ellipsis");
  await expect
    .poll(() =>
      truncatedContent.evaluate(
        (element) => element.scrollWidth > element.clientWidth,
      ),
    )
    .toBe(true);
  for (const control of [edit, remove, steer]) {
    await control.scrollIntoViewIfNeeded();
    await expect(control).toBeInViewport();
    await expect.poll(() => isControlUnobscured(control)).toBe(true);
  }

  await edit.focus();
  await edit.press("Enter");
  await expect(composer).toHaveValue(longEditMessage);
  await expect(composer).toBeFocused();
  await expect(editRow).toHaveCount(0);
  await composer.fill("narrow edited replacement");
  await composer.press("Control+Enter");
  await expect(queue.getByText("narrow edited replacement")).toBeVisible();

  await expect(remove).toBeEnabled();
  await remove.focus();
  await remove.press("Enter");
  await expect(deleteRow).toHaveCount(0);
  await expect(composer).toBeFocused();

  await expect(steer).toBeEnabled();
  await steer.focus();
  await steer.press("Enter");
  await expect(page.getByText("90s")).toHaveCount(0);
  await expect(
    page.locator(".message-bubble.user").filter({ hasText: "narrow steer" }),
  ).toHaveCount(1);
  await expect(composer).toBeFocused();
});

async function startAgent(page: Page): Promise<void> {
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
}

async function directTimelineText(history: Locator): Promise<string[]> {
  return history
    .locator(":scope > article, :scope > details")
    .allTextContents();
}

async function expectMessageQueueExpanded(queue: Locator): Promise<void> {
  const toggle = queue.getByRole("button", { name: /Message queue/ });
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
}

async function directTimelineTimes(history: Locator): Promise<number[]> {
  return history
    .locator(":scope > article, :scope > details")
    .evaluateAll((rows) =>
      rows.map((row) => {
        const dateTime = row.querySelector("time")?.getAttribute("datetime");
        if (dateTime === undefined || dateTime === null) {
          throw new Error("timeline row is missing a datetime");
        }
        return Date.parse(dateTime);
      }),
    );
}

async function abortSuccessfulResponseOnce(
  page: Page,
  path: string,
  expectedStatus: number,
): Promise<void> {
  let shouldAbort = true;
  await page.route(`**${path}`, async (route) => {
    if (!shouldAbort) {
      await route.continue();
      return;
    }
    shouldAbort = false;
    const response = await route.fetch();
    expect(response.status()).toBe(expectedStatus);
    await route.abort("failed");
  });
}

async function consumeBeforeBrowserRequest(
  page: Page,
  path: string,
  expectedStatus: number,
): Promise<void> {
  let shouldConsume = true;
  await page.route(`**${path}`, async (route) => {
    if (!shouldConsume) {
      await route.continue();
      return;
    }
    shouldConsume = false;
    const body = route.request().postData();
    if (body === null) throw new Error(`missing request body for ${path}`);
    const first = await page.request.post(`${apiOrigin}${path}`, {
      data: body,
      headers: { "Content-Type": "application/json" },
    });
    expect(first.status()).toBe(expectedStatus);
    await route.continue();
  });
}

async function fillQuestionBatch(questions: Locator): Promise<void> {
  await questions.getByRole("button", { name: /^US West/u }).click();
  await questions.getByRole("button", { name: "Next" }).click();
  await questions.getByRole("button", { name: /^Careful/u }).click();
  await questions.getByRole("button", { name: "Next" }).click();
  await questions.getByRole("button", { name: /^Summary/u }).click();
}

async function displayedFlowID(page: Page): Promise<string> {
  const flowId = await page
    .locator(".flow-identity code")
    .first()
    .textContent();
  if (flowId === null || flowId === "") throw new Error("missing Flow ID");
  return flowId;
}

async function readAgentSnapshot(
  page: Page,
  flowId: string,
): Promise<AgentSnapshot> {
  const response = await page.request.get(
    `${apiOrigin}/products/ai-agent/snapshot?flowId=${encodeURIComponent(flowId)}`,
  );
  expect(response.status()).toBe(200);
  return (await response.json()) as AgentSnapshot;
}

async function pendingMessageID(
  page: Page,
  flowId: string,
  content: string,
): Promise<string> {
  let messageId: string | undefined;
  await expect
    .poll(async () => {
      const snapshot = await readAgentSnapshot(page, flowId);
      messageId = snapshot.queued.find(
        (message) => message.value.content === content,
      )?.messageId;
      return messageId;
    })
    .not.toBeUndefined();
  if (messageId === undefined)
    throw new Error(`missing queued message ${content}`);
  return messageId;
}

async function isControlUnobscured(control: Locator): Promise<boolean> {
  return control.evaluate((element) => {
    const box = element.getBoundingClientRect();
    const covering = document.elementFromPoint(
      box.left + box.width / 2,
      box.top + box.height / 2,
    );
    return covering === element || element.contains(covering);
  });
}

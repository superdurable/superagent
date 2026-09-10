/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { expect, test, type Locator, type Page } from "@playwright/test";

const apiOrigin =
  process.env["SUPERAGENT_E2E_API_ORIGIN"] ?? "http://127.0.0.1:8080";

test("renders chronological transient activity and durable queue interactions", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const snapshots: number[] = [];
  const commandStatuses: number[] = [];
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

  const composer = page.getByRole("textbox", { name: "Message" });
  await composer.fill("/reason Checked the constraints | Durable answer");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Submitting…")).toBeVisible();

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
  await expect(history.locator(".activity-entry")).toHaveCount(2);
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
  await page.getByRole("checkbox", { name: "Plan mode" }).check();
  await composer.fill("edit this queued message");
  await page.getByRole("button", { name: "Create plan" }).click();
  await composer.fill("delete this queued message");
  await expect(page.getByRole("button", { name: "Send" })).toBeEnabled();
  await composer.press("Control+Enter");

  const queue = page.getByRole("region", { name: "Message queue" });
  await expect(queue).toContainText("2 queued · 0 steering");
  await expect(queue.getByText("Plan", { exact: true })).toBeVisible();
  await expect(queue.getByText("Chat", { exact: true })).toBeVisible();
  expect(await queue.locator(".queue-message p").allTextContents()).toEqual([
    "edit this queued message",
    "delete this queued message",
  ]);

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
  await composer.fill("");
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
        messageId: `archive-ui-message-${String(index)}`,
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
  await expect(plan.getByText("Plan revision 1")).toBeVisible();
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
  await inputCard.getByRole("button", { name: /^US West/u }).click();
  await expect(
    inputCard.getByText("How quickly should I proceed?", { exact: true }),
  ).toBeVisible();
  await inputCard.getByRole("button", { name: /^Fast/u }).click();
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
            { questionId: "region", answer: "US West" },
            { questionId: "pace", answer: "Careful" },
            { questionId: "format", answer: "Checklist" },
          ]) &&
        "callId" in body &&
        typeof body.callId === "string" &&
        body.callId.length > 0 &&
        "messageId" in body &&
        typeof body.messageId === "string" &&
        body.messageId.length > 0,
    ),
  ).toHaveLength(1);
  await expect(
    page.locator(".message-bubble.user").filter({ hasText: "Checklist" }),
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
    activity.filter({ hasText: "Running fixture__echo." }),
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
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
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
  await questions.getByRole("button", { name: /^Careful/u }).click();
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

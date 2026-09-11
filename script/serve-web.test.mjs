/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { after, before, test } from "node:test";

import { createWebServer } from "./serve-web.mjs";

let origin;
let root;
let server;

before(async () => {
  root = await mkdtemp(path.join(tmpdir(), "superagent-web-"));
  await Promise.all([
    writeFile(
      path.join(root, "index.html"),
      "<!doctype html><title>SuperAgent</title>",
    ),
    writeFile(
      path.join(root, "config.json"),
      '{"apiOrigin":"http://127.0.0.1:8080"}',
    ),
    writeFile(path.join(root, "main.0123456789abcdef.js"), "export {};"),
  ]);
  server = createWebServer(root);
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  assert.notEqual(address, null);
  assert.equal(typeof address, "object");
  origin = `http://127.0.0.1:${String(address.port)}`;
});

after(async () => {
  if (server?.listening === true) {
    await new Promise((resolve, reject) => {
      server.close((error) => {
        if (error === undefined) resolve();
        else reject(error);
      });
    });
  }
  if (root !== undefined) await rm(root, { recursive: true });
});

test("serves entrypoint and runtime configuration without stale caching", async () => {
  const [entrypoint, config, asset] = await Promise.all([
    fetch(`${origin}/`),
    fetch(`${origin}/config.json`),
    fetch(`${origin}/main.0123456789abcdef.js`),
  ]);

  assert.equal(entrypoint.status, 200);
  assert.equal(
    entrypoint.headers.get("cache-control"),
    "no-cache, max-age=0, must-revalidate",
  );
  assert.equal(config.headers.get("cache-control"), "no-store");
  assert.equal(
    asset.headers.get("cache-control"),
    "public, max-age=31536000, immutable",
  );
});

test("rejects traversal and unsupported methods", async () => {
  const traversal = await fetch(`${origin}/..%2Foutside.txt`);
  const mutation = await fetch(`${origin}/index.html`, { method: "POST" });

  assert.equal(traversal.status, 404);
  assert.equal(mutation.status, 405);
  assert.equal(mutation.headers.get("allow"), "GET, HEAD");
});

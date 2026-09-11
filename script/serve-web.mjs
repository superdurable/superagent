/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer } from "node:http";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

const contentTypes = new Map([
  [".css", "text/css; charset=utf-8"],
  [".html", "text/html; charset=utf-8"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".map", "application/json; charset=utf-8"],
  [".svg", "image/svg+xml"],
  [".txt", "text/plain; charset=utf-8"],
]);
const immutableAsset = /\.[a-f0-9]{16}\.(?:css|js)$/;

export function createWebServer(directory) {
  const root = path.resolve(directory);
  return createServer(async (request, response) => {
    if (request.method !== "GET" && request.method !== "HEAD") {
      response.writeHead(405, { Allow: "GET, HEAD" });
      response.end();
      return;
    }
    let pathname;
    try {
      pathname = decodeURIComponent(
        new URL(request.url ?? "/", "http://localhost").pathname,
      );
    } catch {
      response.writeHead(400);
      response.end();
      return;
    }
    const relativePath = pathname === "/" ? "index.html" : pathname.slice(1);
    const filename = path.resolve(root, relativePath);
    const relative = path.relative(root, filename);
    if (
      relative === ".." ||
      relative.startsWith(`..${path.sep}`) ||
      path.isAbsolute(relative)
    ) {
      response.writeHead(404);
      response.end();
      return;
    }
    let metadata;
    try {
      metadata = await stat(filename);
    } catch {
      response.writeHead(404);
      response.end();
      return;
    }
    if (!metadata.isFile()) {
      response.writeHead(404);
      response.end();
      return;
    }
    response.writeHead(200, {
      "Cache-Control": cacheControl(relativePath),
      "Content-Length": String(metadata.size),
      "Content-Type":
        contentTypes.get(path.extname(filename)) ?? "application/octet-stream",
      "X-Content-Type-Options": "nosniff",
    });
    if (request.method === "HEAD") {
      response.end();
      return;
    }
    const stream = createReadStream(filename);
    stream.on("error", () => {
      response.destroy();
    });
    stream.pipe(response);
  });
}

function cacheControl(relativePath) {
  if (relativePath === "config.json") return "no-store";
  if (immutableAsset.test(relativePath)) {
    return "public, max-age=31536000, immutable";
  }
  return "no-cache, max-age=0, must-revalidate";
}

function parseArguments(arguments_) {
  const options = {
    directory: "web/dist",
    host: "127.0.0.1",
    port: 3000,
  };
  for (let index = 0; index < arguments_.length; index += 2) {
    const name = arguments_[index];
    const value = arguments_[index + 1];
    if (value === undefined) throw new Error(`missing value for ${name}`);
    switch (name) {
      case "--directory":
        options.directory = value;
        break;
      case "--host":
        options.host = value;
        break;
      case "--port": {
        const port = Number(value);
        if (!Number.isInteger(port) || port < 1 || port > 65_535) {
          throw new Error("--port must be an integer from 1 through 65535");
        }
        options.port = port;
        break;
      }
      default:
        throw new Error(`unknown argument ${name}`);
    }
  }
  return options;
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const server = createWebServer(options.directory);
  const close = () => {
    server.close((error) => {
      if (error !== undefined) {
        console.error(error);
        process.exitCode = 1;
      }
    });
  };
  process.on("SIGINT", close);
  process.on("SIGTERM", close);
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(options.port, options.host, resolve);
  });
  console.log(
    `Serving ${path.resolve(options.directory)} at http://${options.host}:${String(options.port)}/`,
  );
}

const entrypoint = process.argv[1];
if (
  entrypoint !== undefined &&
  pathToFileURL(path.resolve(entrypoint)).href === import.meta.url
) {
  await main();
}

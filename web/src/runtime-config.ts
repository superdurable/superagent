/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

const runtimeConfigFilename = "config.json";

export class APIOrigin {
  readonly value: string;

  private constructor(value: string) {
    this.value = value;
  }

  static parse(value: unknown): APIOrigin {
    if (typeof value !== "string" || value.trim() !== value || value === "") {
      throw new Error(
        "apiOrigin must be a non-empty string without surrounding whitespace.",
      );
    }
    let parsed: URL;
    try {
      parsed = new URL(value);
    } catch {
      throw new Error("apiOrigin must be an absolute HTTP or HTTPS origin.");
    }
    if (
      (parsed.protocol !== "https:" && parsed.protocol !== "http:") ||
      parsed.username !== "" ||
      parsed.password !== "" ||
      parsed.pathname !== "/" ||
      parsed.search !== "" ||
      parsed.hash !== ""
    ) {
      throw new Error(
        "apiOrigin must not contain credentials, a path, a query, or a fragment.",
      );
    }
    if (parsed.protocol === "http:" && !isLoopbackHostname(parsed.hostname)) {
      throw new Error("apiOrigin must use HTTPS unless its host is loopback.");
    }
    return new APIOrigin(parsed.origin);
  }
}

export interface RuntimeConfig {
  apiOrigin: APIOrigin;
}

export function parseRuntimeConfig(value: unknown): RuntimeConfig {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("Runtime configuration must be a JSON object.");
  }
  if (Object.keys(value).length !== 1 || !("apiOrigin" in value)) {
    throw new Error("Runtime configuration must contain only apiOrigin.");
  }
  return { apiOrigin: APIOrigin.parse(value.apiOrigin) };
}

export async function loadRuntimeConfig(
  signal: AbortSignal,
): Promise<RuntimeConfig> {
  const response = await fetch(
    new URL(runtimeConfigFilename, document.baseURI),
    {
      cache: "no-store",
      credentials: "omit",
      signal,
    },
  );
  if (!response.ok) {
    throw new Error(
      `Runtime configuration returned HTTP ${String(response.status)}.`,
    );
  }
  if (
    response.headers
      .get("Content-Type")
      ?.split(";", 1)[0]
      ?.trim()
      .toLowerCase() !== "application/json"
  ) {
    throw new Error("Runtime configuration must use application/json.");
  }
  const value: unknown = await response.json();
  return parseRuntimeConfig(value);
}

function isLoopbackHostname(hostname: string): boolean {
  return (
    hostname === "localhost" ||
    hostname.startsWith("127.") ||
    hostname === "[::1]"
  );
}

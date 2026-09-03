/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  APIOrigin,
  loadRuntimeConfig,
  parseRuntimeConfig,
} from "./runtime-config";

describe("runtime configuration", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("normalizes an HTTPS API origin", () => {
    expect(APIOrigin.parse("https://API.Example.com/").value).toBe(
      "https://api.example.com",
    );
  });

  it("allows loopback HTTP for local development", () => {
    expect(APIOrigin.parse("http://127.0.0.1:8080").value).toBe(
      "http://127.0.0.1:8080",
    );
  });

  it.each([
    "http://api.example.com",
    "https://user:secret@api.example.com",
    "https://api.example.com/v1",
    "https://api.example.com?region=us",
  ])("rejects unsafe API origin %s", (value) => {
    expect(() => APIOrigin.parse(value)).toThrow();
  });

  it("rejects missing and unknown runtime fields", () => {
    expect(() => parseRuntimeConfig({})).toThrow();
    expect(() =>
      parseRuntimeConfig({ apiOrigin: "https://api.example.com", extra: true }),
    ).toThrow();
  });

  it("loads uncached configuration without browser credentials", async () => {
    const response = new Response(
      JSON.stringify({ apiOrigin: "https://api.example.com" }),
      { headers: { "Content-Type": "application/json; charset=utf-8" } },
    );
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(response);
    vi.stubGlobal("fetch", fetchMock);
    const controller = new AbortController();

    const config = await loadRuntimeConfig(controller.signal);

    expect(config.apiOrigin.value).toBe("https://api.example.com");
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledWith(
      new URL("config.json", document.baseURI),
      {
        cache: "no-store",
        credentials: "omit",
        signal: controller.signal,
      },
    );
  });

  it("rejects a non-JSON runtime response", async () => {
    const response = new Response("<html></html>", {
      headers: { "Content-Type": "text/html" },
    });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(response));

    await expect(
      loadRuntimeConfig(new AbortController().signal),
    ).rejects.toThrow("application/json");
  });
});

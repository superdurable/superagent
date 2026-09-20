/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

const ANSI_ESCAPE = new RegExp(
  `${String.fromCharCode(27)}\\[[0-9;]*[A-Za-z]`,
  "g",
);

export function stripAnsi(value: string): string {
  return value.replace(ANSI_ESCAPE, "");
}

export function formatShellCommand(argv: readonly string[]): string {
  return argv.map(shellQuote).join(" ");
}

function shellQuote(argument: string): string {
  if (argument === "") return "''";
  if (/^[A-Za-z0-9_./:=+-]+$/.test(argument)) return argument;
  return `'${argument.replace(/'/g, `'\\''`)}'`;
}

export function projectCommandOutput(content: string): string {
  const parsed = parseJsonObject(content);
  if (parsed === null) return stripAnsi(content);
  const stdout = readOutputProjection(parsed["stdout"]);
  const stderr = readOutputProjection(parsed["stderr"]);
  const parts = [stdout, stderr].filter((part) => part.length > 0);
  if (parts.length === 0) return stripAnsi(content);
  return parts.join("\n");
}

function readOutputProjection(value: unknown): string {
  if (typeof value === "string") return stripAnsi(value);
  if (value === null || typeof value !== "object") return "";
  const record = value as Record<string, unknown>;
  const head = typeof record["head"] === "string" ? record["head"] : "";
  const tail = typeof record["tail"] === "string" ? record["tail"] : "";
  const omitted =
    typeof record["omitted_bytes"] === "number" && record["omitted_bytes"] > 0
      ? `\n… ${String(record["omitted_bytes"])} bytes omitted …\n`
      : head.length > 0 && tail.length > 0
        ? "\n…\n"
        : "";
  return stripAnsi(`${head}${omitted}${tail}`);
}

export function parseJsonObject(
  content: string,
): Record<string, unknown> | null {
  try {
    const value: unknown = JSON.parse(content);
    if (value === null || typeof value !== "object" || Array.isArray(value)) {
      return null;
    }
    return value as Record<string, unknown>;
  } catch {
    return null;
  }
}

export function parseToolArguments(
  argumentsJson: string,
): Record<string, unknown> | null {
  return parseJsonObject(argumentsJson);
}

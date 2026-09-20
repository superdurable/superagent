/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

export type DiffLineKind = "context" | "add" | "remove" | "header" | "meta";

export interface DiffLine {
  kind: DiffLineKind;
  text: string;
  oldLineNumber: number | null;
  newLineNumber: number | null;
}

export interface PatchFileDiff {
  path: string;
  added: number;
  removed: number;
  lines: DiffLine[];
}

export function parsePatchDiff(patch: string): PatchFileDiff[] | null {
  const trimmed = patch.trim();
  if (trimmed.length === 0) return null;
  if (trimmed.includes("*** Begin Patch")) {
    return parseCodexPatch(trimmed);
  }
  if (trimmed.startsWith("--- ") || trimmed.includes("\n--- ")) {
    return parseUnifiedPatch(trimmed);
  }
  return null;
}

function parseUnifiedPatch(patch: string): PatchFileDiff[] {
  const files: PatchFileDiff[] = [];
  let current: PatchFileDiff | null = null;
  let oldLine = 0;
  let newLine = 0;
  for (const rawLine of patch.split(/\r?\n/)) {
    if (rawLine.startsWith("--- ")) {
      current = null;
      continue;
    }
    if (rawLine.startsWith("+++ ")) {
      const path = stripPathPrefix(rawLine.slice(4).trim());
      current = { path, added: 0, removed: 0, lines: [] };
      files.push(current);
      continue;
    }
    if (current === null) continue;
    if (rawLine.startsWith("@@")) {
      const match = /@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(rawLine);
      if (match) {
        oldLine = Number(match[1]);
        newLine = Number(match[2]);
      }
      current.lines.push({
        kind: "header",
        text: rawLine,
        oldLineNumber: null,
        newLineNumber: null,
      });
      continue;
    }
    if (rawLine.startsWith("+")) {
      current.added += 1;
      current.lines.push({
        kind: "add",
        text: rawLine.slice(1),
        oldLineNumber: null,
        newLineNumber: newLine,
      });
      newLine += 1;
      continue;
    }
    if (rawLine.startsWith("-")) {
      current.removed += 1;
      current.lines.push({
        kind: "remove",
        text: rawLine.slice(1),
        oldLineNumber: oldLine,
        newLineNumber: null,
      });
      oldLine += 1;
      continue;
    }
    if (rawLine.startsWith(" ") || rawLine === "") {
      const text = rawLine.startsWith(" ") ? rawLine.slice(1) : rawLine;
      current.lines.push({
        kind: "context",
        text,
        oldLineNumber: oldLine,
        newLineNumber: newLine,
      });
      oldLine += 1;
      newLine += 1;
    }
  }
  return files.filter((file) => file.lines.length > 0 || file.path.length > 0);
}

function parseCodexPatch(patch: string): PatchFileDiff[] {
  const files: PatchFileDiff[] = [];
  let current: PatchFileDiff | null = null;
  let oldLine = 1;
  let newLine = 1;
  for (const rawLine of patch.split(/\r?\n/)) {
    if (rawLine.startsWith("*** Add File:")) {
      current = {
        path: rawLine.slice("*** Add File:".length).trim(),
        added: 0,
        removed: 0,
        lines: [],
      };
      files.push(current);
      oldLine = 1;
      newLine = 1;
      continue;
    }
    if (rawLine.startsWith("*** Update File:")) {
      current = {
        path: rawLine.slice("*** Update File:".length).trim(),
        added: 0,
        removed: 0,
        lines: [],
      };
      files.push(current);
      oldLine = 1;
      newLine = 1;
      continue;
    }
    if (rawLine.startsWith("*** Delete File:")) {
      current = {
        path: rawLine.slice("*** Delete File:".length).trim(),
        added: 0,
        removed: 1,
        lines: [
          {
            kind: "remove",
            text: "(deleted)",
            oldLineNumber: null,
            newLineNumber: null,
          },
        ],
      };
      files.push(current);
      current = null;
      continue;
    }
    if (current === null) continue;
    if (rawLine.startsWith("@@")) {
      const match = /@@(?: -(\d+))?/.exec(rawLine);
      if (match?.[1] !== undefined) {
        oldLine = Number(match[1]);
        newLine = oldLine;
      }
      current.lines.push({
        kind: "header",
        text: rawLine,
        oldLineNumber: null,
        newLineNumber: null,
      });
      continue;
    }
    if (rawLine.startsWith("+")) {
      current.added += 1;
      current.lines.push({
        kind: "add",
        text: rawLine.slice(1),
        oldLineNumber: null,
        newLineNumber: newLine,
      });
      newLine += 1;
      continue;
    }
    if (rawLine.startsWith("-")) {
      current.removed += 1;
      current.lines.push({
        kind: "remove",
        text: rawLine.slice(1),
        oldLineNumber: oldLine,
        newLineNumber: null,
      });
      oldLine += 1;
      continue;
    }
    if (rawLine.startsWith(" ")) {
      current.lines.push({
        kind: "context",
        text: rawLine.slice(1),
        oldLineNumber: oldLine,
        newLineNumber: newLine,
      });
      oldLine += 1;
      newLine += 1;
    }
  }
  return files;
}

function stripPathPrefix(path: string): string {
  if (path.startsWith("a/") || path.startsWith("b/")) return path.slice(2);
  return path;
}

export function summarizePatchFiles(files: readonly PatchFileDiff[]): string {
  if (files.length === 0) return "Edited files";
  if (files.length === 1) {
    const file = files[0];
    if (file === undefined) return "Edited files";
    return `Edited \`${file.path}\` +${String(file.added)} -${String(file.removed)}`;
  }
  const added = files.reduce((sum, file) => sum + file.added, 0);
  const removed = files.reduce((sum, file) => sum + file.removed, 0);
  return `Edited ${String(files.length)} files +${String(added)} -${String(removed)}`;
}

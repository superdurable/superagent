/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import type { ReactNode } from "react";

import {
  parsePatchDiff,
  summarizePatchFiles,
  type DiffLine,
  type PatchFileDiff,
} from "./parsePatchDiff.js";
import type { TimelineToolCall } from "./conversationTimeline.js";
import type { ToolCallResultView } from "./pairToolCalls.js";
import {
  formatShellCommand,
  parseToolArguments,
  projectCommandOutput,
} from "./toolPayload.js";

const APPLY_PATCH = "apply_patch";
const EXEC_SHORT = "exec_short_command";
const EXEC_LONG = "exec_long_command";

export interface ToolCallCardProps {
  call: TimelineToolCall;
  result?: ToolCallResultView | null;
  defaultOpen?: boolean;
  renderToolCall?: (
    call: TimelineToolCall,
    result: ToolCallResultView | null,
  ) => ReactNode;
}

export function ToolCallCard({
  call,
  result = null,
  defaultOpen = false,
  renderToolCall,
}: ToolCallCardProps) {
  if (renderToolCall !== undefined) {
    return <>{renderToolCall(call, result)}</>;
  }
  if (call.name === APPLY_PATCH) {
    return (
      <ApplyPatchCard call={call} result={result} defaultOpen={defaultOpen} />
    );
  }
  if (call.name === EXEC_SHORT || call.name === EXEC_LONG) {
    return (
      <ExecCommandCard call={call} result={result} defaultOpen={defaultOpen} />
    );
  }
  return (
    <GenericToolCard call={call} result={result} defaultOpen={defaultOpen} />
  );
}

function ApplyPatchCard({
  call,
  result,
  defaultOpen,
}: {
  call: TimelineToolCall;
  result: ToolCallResultView | null;
  defaultOpen: boolean;
}) {
  const argumentsObject = parseToolArguments(call.argumentsJson);
  const patch =
    typeof argumentsObject?.["patch"] === "string"
      ? argumentsObject["patch"]
      : "";
  const files = parsePatchDiff(patch);
  if (files === null) {
    return (
      <GenericToolCard call={call} result={result} defaultOpen={defaultOpen} />
    );
  }
  const summary = summarizePatchFiles(files);
  return (
    <details className="sa-tool-call" open={defaultOpen}>
      <summary className="sa-tool-call-summary">
        <span>{summary}</span>
        <span className="sa-tool-call-chevron" aria-hidden="true">
          ▾
        </span>
      </summary>
      <div className="sa-tool-call-body">
        {files.map((file) => (
          <ApplyPatchDiff key={file.path} file={file} />
        ))}
        {result !== null && <PatchResultFootnote content={result.content} />}
        {result === null && (
          <p className="sa-tool-call-pending">Waiting for tool result…</p>
        )}
      </div>
    </details>
  );
}

export function ApplyPatchDiff({ file }: { file: PatchFileDiff }) {
  return (
    <div className="sa-patch-diff">
      <div className="sa-patch-diff-header">
        <code>{file.path}</code>
        <span className="sa-patch-diff-stats">
          <span className="sa-patch-added">+{String(file.added)}</span>{" "}
          <span className="sa-patch-removed">-{String(file.removed)}</span>
        </span>
      </div>
      <pre className="sa-patch-diff-body">
        {file.lines.map((line, index) => (
          <DiffLineRow key={`${file.path}:${String(index)}`} line={line} />
        ))}
      </pre>
    </div>
  );
}

function DiffLineRow({ line }: { line: DiffLine }) {
  const marker = line.kind === "add" ? "+" : line.kind === "remove" ? "-" : " ";
  const lineNumber =
    line.kind === "add"
      ? line.newLineNumber
      : line.kind === "remove"
        ? line.oldLineNumber
        : (line.newLineNumber ?? line.oldLineNumber);
  return (
    <span className={`sa-patch-line sa-patch-line--${line.kind}`}>
      <span className="sa-patch-gutter">{lineNumber ?? ""}</span>
      <span className="sa-patch-marker">{marker}</span>
      <span className="sa-patch-text">{line.text}</span>
    </span>
  );
}

function PatchResultFootnote({ content }: { content: string }) {
  const paths = readChangedPaths(content);
  if (paths.length === 0) return null;
  return (
    <p className="sa-tool-call-footnote">
      Changed {paths.map((path) => `\`${path}\``).join(", ")}
    </p>
  );
}

function readChangedPaths(content: string): string[] {
  try {
    const value: unknown = JSON.parse(content);
    if (value === null || typeof value !== "object") return [];
    const paths = (value as Record<string, unknown>)["changed_paths"];
    if (!Array.isArray(paths)) return [];
    return paths.filter((path): path is string => typeof path === "string");
  } catch {
    return [];
  }
}

export function ExecCommandCard({
  call,
  result = null,
  defaultOpen = false,
}: {
  call: TimelineToolCall;
  result?: ToolCallResultView | null;
  defaultOpen?: boolean;
}) {
  const argumentsObject = parseToolArguments(call.argumentsJson);
  const argv = Array.isArray(argumentsObject?.["argv"])
    ? argumentsObject["argv"].filter(
        (entry): entry is string => typeof entry === "string",
      )
    : [];
  const command =
    argv.length > 0 ? formatShellCommand(argv) : call.argumentsJson;
  const summary =
    argv.length > 0 ? `Ran ${argv[0] ?? "command"}` : `Ran ${call.name}`;
  const output = result === null ? null : projectCommandOutput(result.content);
  return (
    <details className="sa-tool-call" open={defaultOpen}>
      <summary className="sa-tool-call-summary">
        <span>{summary}</span>
        <span className="sa-tool-call-chevron" aria-hidden="true">
          ▾
        </span>
      </summary>
      <div className="sa-tool-call-body">
        <pre className="sa-exec-command">
          <span className="sa-exec-prompt">$</span>{" "}
          <HighlightedShell command={command} />
        </pre>
        {output !== null && (
          <pre className="sa-exec-output">{output || "(no output)"}</pre>
        )}
        {result === null && (
          <p className="sa-tool-call-pending">Waiting for tool result…</p>
        )}
      </div>
    </details>
  );
}

function HighlightedShell({ command }: { command: string }) {
  const tokens = command.split(/(\s+|&&|\|\||\||;)/);
  return (
    <span className="sa-exec-tokens">
      {tokens.map((token, index) => {
        if (token.trim() === "") {
          return <span key={String(index)}>{token}</span>;
        }
        if (
          token === "&&" ||
          token === "||" ||
          token === "|" ||
          token === ";"
        ) {
          return (
            <span className="sa-exec-operator" key={String(index)}>
              {token}
            </span>
          );
        }
        if (token.startsWith("-")) {
          return (
            <span className="sa-exec-flag" key={String(index)}>
              {token}
            </span>
          );
        }
        if (index === 0 || tokens[index - 1]?.trim() === "&&") {
          return (
            <span className="sa-exec-binary" key={String(index)}>
              {token}
            </span>
          );
        }
        return (
          <span className="sa-exec-arg" key={String(index)}>
            {token}
          </span>
        );
      })}
    </span>
  );
}

function GenericToolCard({
  call,
  result,
  defaultOpen,
}: {
  call: TimelineToolCall;
  result: ToolCallResultView | null;
  defaultOpen: boolean;
}) {
  return (
    <details className="sa-tool-call" open={defaultOpen}>
      <summary className="sa-tool-call-summary">
        <span>
          {call.name}
          {result === null ? " · pending" : ""}
        </span>
        <span className="sa-tool-call-chevron" aria-hidden="true">
          ▾
        </span>
      </summary>
      <div className="sa-tool-call-body">
        <p className="sa-tool-call-label">Request</p>
        <pre className="sa-tool-call-json">{call.argumentsJson}</pre>
        {result !== null ? (
          <>
            <p className="sa-tool-call-label">Result</p>
            <pre className="sa-tool-call-json">{result.content}</pre>
          </>
        ) : (
          <p className="sa-tool-call-pending">Waiting for tool result…</p>
        )}
      </div>
    </details>
  );
}

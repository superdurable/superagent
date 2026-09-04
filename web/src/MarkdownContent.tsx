/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { Components } from "react-markdown";
import type { Node, Parent } from "unist";

const components: Components = {
  table({ children }) {
    return (
      <div className="markdown-table-scroll">
        <table>{children}</table>
      </div>
    );
  },
};

const breakTagPattern = /^<br\s*\/?\s*>$/i;

export default function MarkdownContent({ value }: { value: string }) {
  return (
    <div className="markdown-content">
      <ReactMarkdown
        components={components}
        remarkPlugins={[remarkGfm, replaceBreakTags]}
      >
        {value}
      </ReactMarkdown>
    </div>
  );
}

function replaceBreakTags() {
  return (tree: Node): void => {
    replaceBreakTagNodes(tree);
  };
}

function replaceBreakTagNodes(node: Node): void {
  if (!isParent(node)) return;
  for (let index = 0; index < node.children.length; index += 1) {
    const child = node.children[index];
    if (child === undefined) continue;
    if (isBreakTag(child)) {
      node.children[index] = { type: "break" };
      continue;
    }
    replaceBreakTagNodes(child);
  }
}

function isParent(node: Node): node is Parent {
  return "children" in node && Array.isArray(node.children);
}

function isBreakTag(node: Node): node is Node & { value: string } {
  return (
    node.type === "html" &&
    "value" in node &&
    typeof node.value === "string" &&
    breakTagPattern.test(node.value)
  );
}

/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import MarkdownContent from "./MarkdownContent";

describe("MarkdownContent", () => {
  afterEach(cleanup);

  it("renders safe line breaks and scrollable GFM tables", () => {
    const { container } = render(
      <MarkdownContent
        value={
          "First<br>Second\n\n| Region | Action |\n| --- | --- |\n| A | Continue |"
        }
      />,
    );

    expect(container.querySelector("p")).toContainElement(
      container.querySelector("br"),
    );
    expect(container.querySelector("table")?.parentElement).toHaveClass(
      "markdown-table-scroll",
    );
  });

  it("leaves unsafe raw HTML inert", () => {
    const { container } = render(
      <MarkdownContent
        value={'Safe<script type="text/javascript">bad()</script>'}
      />,
    );

    expect(container.querySelector("script")).not.toBeInTheDocument();
    expect(container).toHaveTextContent("Safe");
  });
});

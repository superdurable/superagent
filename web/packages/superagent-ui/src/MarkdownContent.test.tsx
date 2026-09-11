/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { MarkdownContent } from "./MarkdownContent";

describe("MarkdownContent", () => {
  it("renders GFM content with a scrollable table wrapper", () => {
    const { container } = render(
      <MarkdownContent
        value={"| Name | Value |\n| --- | --- |\n| Alpha | **One** |"}
      />,
    );

    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(screen.getByText("One")).toHaveProperty("tagName", "STRONG");
    expect(container.querySelector(".markdown-table-scroll")).toHaveClass(
      "sa-markdown-table-scroll",
    );
  });

  it("keeps unsafe HTML inert while preserving line breaks", () => {
    const { container } = render(
      <MarkdownContent
        value={'first<br>second\n\n<script>alert("x")</script>'}
      />,
    );

    expect(container.querySelector("br")).toBeInTheDocument();
    expect(container.querySelector("script")).not.toBeInTheDocument();
    expect(screen.getByText(/alert/)).toBeInTheDocument();
  });
});

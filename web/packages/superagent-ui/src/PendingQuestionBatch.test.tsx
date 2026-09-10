/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import {
  PendingQuestionBatch,
  type PendingQuestion,
} from "./PendingQuestionBatch";

const regionQuestion = question(
  "region",
  "Region",
  "Which region?",
  "West",
  "East",
);
const questions: readonly PendingQuestion[] = [
  regionQuestion,
  question("pace", "Pace", "Which pace?", "Fast", "Careful"),
  question("format", "Format", "Which format?", "Short", "Detailed"),
];

describe("PendingQuestionBatch", () => {
  it("navigates, revises, and submits all answers atomically", () => {
    const onSubmit = vi.fn();
    render(<PendingQuestionBatch onSubmit={onSubmit} questions={questions} />);

    fireEvent.click(screen.getByRole("button", { name: /^West/u }));
    expect(screen.getByText("Which pace?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Fast/u }));
    expect(screen.getByText("Which format?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Other/u }));
    fireEvent.change(
      screen.getByRole("textbox", { name: "Other answer for Format" }),
      { target: { value: "  Checklist  " } },
    );
    expect(onSubmit).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /^Pace/u }));
    fireEvent.click(screen.getByRole("button", { name: /^Careful/u }));
    fireEvent.click(screen.getByRole("button", { name: "Submit all" }));

    expect(onSubmit).toHaveBeenCalledOnce();
    expect(onSubmit).toHaveBeenCalledWith([
      { questionId: "region", answer: "West" },
      { questionId: "pace", answer: "Careful" },
      { questionId: "format", answer: "Checklist" },
    ]);
  });

  it("supports a single question and exposes submitted loading state", () => {
    const onSubmit = vi.fn();
    const { rerender } = render(
      <PendingQuestionBatch onSubmit={onSubmit} questions={[regionQuestion]} />,
    );

    expect(screen.queryByRole("button", { name: "Next" })).toBeNull();
    expect(screen.getByRole("button", { name: "Submit all" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: /^East/u }));
    fireEvent.click(screen.getByRole("button", { name: "Submit all" }));
    expect(onSubmit).toHaveBeenCalledWith([
      { questionId: "region", answer: "East" },
    ]);

    rerender(
      <PendingQuestionBatch
        isSubmitting
        onSubmit={onSubmit}
        questions={[regionQuestion]}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Submitting answers…" }),
    ).toBeInTheDocument();
  });

  it("prevents navigation and submission while disabled", () => {
    const onSubmit = vi.fn();
    render(
      <PendingQuestionBatch
        disabled
        onSubmit={onSubmit}
        questions={questions}
      />,
    );

    expect(screen.getByRole("group", { name: "Region" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /^West/u })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
    expect(onSubmit).not.toHaveBeenCalled();
  });
});

function question(
  id: string,
  header: string,
  prompt: string,
  first: string,
  second: string,
): PendingQuestion {
  return {
    id,
    header,
    question: prompt,
    options: [
      { label: first, description: `Choose ${first}.` },
      { label: second, description: `Choose ${second}.` },
    ],
  };
}

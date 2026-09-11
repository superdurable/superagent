/*
 * Copyright (c) 2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

"use client";

import { useState } from "react";

export interface PendingQuestionOption {
  label: string;
  description: string;
}

export interface PendingQuestion {
  id: string;
  header: string;
  question: string;
  options: readonly PendingQuestionOption[];
}

export interface PendingQuestionAnswer {
  questionId: string;
  answer: string;
}

export interface PendingQuestionBatchProps {
  questions: readonly PendingQuestion[];
  disabled?: boolean;
  isSubmitting?: boolean;
  onSubmit: (answers: readonly PendingQuestionAnswer[]) => void;
}

interface QuestionDraft {
  selectedOption: string | null;
  detail: string;
  isOther: boolean;
}

export function PendingQuestionBatch({
  questions,
  disabled = false,
  isSubmitting = false,
  onSubmit,
}: PendingQuestionBatchProps) {
  const [currentIndex, setCurrentIndex] = useState(0);
  const [drafts, setDrafts] = useState<Record<string, QuestionDraft>>({});
  const question = questions[currentIndex];
  if (question === undefined) return null;
  const currentDraft = drafts[question.id];
  const hasAllAnswers = questions.every(
    ({ id }) => questionAnswer(drafts[id]) !== "",
  );
  const isLast = currentIndex === questions.length - 1;
  const chooseOption = (selectedOption: string) => {
    setDrafts((current) => ({
      ...current,
      [question.id]: {
        selectedOption,
        detail:
          current[question.id]?.isOther === false &&
          current[question.id]?.selectedOption === selectedOption
            ? (current[question.id]?.detail ?? "")
            : "",
        isOther: false,
      },
    }));
  };
  const chooseOther = () => {
    setDrafts((current) => ({
      ...current,
      [question.id]: {
        selectedOption: null,
        detail:
          current[question.id]?.isOther === true
            ? (current[question.id]?.detail ?? "")
            : "",
        isOther: true,
      },
    }));
  };
  const setDetail = (detail: string) => {
    setDrafts((current) => {
      const draft = current[question.id];
      if (draft === undefined) return current;
      return { ...current, [question.id]: { ...draft, detail } };
    });
  };
  const submit = () => {
    if (!hasAllAnswers || disabled) return;
    onSubmit(
      questions.map(({ id }) => ({
        questionId: id,
        answer: questionAnswer(drafts[id]),
      })),
    );
  };

  return (
    <section
      className="sa-question-batch pending-input"
      aria-label="Agent questions"
    >
      <div className="sa-question-heading question-heading">
        <div>
          <p className="sa-question-eyebrow eyebrow">Agent needs your input</p>
          <strong>
            Question {String(currentIndex + 1)} of {String(questions.length)}
          </strong>
        </div>
        <div className="sa-question-tabs question-tabs" aria-label="Questions">
          {questions.map((candidate, index) => (
            <button
              type="button"
              className={
                index === currentIndex
                  ? "sa-question-button sa-question-button--active active"
                  : "sa-question-button sa-question-button--secondary secondary"
              }
              aria-current={index === currentIndex ? "step" : undefined}
              key={candidate.id}
              onClick={() => {
                setCurrentIndex(index);
              }}
            >
              {candidate.header}
              {questionAnswer(drafts[candidate.id]) !== "" && (
                <span
                  className="sa-question-answered-mark answered-mark"
                  aria-label="Answered"
                >
                  ✓
                </span>
              )}
            </button>
          ))}
        </div>
      </div>
      <fieldset
        className="sa-question-content question-content"
        disabled={disabled}
      >
        <legend>{question.header}</legend>
        <p>{question.question}</p>
        <div className="sa-question-options choice-row">
          {question.options.map((option) => {
            const isSelected =
              currentDraft?.isOther === false &&
              currentDraft.selectedOption === option.label;
            return (
              <button
                type="button"
                className={
                  isSelected
                    ? "sa-question-button sa-question-option sa-question-option--selected question-option selected"
                    : "sa-question-button sa-question-button--secondary sa-question-option question-option secondary"
                }
                key={option.label}
                onClick={() => {
                  chooseOption(option.label);
                }}
              >
                <strong>{option.label}</strong>
                <small>{option.description}</small>
              </button>
            );
          })}
          <button
            type="button"
            className={
              currentDraft?.isOther === true
                ? "sa-question-button sa-question-option sa-question-option--selected question-option selected"
                : "sa-question-button sa-question-button--secondary sa-question-option question-option secondary"
            }
            onClick={() => {
              chooseOther();
            }}
          >
            <strong>Other</strong>
            <small>Enter a different answer.</small>
          </button>
        </div>
        {currentDraft !== undefined && (
          <label className="sa-question-other-answer answer-detail">
            {currentDraft.isOther ? "Your answer" : "Add details (optional)"}
            <input
              aria-label={
                currentDraft.isOther
                  ? `Other answer for ${question.header}`
                  : `Additional details for ${question.header}`
              }
              placeholder={
                currentDraft.isOther
                  ? "Enter your answer…"
                  : "Add dates, constraints, or context…"
              }
              value={currentDraft.detail}
              onChange={(event) => {
                setDetail(event.target.value);
              }}
            />
          </label>
        )}
      </fieldset>
      <div className="sa-question-navigation question-navigation">
        <button
          type="button"
          className="sa-question-button sa-question-button--secondary secondary"
          disabled={disabled || currentIndex === 0}
          onClick={() => {
            setCurrentIndex((index) => Math.max(0, index - 1));
          }}
        >
          Previous
        </button>
        {!isLast && (
          <button
            type="button"
            className="sa-question-button sa-question-button--secondary secondary"
            disabled={disabled || questionAnswer(currentDraft) === ""}
            onClick={() => {
              setCurrentIndex((index) => index + 1);
            }}
          >
            Next
          </button>
        )}
        {isLast && (
          <button
            type="button"
            className="sa-question-button"
            disabled={disabled || !hasAllAnswers}
            onClick={submit}
          >
            {isSubmitting ? "Submitting answers…" : "Submit all"}
          </button>
        )}
      </div>
    </section>
  );
}

function questionAnswer(draft: QuestionDraft | undefined): string {
  if (draft === undefined) return "";
  const detail = draft.detail.trim();
  if (draft.isOther) return detail;
  const selectedOption = draft.selectedOption?.trim() ?? "";
  if (selectedOption === "" || detail === "") return selectedOption;
  return `${selectedOption}: ${detail}`;
}

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
  answer: string;
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
    ({ id }) => (drafts[id]?.answer.trim().length ?? 0) > 0,
  );
  const isLast = currentIndex === questions.length - 1;
  const setAnswer = (answer: string, isOther: boolean) => {
    setDrafts((current) => ({
      ...current,
      [question.id]: { answer, isOther },
    }));
  };
  const chooseOption = (answer: string) => {
    setAnswer(answer, false);
    if (!isLast) setCurrentIndex((index) => index + 1);
  };
  const submit = () => {
    if (!hasAllAnswers || disabled) return;
    onSubmit(
      questions.map(({ id }) => ({
        questionId: id,
        answer: drafts[id]?.answer.trim() ?? "",
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
              {(drafts[candidate.id]?.answer.trim().length ?? 0) > 0 && (
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
              currentDraft.answer === option.label;
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
              setAnswer(
                currentDraft?.isOther === true ? currentDraft.answer : "",
                true,
              );
            }}
          >
            <strong>Other</strong>
            <small>Enter a different answer.</small>
          </button>
        </div>
        {currentDraft?.isOther === true && (
          <label className="sa-question-other-answer other-answer">
            Other answer
            <input
              aria-label={`Other answer for ${question.header}`}
              value={currentDraft.answer}
              onChange={(event) => {
                setAnswer(event.target.value, true);
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
            disabled={disabled || currentDraft?.answer.trim() === ""}
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

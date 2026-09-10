// Copyright (c) 2022-2026 Super Durable, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maximumUserInputQuestions = 3
	maximumUserInputOptions   = 3
	maximumUserMessageLength  = 1_000_000
)

const (
	// ToolNameWriteTodos atomically replaces the durable plan.
	ToolNameWriteTodos ToolName = "write_todos"
	// ToolNameDurableWait creates a Dex Timer wait.
	ToolNameDurableWait ToolName = "durable_wait"
	// ToolNameRequestUserInput creates a durable user question.
	ToolNameRequestUserInput ToolName = "request_user_input"
)

const (
	toolErrorUnknownOrDisabled     toolErrorCode    = "unknown_or_disabled_tool"
	toolErrorMultiplePlanWrites    toolErrorCode    = "multiple_write_todos_calls"
	toolErrorInvalidPlan           toolErrorCode    = "invalid_todos"
	toolErrorInvalidDuration       toolErrorCode    = "invalid_duration_seconds"
	toolErrorInvalidUserInput      toolErrorCode    = "invalid_user_input"
	toolErrorUserInputPending      toolErrorCode    = "user_input_already_pending"
	toolErrorRejectedByUser        toolErrorCode    = "rejected_by_user"
	toolErrorSupersededBySteering  toolErrorCode    = "superseded_by_steered_user_message"
	toolErrorSupersededByUserInput toolErrorCode    = "superseded_by_user_input"
	toolResultStatusFailed         toolResultStatus = "failed"
	toolResultStatusUpdated        toolResultStatus = "updated"
	toolResultStatusCleared        toolResultStatus = "cleared"
	toolResultStatusWaitingForUser toolResultStatus = "waiting_for_user"
	toolResultStatusInterrupted    toolResultStatus = "interrupted"
	toolResultStatusCompleted      toolResultStatus = "completed"
)

type toolErrorCode string
type toolResultStatus string

type writeTodosArguments struct {
	Todos []PlanTask `json:"todos"`
}

type durableWaitArguments struct {
	DurationSeconds int64  `json:"duration_seconds"`
	Reason          string `json:"reason"`
}

type userInputArguments struct {
	Questions []UserInputQuestion `json:"questions"`
}

type toolResultPayload struct {
	Status          toolResultStatus    `json:"status"`
	Error           toolErrorCode       `json:"error,omitempty"`
	Message         string              `json:"message,omitempty"`
	Tool            ToolName            `json:"tool,omitempty"`
	Outcome         ToolOutcome         `json:"outcome,omitempty"`
	ErrorType       string              `json:"error_type,omitempty"`
	Reason          string              `json:"reason,omitempty"`
	Revision        PlanRevision        `json:"revision,omitempty"`
	TaskCount       int                 `json:"task_count,omitempty"`
	Questions       []UserInputQuestion `json:"questions,omitempty"`
	DurationSeconds int64               `json:"duration_seconds,omitempty"`
	Attempts        int                 `json:"attempts,omitempty"`
}

func writeTodosDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        ToolNameWriteTodos,
		Description: "Replace the durable plan with a complete ordered todo list. Use an empty list to clear the plan. Keep statuses accurate as work proceeds.",
		InputSchema: MustJSONObject(`{
			"type":"object",
			"properties":{
				"todos":{
					"type":"array",
					"items":{
						"type":"object",
						"properties":{
							"content":{"type":"string","minLength":1},
							"status":{"type":"string","enum":["pending","in_progress","completed"]}
						},
						"required":["content","status"],
						"additionalProperties":false
					}
				}
			},
			"required":["todos"],
			"additionalProperties":false
		}`),
		MaximumAttempts: 1,
	}
}

func durableWaitDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        ToolNameDurableWait,
		Description: "Wait durably before continuing. A steered user message interrupts the wait.",
		InputSchema: MustJSONObject(`{
			"type":"object",
			"properties":{
				"duration_seconds":{"type":"integer","minimum":1},
				"reason":{"type":"string"}
			},
			"required":["duration_seconds","reason"],
			"additionalProperties":false
		}`),
		MaximumAttempts: 1,
	}
}

func requestUserInputDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        ToolNameRequestUserInput,
		Description: "Pause durably and ask for information required to continue. Keep dependent plan tasks pending until the user answers.",
		InputSchema: MustJSONObject(`{
			"type":"object",
			"properties":{
				"questions":{
					"type":"array",
					"minItems":1,
					"maxItems":3,
					"items":{
						"type":"object",
						"properties":{
							"id":{"type":"string","minLength":1,"description":"Stable identifier unique within this batch."},
							"header":{"type":"string","minLength":1,"maxLength":12,"description":"Short label for navigation."},
							"question":{"type":"string","minLength":1,"description":"One concise question for the user."},
							"options":{
								"type":"array",
								"minItems":2,
								"maxItems":3,
								"items":{
									"type":"object",
									"properties":{
										"label":{"type":"string","minLength":1},
										"description":{"type":"string","minLength":1}
									},
									"required":["label","description"],
									"additionalProperties":false
								}
							}
						},
						"required":["id","header","question","options"],
						"additionalProperties":false
					}
				}
			},
			"required":["questions"],
			"additionalProperties":false
		}`),
		MaximumAttempts: 1,
	}
}

func planTasks(call ToolCall) ([]PlanTask, error) {
	var arguments writeTodosArguments
	if err := decodeWriteTodosArguments(call, &arguments); err != nil {
		return nil, err
	}
	for index, task := range arguments.Todos {
		arguments.Todos[index].Content = strings.TrimSpace(task.Content)
		if err := validateTask(arguments.Todos[index], index); err != nil {
			return nil, err
		}
	}
	return arguments.Todos, nil
}

func decodeWriteTodosArguments(call ToolCall, arguments *writeTodosArguments) error {
	return decodeStrictToolObject(call, func(decoder *json.Decoder) error {
		return decoder.Decode(arguments)
	})
}

func durableWaitArgumentsFor(call ToolCall) (durableWaitArguments, error) {
	var arguments durableWaitArguments
	err := decodeStrictToolObject(call, func(decoder *json.Decoder) error {
		return decoder.Decode(&arguments)
	})
	if err != nil {
		return durableWaitArguments{}, err
	}
	arguments.Reason = strings.TrimSpace(arguments.Reason)
	if arguments.DurationSeconds <= 0 {
		return durableWaitArguments{}, errors.New("duration_seconds must be positive")
	}
	return arguments, nil
}

func userInputArgumentsFor(call ToolCall) (userInputArguments, error) {
	var arguments userInputArguments
	err := decodeStrictToolObject(call, func(decoder *json.Decoder) error {
		return decoder.Decode(&arguments)
	})
	if err != nil {
		return userInputArguments{}, err
	}
	questions, err := validateUserInputQuestions(arguments.Questions)
	if err != nil {
		return userInputArguments{}, err
	}
	arguments.Questions = questions
	return arguments, nil
}

func decodeStrictToolObject(call ToolCall, decode func(*json.Decoder) error) error {
	decoder := json.NewDecoder(bytes.NewBufferString(call.Arguments.String()))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decode(decoder); err != nil {
		return fmt.Errorf("tool %q has invalid arguments: %w", call.Name, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("tool %q has trailing arguments", call.Name)
		}
		return fmt.Errorf("tool %q has invalid arguments: %w", call.Name, err)
	}
	return nil
}

func validateUserInputQuestions(values []UserInputQuestion) ([]UserInputQuestion, error) {
	if len(values) == 0 || len(values) > maximumUserInputQuestions {
		return nil, fmt.Errorf("questions must contain 1-%d values", maximumUserInputQuestions)
	}
	questions := make([]UserInputQuestion, 0, len(values))
	seenIDs := make(map[UserInputQuestionID]struct{}, len(values))
	for index, value := range values {
		value.ID = UserInputQuestionID(strings.TrimSpace(string(value.ID)))
		value.Header = strings.TrimSpace(value.Header)
		value.Question = strings.TrimSpace(value.Question)
		if value.ID == "" || value.Header == "" || value.Question == "" {
			return nil, fmt.Errorf("question %d requires id, header, and question", index+1)
		}
		if utf8.RuneCountInString(value.Header) > 12 {
			return nil, fmt.Errorf("question %d header must contain at most 12 characters", index+1)
		}
		if _, found := seenIDs[value.ID]; found {
			return nil, fmt.Errorf("question IDs must be unique: %q", value.ID)
		}
		seenIDs[value.ID] = struct{}{}
		options, err := validateUserInputOptions(value.Options, index)
		if err != nil {
			return nil, err
		}
		value.Options = options
		questions = append(questions, value)
	}
	return questions, nil
}

func validateUserInputOptions(values []UserInputOption, questionIndex int) ([]UserInputOption, error) {
	if len(values) < 2 || len(values) > maximumUserInputOptions {
		return nil, fmt.Errorf("question %d options must contain 2-%d values", questionIndex+1, maximumUserInputOptions)
	}
	options := make([]UserInputOption, 0, len(values))
	seenLabels := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.Label = strings.TrimSpace(value.Label)
		value.Description = strings.TrimSpace(value.Description)
		if value.Label == "" || value.Description == "" {
			return nil, fmt.Errorf("question %d options require label and description", questionIndex+1)
		}
		if _, found := seenLabels[value.Label]; found {
			return nil, fmt.Errorf("question %d option labels must be unique", questionIndex+1)
		}
		seenLabels[value.Label] = struct{}{}
		options = append(options, value)
	}
	return options, nil
}

func answeredUserMessage(pending PendingUserInput, answers []UserInputAnswer) (UserMessage, error) {
	if len(answers) != len(pending.Questions) {
		return UserMessage{}, errors.New("answers must contain exactly one value for every question")
	}
	byQuestion := make(map[UserInputQuestionID]string, len(answers))
	for _, answer := range answers {
		answer.QuestionID = UserInputQuestionID(strings.TrimSpace(string(answer.QuestionID)))
		answer.Answer = strings.TrimSpace(answer.Answer)
		if answer.QuestionID == "" || answer.Answer == "" {
			return UserMessage{}, errors.New("answers require question_id and answer")
		}
		if _, found := byQuestion[answer.QuestionID]; found {
			return UserMessage{}, fmt.Errorf("question %q was answered more than once", answer.QuestionID)
		}
		byQuestion[answer.QuestionID] = answer.Answer
	}
	var content strings.Builder
	for index, question := range pending.Questions {
		answer, found := byQuestion[question.ID]
		if !found {
			return UserMessage{}, fmt.Errorf("question %q is missing an answer", question.ID)
		}
		delete(byQuestion, question.ID)
		if index > 0 {
			content.WriteString("\n\n")
		}
		fmt.Fprintf(&content, "**%s**: %s", question.Header, answer)
	}
	if len(byQuestion) > 0 {
		return UserMessage{}, errors.New("answers contain an unknown question ID")
	}
	if content.Len() > maximumUserMessageLength {
		return UserMessage{}, fmt.Errorf("combined answer exceeds %d characters", maximumUserMessageLength)
	}
	callID := pending.CallID
	return UserMessage{Content: content.String(), AnsweredInputCallID: &callID}, nil
}

func encodeToolResult(payload toolResultPayload, outcome ToolOutcome, isError bool) (ToolExecutionResult, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ToolExecutionResult{}, fmt.Errorf("encode tool result: %w", err)
	}
	return ToolExecutionResult{Content: string(encoded), Outcome: outcome, IsError: isError}, nil
}

func toolSafeCompactionCutoff(messages []AgentMessage, firstSequence Sequence, cutoff Sequence) Sequence {
	pending := make(map[CallID]Sequence)
	for offset, message := range messages {
		sequence := firstSequence + Sequence(offset)
		for _, call := range message.ToolCalls {
			pending[call.ID] = sequence
		}
		if message.Role == MessageRoleTool && message.ToolCallID != nil {
			delete(pending, *message.ToolCallID)
		}
	}
	if len(pending) == 0 {
		return cutoff
	}
	sequences := make([]Sequence, 0, len(pending))
	for _, sequence := range pending {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(left, right int) bool { return sequences[left] < sequences[right] })
	if candidate := sequences[0] - 1; candidate < cutoff {
		return candidate
	}
	return cutoff
}

func sequenceKey(sequence Sequence) string {
	return fmt.Sprintf("%020d", sequence)
}

func planRevisionKey(revision PlanRevision) string {
	return fmt.Sprintf("%d", revision)
}

func projectMessage(message AgentMessage, maxContextTokens int) AgentMessage {
	maximumCharacters := maxContextTokens * 4 / 5
	if maximumCharacters < 1_000 {
		maximumCharacters = 1_000
	}
	if len(message.Content) <= maximumCharacters {
		return message
	}
	message.Content = message.Content[:maximumCharacters] + "\n[Content truncated in the model context; the durable message is complete.]"
	return message
}

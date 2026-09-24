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
	"strings"
	"testing"
)

func TestSequenceKeyUsesUnpaddedDecimal(t *testing.T) {
	tests := map[Sequence]string{
		1:                     "1",
		11:                    "11",
		9_007_199_254_740_993: "9007199254740993",
	}
	for sequence, expected := range tests {
		if actual := sequenceKey(sequence); actual != expected {
			t.Fatalf("sequenceKey(%d) = %q, want %q", sequence, actual, expected)
		}
	}
}

func TestToolSafeCompactionCutoffKeepsCallWithItsResult(t *testing.T) {
	callID := CallID("call-1")
	messages := []AgentMessage{
		{Role: MessageRoleUser, Content: "request"},
		{Role: MessageRoleAssistant, ToolCalls: []ToolCall{{ID: callID, Name: "search"}}},
		{Role: MessageRoleTool, ToolCallID: &callID, Content: "result"},
	}
	if got := toolSafeCompactionCutoff(messages, 10, 12); got != 12 {
		t.Fatalf("completed call cutoff = %d", got)
	}
	if got := toolSafeCompactionCutoff(messages[:2], 10, 11); got != 10 {
		t.Fatalf("pending call cutoff = %d", got)
	}
}

func TestPlanTasksValidatesStatusAndTrimsContent(t *testing.T) {
	tasks, err := planTasks(ToolCall{
		Name:      ToolNameWriteTodos,
		Arguments: MustJSONObject(`{"todos":[{"content":"  first  ","status":"pending"}]}`),
	})
	if err != nil {
		t.Fatalf("planTasks() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].Content != "first" {
		t.Fatalf("planTasks() = %+v", tasks)
	}
	_, err = planTasks(ToolCall{
		Name:      ToolNameWriteTodos,
		Arguments: MustJSONObject(`{"todos":[{"content":"first","status":"unknown"}]}`),
	})
	if err == nil {
		t.Fatal("planTasks() error = nil")
	}
	for _, arguments := range []string{`{}`, `{"todos":null}`, `{"todos":[{"content":"   ","status":"pending"}]}`} {
		if _, err := planTasks(ToolCall{Name: ToolNameWriteTodos, Arguments: MustJSONObject(arguments)}); err == nil {
			t.Fatalf("planTasks(%s) error = nil", arguments)
		}
	}
}

func TestUserInputQuestionsEnforceBatchShape(t *testing.T) {
	questions, err := validateUserInputQuestions([]UserInputQuestion{{
		ID:       " region ",
		Header:   " Region ",
		Question: " Where? ",
		Options: []UserInputOption{
			{Label: " West ", Description: " Use west. "},
			{Label: " East ", Description: " Use east. "},
		},
	}})
	if err != nil || len(questions) != 1 || questions[0].ID != "region" || questions[0].Options[0].Label != "West" {
		t.Fatalf("validateUserInputQuestions() = %#v, %v", questions, err)
	}
	invalid := []UserInputQuestion{{
		ID:       "region",
		Header:   strings.Repeat("界", maximumUserInputHeaderCharacters+1),
		Question: "Where?",
		Options:  []UserInputOption{{Label: "West", Description: "Use west."}},
	}}
	if _, err := validateUserInputQuestions(invalid); err == nil {
		t.Fatal("validateUserInputQuestions() error = nil")
	}
	duplicateIDs := []UserInputQuestion{
		{ID: "region", Header: "Region", Question: "Where?", Options: []UserInputOption{{Label: "West", Description: "Use west."}, {Label: "East", Description: "Use east."}}},
		{ID: " region ", Header: "Backup", Question: "Again?", Options: []UserInputOption{{Label: "West", Description: "Use west."}, {Label: "East", Description: "Use east."}}},
	}
	if _, err := validateUserInputQuestions(duplicateIDs); err == nil {
		t.Fatal("duplicate question ID error = nil")
	}
	duplicateLabels := []UserInputQuestion{{
		ID: "region", Header: "Region", Question: "Where?",
		Options: []UserInputOption{{Label: "West", Description: "Use west."}, {Label: " West ", Description: "Still west."}},
	}}
	if _, err := validateUserInputQuestions(duplicateLabels); err == nil {
		t.Fatal("duplicate option label error = nil")
	}
}

func TestUserInputArgumentsUseGeneratedContractBeforeSemanticValidation(t *testing.T) {
	arguments, err := userInputArgumentsFor(ToolCall{
		Name: ToolNameRequestUserInput,
		Arguments: MustJSONObject(`{
			"questions":[{
				"id":" region ",
				"header":" Region ",
				"question":" Where? ",
				"options":[
					{"label":" West ","description":" Use west. "},
					{"label":" East ","description":" Use east. "}
				]
			}]
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments.Questions) != 1 || arguments.Questions[0].ID != "region" ||
		arguments.Questions[0].Options[0].Label != "West" {
		t.Fatalf("userInputArgumentsFor() = %#v", arguments)
	}

	for name, encoded := range map[string]string{
		"duplicate IDs":    `{"questions":[{"id":"region","header":"Region","question":"Where?","options":[{"label":"West","description":"West."},{"label":"East","description":"East."}]},{"id":" region ","header":"Backup","question":"Again?","options":[{"label":"West","description":"West."},{"label":"East","description":"East."}]}]}`,
		"duplicate labels": `{"questions":[{"id":"region","header":"Region","question":"Where?","options":[{"label":"West","description":"West."},{"label":" West ","description":"Still west."}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := userInputArgumentsFor(ToolCall{
				Name:      ToolNameRequestUserInput,
				Arguments: MustJSONObject(encoded),
			}); err == nil {
				t.Fatal("userInputArgumentsFor() error = nil")
			}
		})
	}
}

func TestDurableWaitArgumentsUseGeneratedContractAndTrimReason(t *testing.T) {
	arguments, err := durableWaitArgumentsFor(ToolCall{
		Name:      ToolNameDurableWait,
		Arguments: MustJSONObject(`{"duration_seconds":1,"reason":" retry "}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if arguments.DurationSeconds != 1 || arguments.Reason != "retry" {
		t.Fatalf("durableWaitArgumentsFor() = %#v", arguments)
	}
}

func TestAnsweredUserMessageRequiresExactQuestionSet(t *testing.T) {
	pending := PendingUserInput{
		CallID: "call-1",
		Questions: []UserInputQuestion{
			{ID: "region", Header: "Region"},
			{ID: "pace", Header: "Pace"},
		},
	}
	message, err := answeredUserMessage(pending, []UserInputAnswer{
		{QuestionID: "pace", Answer: "Relaxed"},
		{QuestionID: "region", Answer: "West"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "**Region**: West\n\n**Pace**: Relaxed" ||
		message.AnsweredInputCallID == nil || *message.AnsweredInputCallID != "call-1" {
		t.Fatalf("answered message = %#v", message)
	}
	if _, err := answeredUserMessage(pending, []UserInputAnswer{{QuestionID: "region", Answer: "West"}}); err == nil {
		t.Fatal("missing answer error = nil")
	}
}

func TestBuiltinToolArgumentsRejectUnknownFields(t *testing.T) {
	_, err := durableWaitArgumentsFor(ToolCall{
		Name:      ToolNameDurableWait,
		Arguments: MustJSONObject(`{"duration_seconds":1,"reason":"test","unexpected":true}`),
	})
	if err == nil {
		t.Fatal("durableWaitArgumentsFor() error = nil")
	}
}

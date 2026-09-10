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

import "testing"

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
		Header:   "Header longer than twelve",
		Question: "Where?",
		Options:  []UserInputOption{{Label: "West", Description: "Use west."}},
	}}
	if _, err := validateUserInputQuestions(invalid); err == nil {
		t.Fatal("validateUserInputQuestions() error = nil")
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

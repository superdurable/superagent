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

func TestUserInputChoicesEnforcesBoundsAndUniqueness(t *testing.T) {
	choices, err := validateUserInputChoices([]string{" yes ", "no"})
	if err != nil || len(choices) != 2 || choices[0] != "yes" {
		t.Fatalf("userInputChoices() = %v, %v", choices, err)
	}
	for _, invalid := range [][]string{
		{"only"},
		{"same", "same"},
		{"", "valid"},
	} {
		if _, err := validateUserInputChoices(invalid); err == nil {
			t.Fatalf("validateUserInputChoices(%v) error = nil", invalid)
		}
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

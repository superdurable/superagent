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

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/superdurable/superagent/internal/agent"
)

func TestMockClientPlansAndWaitsLikePython(t *testing.T) {
	client := NewMockClient()
	tests := []struct {
		name       string
		request    string
		forcedTool agent.ToolName
		tools      []agent.ToolDefinition
		wantTool   agent.ToolName
	}{
		{
			name:       "forced plan",
			request:    "ship the release",
			forcedTool: agent.ToolNameWriteTodos,
			tools:      []agent.ToolDefinition{{Name: agent.ToolNameWriteTodos}},
			wantTool:   agent.ToolNameWriteTodos,
		},
		{
			name:     "durable wait",
			request:  "/wait 12 check the ticket sale",
			tools:    []agent.ToolDefinition{{Name: agent.ToolNameDurableWait}},
			wantTool: agent.ToolNameDurableWait,
		},
		{
			name:     "input choices",
			request:  "/choose Region? | us | eu",
			tools:    []agent.ToolDefinition{{Name: agent.ToolNameRequestUserInput}},
			wantTool: agent.ToolNameRequestUserInput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reply, err := client.Complete(context.Background(), agent.ModelRequest{
				Config:         agent.NewAgentConfig(),
				Messages:       []agent.AgentMessage{{Role: agent.MessageRoleUser, Content: test.request}},
				Tools:          test.tools,
				WriteAssistant: discardText,
				WriteReasoning: discardText,
				WriteActivity:  discardActivity,
				ForcedTool:     test.forcedTool,
				FlowID:         agent.FlowID("flow-1"),
			})
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Name != test.wantTool {
				t.Fatalf("Complete() calls = %+v", reply.ToolCalls)
			}
			if _, err := agent.ParseJSONObject(reply.ToolCalls[0].Arguments.String()); err != nil {
				t.Fatalf("arguments are invalid JSON: %v", err)
			}
		})
	}
}

func TestMockClientProducesStableCallIDsForRetries(t *testing.T) {
	request := agent.ModelRequest{
		Config:         agent.NewAgentConfig(),
		Messages:       []agent.AgentMessage{{Role: agent.MessageRoleUser, Content: "/wait 1 test"}},
		Tools:          []agent.ToolDefinition{{Name: agent.ToolNameDurableWait}},
		WriteAssistant: discardText,
		WriteReasoning: discardText,
		WriteActivity:  discardActivity,
		FlowID:         agent.FlowID("stable-flow"),
	}
	first, err := NewMockClient().Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("first Complete() error = %v", err)
	}
	second, err := NewMockClient().Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("second Complete() error = %v", err)
	}
	if first.ToolCalls[0].ID != second.ToolCalls[0].ID {
		t.Fatalf("call IDs differ: %q != %q", first.ToolCalls[0].ID, second.ToolCalls[0].ID)
	}
}

func TestMockClientStreamsVisibleResponse(t *testing.T) {
	client := NewMockClient()
	var streamed strings.Builder
	reply, err := client.Complete(context.Background(), agent.ModelRequest{
		Config:   agent.NewAgentConfig(),
		Messages: []agent.AgentMessage{{Role: agent.MessageRoleUser, Content: "hello"}},
		WriteAssistant: func(chunk string) error {
			_, err := streamed.WriteString(chunk)
			return err
		},
		WriteReasoning: discardText,
		WriteActivity:  discardActivity,
		FlowID:         agent.FlowID("flow-1"),
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if streamed.String() != reply.Content || reply.Content != "Local demo response: hello" {
		t.Fatalf("stream = %q, reply = %q", streamed.String(), reply.Content)
	}
}

func TestMockPlanAdvancesOneTaskAtATime(t *testing.T) {
	tasks := []agent.PlanTask{
		{Content: "one", Status: agent.TaskStatusPending},
		{Content: "two", Status: agent.TaskStatusPending},
	}
	first := nextMockPlanTasks(tasks)
	if first[0].Status != agent.TaskStatusInProgress || first[1].Status != agent.TaskStatusPending {
		t.Fatalf("first = %+v", first)
	}
	second := nextMockPlanTasks(first)
	if second[0].Status != agent.TaskStatusCompleted || second[1].Status != agent.TaskStatusInProgress {
		t.Fatalf("second = %+v", second)
	}
}

func discardText(string) error {
	return nil
}

func discardActivity(agent.AgentEvent) error {
	return nil
}

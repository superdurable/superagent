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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/superdurable/superagent/internal/agent"
)

// MockClient provides deterministic credential-free local behavior.
type MockClient struct{}

// NewMockClient creates the local model adapter.
func NewMockClient() *MockClient {
	return &MockClient{}
}

// Complete implements the Python mock/dex command language.
func (*MockClient) Complete(ctx context.Context, request agent.ModelRequest) (agent.ModelReply, error) {
	if request.WriteAssistant == nil || request.WriteReasoning == nil || request.WriteActivity == nil {
		return agent.ModelReply{}, errors.New("mock model writers are required")
	}
	available := make(map[agent.ToolName]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		available[tool.Name] = struct{}{}
	}
	calls := mockCallFactory{flowID: request.FlowID, messageCount: len(request.Messages)}
	if request.ForcedTool != "" {
		if _, found := available[request.ForcedTool]; !found {
			return agent.ModelReply{}, fmt.Errorf("forced tool %q is not available", request.ForcedTool)
		}
		if request.ForcedTool != agent.ToolNameWriteTodos {
			return agent.ModelReply{}, fmt.Errorf("mock/dex cannot force tool %q", request.ForcedTool)
		}
		userRequest := lastUserContent(request.Messages)
		if userRequest == "" {
			userRequest = "the requested objective"
		}
		tasks := []agent.PlanTask{}
		if strings.ToLower(userRequest) != "/plan-clear" {
			tasks = []agent.PlanTask{
				{Content: "Complete the objective: " + userRequest, Status: agent.TaskStatusPending},
				{Content: "Verify and report the result", Status: agent.TaskStatusPending},
			}
		}
		arguments, err := writeTodosObject(tasks)
		if err != nil {
			return agent.ModelReply{}, err
		}
		return calls.toolReply(
			"I will prepare a plan for review.",
			agent.ToolNameWriteTodos,
			arguments,
			request.WriteActivity,
		)
	}

	userRequest := lastUserContent(request.Messages)
	if strings.ToLower(userRequest) == "/plan-clear" && hasTool(available, agent.ToolNameWriteTodos) {
		arguments, err := writeTodosObject([]agent.PlanTask{})
		if err != nil {
			return agent.ModelReply{}, err
		}
		return calls.toolReply(
			"I will clear the current plan.",
			agent.ToolNameWriteTodos,
			arguments,
			request.WriteActivity,
		)
	}
	if strings.HasPrefix(strings.ToLower(userRequest), "/choose ") && hasTool(available, agent.ToolNameRequestUserInput) {
		parts := splitChoiceCommand(userRequest)
		if len(parts) < 3 {
			return agent.ModelReply{}, errors.New("local /choose syntax is /choose <prompt> | <choice> | <choice>")
		}
		arguments, err := userInputObject(parts[0], parts[1:])
		if err != nil {
			return agent.ModelReply{}, err
		}
		return calls.toolReply(
			"Please choose an option so I can continue.",
			agent.ToolNameRequestUserInput,
			arguments,
			request.WriteActivity,
		)
	}
	if strings.HasPrefix(strings.ToLower(userRequest), "/ask-many ") &&
		hasTool(available, agent.ToolNameRequestUserInput) && hasTool(available, agent.ToolNameDurableWait) {
		prompt := strings.TrimSpace(userRequest[len("/ask-many "):])
		inputArguments, err := userInputObject(prompt, nil)
		if err != nil {
			return agent.ModelReply{}, err
		}
		firstCall := calls.makeToolCall(agent.ToolNameRequestUserInput, inputArguments)
		waitArguments, err := durableWaitObject(60, "superseded test")
		if err != nil {
			return agent.ModelReply{}, err
		}
		secondCall := calls.makeToolCall(agent.ToolNameDurableWait, waitArguments)
		toolCalls := []agent.ToolCall{firstCall, secondCall}
		if err := writeToolActivities(toolCalls, request.WriteActivity); err != nil {
			return agent.ModelReply{}, err
		}
		return agent.ModelReply{
			Content:   "I need more information before I continue.",
			ToolCalls: toolCalls,
		}, nil
	}
	if strings.HasPrefix(strings.ToLower(userRequest), "/ask ") && hasTool(available, agent.ToolNameRequestUserInput) {
		content := "I need more information before I continue."
		if err := streamMockContent(ctx, content, request.WriteAssistant); err != nil {
			return agent.ModelReply{}, err
		}
		arguments, err := userInputObject(strings.TrimSpace(userRequest[len("/ask "):]), nil)
		if err != nil {
			return agent.ModelReply{}, err
		}
		return calls.toolReply(content, agent.ToolNameRequestUserInput, arguments, request.WriteActivity)
	}

	activeTasks := activePlan(request.Messages)
	if activeTasks != nil && hasUnfinishedTask(activeTasks) {
		if strings.HasPrefix(strings.ToLower(lastUserContent(request.Messages)), "/plan-stop ") {
			content := "I stopped before completing every plan task."
			if err := streamMockContent(ctx, content, request.WriteAssistant); err != nil {
				return agent.ModelReply{}, err
			}
			return agent.ModelReply{Content: content, ToolCalls: []agent.ToolCall{}}, nil
		}
		arguments, err := writeTodosObject(nextMockPlanTasks(activeTasks))
		if err != nil {
			return agent.ModelReply{}, err
		}
		return calls.toolReply(
			"I will execute the approved plan.",
			agent.ToolNameWriteTodos,
			arguments,
			request.WriteActivity,
		)
	}

	lastMessage := lastConversationMessage(request.Messages)
	content := "How can I help?"
	if lastMessage != nil {
		switch {
		case lastMessage.Role == agent.MessageRoleTool && pointerValue(lastMessage.ToolName) == agent.ToolNameWriteTodos:
			if planStatus(request.Messages) == agent.PlanStatusCompleted {
				content = "I completed the approved plan."
			} else {
				content = "The plan is ready for review."
			}
		case lastMessage.Role == agent.MessageRoleTool:
			content = "The tool finished with this result: " + lastMessage.Content
		case strings.HasPrefix(strings.ToLower(userRequest), "/wait "):
			parts := strings.SplitN(userRequest, " ", 3)
			if len(parts) < 2 {
				return agent.ModelReply{}, errors.New("local /wait syntax is /wait <seconds> <reason>")
			}
			var duration int64
			if _, err := fmt.Sscan(parts[1], &duration); err != nil {
				return agent.ModelReply{}, fmt.Errorf("parse wait duration: %w", err)
			}
			reason := "Requested wait"
			if len(parts) > 2 {
				reason = parts[2]
			}
			arguments, err := durableWaitObject(duration, reason)
			if err != nil {
				return agent.ModelReply{}, err
			}
			return calls.toolReply("I will wait durably.", agent.ToolNameDurableWait, arguments, request.WriteActivity)
		case strings.HasPrefix(strings.ToLower(userRequest), "/tool "):
			parts := strings.SplitN(userRequest, " ", 3)
			if len(parts) != 3 {
				return agent.ModelReply{}, errors.New("local /tool syntax is /tool <name> <json-object>")
			}
			arguments, err := agent.ParseJSONObject(parts[2])
			if err != nil {
				return agent.ModelReply{}, fmt.Errorf("parse local tool arguments: %w", err)
			}
			return calls.toolReply(
				"I will call "+parts[1]+".",
				agent.ToolName(parts[1]),
				arguments,
				request.WriteActivity,
			)
		default:
			content = "Local demo response: " + userRequest
		}
	}
	if err := streamMockContent(ctx, content, request.WriteAssistant); err != nil {
		return agent.ModelReply{}, err
	}
	return agent.ModelReply{Content: content, ToolCalls: []agent.ToolCall{}}, nil
}

// Summarize creates the same bounded local transcript as Python mock/dex.
func (*MockClient) Summarize(_ context.Context, request agent.SummarizeRequest) (string, error) {
	parts := make([]string, 0, len(request.Messages)+1)
	if request.PreviousSummary != "" {
		parts = append(parts, request.PreviousSummary)
	}
	for _, message := range request.Messages {
		content := message.Content
		if len(content) > 500 {
			content = content[:500]
		}
		parts = append(parts, string(message.Role)+": "+content)
	}
	result := strings.Join(parts, "\n")
	if len(result) > 12_000 {
		result = result[len(result)-12_000:]
	}
	return result, nil
}

// CountTokens uses the Python mock/dex four-characters estimate.
func (*MockClient) CountTokens(_ agent.Model, messages []agent.AgentMessage) int {
	total := 0
	for _, message := range messages {
		count := len(message.Content) / 4
		if count < 1 {
			count = 1
		}
		total += count
	}
	return total
}

type mockCallFactory struct {
	flowID       agent.FlowID
	messageCount int
	nextOrdinal  int
}

func (factory *mockCallFactory) toolReply(
	content string,
	name agent.ToolName,
	arguments agent.JSONObject,
	writeActivity agent.ActivityWriter,
) (agent.ModelReply, error) {
	call := factory.makeToolCall(name, arguments)
	if err := writeToolActivities([]agent.ToolCall{call}, writeActivity); err != nil {
		return agent.ModelReply{}, err
	}
	return agent.ModelReply{Content: content, ToolCalls: []agent.ToolCall{call}}, nil
}

func (factory *mockCallFactory) makeToolCall(name agent.ToolName, arguments agent.JSONObject) agent.ToolCall {
	seed := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s", factory.flowID, factory.messageCount, factory.nextOrdinal, name, arguments)
	digest := sha256.Sum256([]byte(seed))
	factory.nextOrdinal++
	return agent.ToolCall{
		ID:        agent.CallID("call-" + hex.EncodeToString(digest[:16])),
		Name:      name,
		Arguments: arguments,
	}
}

func writeToolActivities(calls []agent.ToolCall, writeActivity agent.ActivityWriter) error {
	for _, call := range calls {
		callID := call.ID
		toolName := call.Name
		if err := writeActivity(agent.AgentEvent{
			Kind:     agent.EventKindModelToolCall,
			Message:  "Model requested " + string(call.Name) + ".",
			CallID:   &callID,
			ToolName: &toolName,
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeTodosObject(tasks []agent.PlanTask) (agent.JSONObject, error) {
	encoded, err := json.Marshal(struct {
		Todos []agent.PlanTask `json:"todos"`
	}{Todos: tasks})
	if err != nil {
		return "", fmt.Errorf("encode mock plan arguments: %w", err)
	}
	return agent.ParseJSONObject(string(encoded))
}

func userInputObject(prompt string, choices []string) (agent.JSONObject, error) {
	encoded, err := json.Marshal(struct {
		Prompt  string   `json:"prompt"`
		Choices []string `json:"choices,omitempty"`
	}{Prompt: prompt, Choices: choices})
	if err != nil {
		return "", fmt.Errorf("encode mock user-input arguments: %w", err)
	}
	return agent.ParseJSONObject(string(encoded))
}

func durableWaitObject(durationSeconds int64, reason string) (agent.JSONObject, error) {
	encoded, err := json.Marshal(struct {
		DurationSeconds int64  `json:"duration_seconds"`
		Reason          string `json:"reason"`
	}{DurationSeconds: durationSeconds, Reason: reason})
	if err != nil {
		return "", fmt.Errorf("encode mock wait arguments: %w", err)
	}
	return agent.ParseJSONObject(string(encoded))
}

func streamMockContent(ctx context.Context, content string, write agent.TextWriter) error {
	midpoint := len(content) / 2
	if err := write(content[:midpoint]); err != nil {
		return err
	}
	if err := waitContext(ctx, 200*time.Millisecond); err != nil {
		return err
	}
	if err := write(content[midpoint:]); err != nil {
		return err
	}
	return waitContext(ctx, 200*time.Millisecond)
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func splitChoiceCommand(request string) []string {
	parts := strings.Split(request[len("/choose "):], "|")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
		if parts[index] == "" {
			return nil
		}
	}
	return parts
}

func hasTool(available map[agent.ToolName]struct{}, name agent.ToolName) bool {
	_, found := available[name]
	return found
}

func lastUserContent(messages []agent.AgentMessage) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == agent.MessageRoleUser {
			return strings.TrimSpace(messages[index].Content)
		}
	}
	return ""
}

func lastConversationMessage(messages []agent.AgentMessage) *agent.AgentMessage {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != agent.MessageRoleSystem {
			return &messages[index]
		}
	}
	return nil
}

func activePlan(messages []agent.AgentMessage) []agent.PlanTask {
	if len(messages) == 0 || messages[len(messages)-1].Role != agent.MessageRoleSystem ||
		!strings.Contains(messages[len(messages)-1].Content, "The user approved this plan. Execute it") {
		return nil
	}
	plan := durablePlan(messages)
	if plan == nil || plan.Status != agent.PlanStatusActive {
		return nil
	}
	return plan.Tasks
}

func durablePlan(messages []agent.AgentMessage) *agent.AgentPlan {
	const prefix = "Current durable plan: "
	for _, message := range messages {
		if message.Role != agent.MessageRoleSystem || !strings.HasPrefix(message.Content, prefix) {
			continue
		}
		line := strings.SplitN(strings.TrimPrefix(message.Content, prefix), "\n", 2)[0]
		var plan agent.AgentPlan
		if err := json.Unmarshal([]byte(line), &plan); err != nil {
			return nil
		}
		return &plan
	}
	return nil
}

func planStatus(messages []agent.AgentMessage) agent.PlanStatus {
	plan := durablePlan(messages)
	if plan == nil {
		return ""
	}
	return plan.Status
}

func hasUnfinishedTask(tasks []agent.PlanTask) bool {
	for _, task := range tasks {
		if task.Status != agent.TaskStatusCompleted {
			return true
		}
	}
	return false
}

func nextMockPlanTasks(tasks []agent.PlanTask) []agent.PlanTask {
	currentIndex := -1
	for index, task := range tasks {
		if task.Status == agent.TaskStatusInProgress {
			currentIndex = index
			break
		}
	}
	result := append([]agent.PlanTask(nil), tasks...)
	if currentIndex < 0 {
		for index := range result {
			if result[index].Status == agent.TaskStatusPending {
				result[index].Status = agent.TaskStatusInProgress
				break
			}
		}
		return result
	}
	result[currentIndex].Status = agent.TaskStatusCompleted
	for index := currentIndex + 1; index < len(result); index++ {
		if result[index].Status == agent.TaskStatusPending {
			result[index].Status = agent.TaskStatusInProgress
			break
		}
	}
	return result
}

func pointerValue(value *agent.ToolName) agent.ToolName {
	if value == nil {
		return ""
	}
	return *value
}

var _ agent.ModelClient = (*MockClient)(nil)

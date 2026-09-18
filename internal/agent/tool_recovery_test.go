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
	"context"
	"testing"
	"time"
)

func TestParallelToolMovementsUseBoundedContiguousSafeWave(t *testing.T) {
	definitions := []ToolDefinition{
		parallelDefinitionForTestOnly("read_a"),
		parallelDefinitionForTestOnly("read_b"),
		parallelDefinitionForTestOnly("write_c"),
		parallelDefinitionForTestOnly("read_d"),
	}
	definitions[2].SupportsParallelExecution = false
	definitions[2].RequiresApproval = true
	flow := &Flow{tools: staticToolRegistryForTestOnly{definitions: definitions}}
	config := NewAgentConfig()
	config.MaxParallelToolCalls = 4
	state := AgentState{
		InteractionMode: InteractionModeChat,
		PendingToolCalls: []ToolCall{
			toolCallForTestOnly("call-a", "read_a"),
			toolCallForTestOnly("call-b", "read_b"),
			toolCallForTestOnly("call-c", "write_c"),
			toolCallForTestOnly("call-d", "read_d"),
		},
	}
	movements, parallel, err := flow.parallelToolMovements(config, state)
	if err != nil {
		t.Fatal(err)
	}
	if !parallel || len(movements) != 3 {
		t.Fatalf("parallel = %t, movements = %d; want coordinator plus two reads", parallel, len(movements))
	}

	config.MaxParallelToolCalls = 1
	movements, parallel, err = flow.parallelToolMovements(config, state)
	if err != nil {
		t.Fatal(err)
	}
	if parallel || movements != nil {
		t.Fatalf("limit one scheduled parallel work: parallel = %t, movements = %d", parallel, len(movements))
	}
}

func TestParallelToolMovementsSplitAtConfiguredLimit(t *testing.T) {
	definitions := []ToolDefinition{}
	calls := []ToolCall{}
	for index, name := range []ToolName{"read_a", "read_b", "read_c", "read_d", "read_e"} {
		definitions = append(definitions, parallelDefinitionForTestOnly(name))
		calls = append(calls, toolCallForTestOnly(CallID("call-"+string(rune('a'+index))), name))
	}
	flow := &Flow{tools: staticToolRegistryForTestOnly{definitions: definitions}}
	config := NewAgentConfig()
	config.MaxParallelToolCalls = 3
	movements, parallel, err := flow.parallelToolMovements(config, AgentState{
		InteractionMode:  InteractionModeChat,
		PendingToolCalls: calls,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !parallel || len(movements) != 4 {
		t.Fatalf("parallel = %t, movements = %d; want coordinator plus three reads", parallel, len(movements))
	}
}

func TestToolDefinitionsExposeFailureSimulationOnlyToLocalMock(t *testing.T) {
	flow := &Flow{tools: staticToolRegistryForTestOnly{}}
	config := NewAgentConfig()
	if !hasToolDefinitionForTestOnly(flow.toolDefinitions(config), ToolNameSimulateFailure) {
		t.Fatal("local mock definitions omit simulate_tool_failure")
	}
	config.Model = "openai/gpt-5-mini"
	if hasToolDefinitionForTestOnly(flow.toolDefinitions(config), ToolNameSimulateFailure) {
		t.Fatal("real provider definitions include simulate_tool_failure")
	}
}

func TestValidateToolRecoveryResolutionRequiresAtomicCompleteDecision(t *testing.T) {
	pending := PendingToolRecovery{
		RecoveryID: "recovery-1",
		Calls: []PendingToolRecoveryCall{
			{CallID: "call-a"},
			{CallID: "call-b"},
		},
	}
	valid := ResolveToolRecoveryRequest{
		RecoveryID: "recovery-1",
		Resolution: ToolRecoveryResolutionResume,
		Decisions: []ToolRecoveryDecision{
			{CallID: "call-a", Action: ToolRecoveryActionRetry},
			{CallID: "call-b", Action: ToolRecoveryActionContinueWithUnknown},
		},
	}
	if err := validateToolRecoveryResolution(pending, valid); err != nil {
		t.Fatalf("valid resolution: %v", err)
	}

	tests := map[string]ResolveToolRecoveryRequest{
		"incomplete": {
			Resolution: ToolRecoveryResolutionResume,
			Decisions:  valid.Decisions[:1],
		},
		"duplicate": {
			Resolution: ToolRecoveryResolutionResume,
			Decisions: []ToolRecoveryDecision{
				{CallID: "call-a", Action: ToolRecoveryActionRetry},
				{CallID: "call-a", Action: ToolRecoveryActionRetry},
			},
		},
		"unknown call": {
			Resolution: ToolRecoveryResolutionResume,
			Decisions: []ToolRecoveryDecision{
				{CallID: "call-a", Action: ToolRecoveryActionRetry},
				{CallID: "call-c", Action: ToolRecoveryActionRetry},
			},
		},
		"stop with decisions": {
			Resolution: ToolRecoveryResolutionStop,
			Decisions:  valid.Decisions,
		},
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateToolRecoveryResolution(pending, request); err == nil {
				t.Fatal("validateToolRecoveryResolution() error = nil")
			}
		})
	}
}

func parallelDefinitionForTestOnly(name ToolName) ToolDefinition {
	return ToolDefinition{
		Name:                      name,
		InputSchema:               MustJSONObject(`{"type":"object"}`),
		AttemptTimeout:            time.Second,
		MaximumAttempts:           1,
		RetryTotalDuration:        time.Second,
		SupportsParallelExecution: true,
		RetryExhaustionPolicy:     ToolRetryExhaustionPolicyManualRecovery,
	}
}

func toolCallForTestOnly(id CallID, name ToolName) ToolCall {
	return ToolCall{ID: id, Name: name, Arguments: MustJSONObject(`{}`)}
}

func hasToolDefinitionForTestOnly(definitions []ToolDefinition, name ToolName) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

type staticToolRegistryForTestOnly struct {
	definitions []ToolDefinition
}

func (staticToolRegistryForTestOnly) ServerNames() []string { return []string{"test"} }

func (registry staticToolRegistryForTestOnly) RegisteredTools() []RegisteredTool {
	result := make([]RegisteredTool, 0, len(registry.definitions))
	for _, definition := range registry.definitions {
		result = append(result, RegisteredTool{ServerName: "test", Definition: definition})
	}
	return result
}

func (registry staticToolRegistryForTestOnly) Definitions([]string, []ToolName) []ToolDefinition {
	return append([]ToolDefinition(nil), registry.definitions...)
}

func (staticToolRegistryForTestOnly) Execute(context.Context, ToolInvocation) (ToolExecutionResult, error) {
	return ToolExecutionResult{}, nil
}

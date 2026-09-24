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
	"errors"
	"testing"
	"time"

	"github.com/superdurable/dex/sdk-go/dex"
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

func TestValidateConfigRequiresExpirationHandlerWhenTimeoutEnabled(t *testing.T) {
	config := NewAgentConfig()
	config.InactivityTimeoutSeconds = 60
	flow := &Flow{tools: staticToolRegistryForTestOnly{}}
	if err := flow.validateConfig(config); err == nil {
		t.Fatal("enabled inactivity timeout without handler validated")
	}
	flow.inactivityExpiration = inactivityExpirationHandlerFunc(func(
		context.Context,
		InactivityExpiration,
	) error {
		return nil
	})
	if err := flow.validateConfig(config); err != nil {
		t.Fatalf("enabled inactivity timeout with handler: %v", err)
	}
}

type inactivityExpirationHandlerFunc func(context.Context, InactivityExpiration) error

func (handler inactivityExpirationHandlerFunc) HandleInactivityExpiration(
	ctx context.Context,
	expiration InactivityExpiration,
) error {
	return handler(ctx, expiration)
}

func TestToolStepOptionsMapRunningTypeWithOneMinuteHeartbeat(t *testing.T) {
	flow := &Flow{}
	short := parallelDefinitionForTestOnly("short")
	shortOptions := flow.serialToolStepOptions(short)
	if shortOptions.ExecuteDurability != dex.StepDurabilityDefault ||
		shortOptions.HeartbeatTimeout != time.Minute {
		t.Fatalf("short options = %+v", shortOptions)
	}
	parallelShortOptions := flow.parallelToolStepOptions(short)
	if parallelShortOptions.ExecuteDurability != dex.StepDurabilityDefault ||
		parallelShortOptions.HeartbeatTimeout != time.Minute {
		t.Fatalf("parallel short options = %+v", parallelShortOptions)
	}

	long := short
	long.RunningType = ToolRunningTypeLongRunning
	longOptions := flow.serialToolStepOptions(long)
	if longOptions.ExecuteDurability != dex.StepDurabilitySync ||
		longOptions.HeartbeatTimeout != time.Minute {
		t.Fatalf("long options = %+v", longOptions)
	}
	parallelLongOptions := flow.parallelToolStepOptions(long)
	if parallelLongOptions.ExecuteDurability != dex.StepDurabilitySync ||
		parallelLongOptions.HeartbeatTimeout != time.Minute {
		t.Fatalf("parallel long options = %+v", parallelLongOptions)
	}
}

func TestToolExecutionStepsUseOnlyMovementOptions(t *testing.T) {
	if got := (executeToolStep{}).GetStepType(); got != "ExecuteTool" {
		t.Fatalf("serial Step type = %q", got)
	}
	if options := (executeToolStep{}).GetStepOptions(); options != nil {
		t.Fatalf("serial registered options = %+v", options)
	}
	if options := (executeParallelToolStep{}).GetStepOptions(); options != nil {
		t.Fatalf("parallel registered options = %+v", options)
	}
}

func TestRegisteredStepOptionsUseBoundedTimeoutsAndModelSyncDurability(t *testing.T) {
	if defaultStepOptions.WaitForMethodTimeout != time.Minute ||
		defaultStepOptions.ExecuteMethodTimeout != time.Minute {
		t.Fatalf("default Step options = %+v", defaultStepOptions)
	}
	if modelStepOptions.ExecuteDurability != dex.StepDurabilitySync ||
		modelStepOptions.ExecuteMethodTimeout != 10*time.Minute ||
		modelStepOptions.HeartbeatTimeout != time.Minute {
		t.Fatalf("model Step options = %+v", modelStepOptions)
	}
}

func TestValidateToolExecutionPolicyRejectsUnknownRunningType(t *testing.T) {
	definition := parallelDefinitionForTestOnly("invalid")
	definition.RunningType = "sometimes"
	err := validateToolExecutionPolicy(definition)
	var validationErr *EnumValidationError
	if !errors.As(err, &validationErr) || validationErr.Type != "ToolRunningType" {
		t.Fatalf("validation error = %T %v", err, err)
	}
}

func TestToolExecutionContextUsesDeclaredAttemptTimeout(t *testing.T) {
	started := time.Now()
	ctx, cancel := newToolExecutionContext(context.Background(), time.Minute)
	defer cancel()
	deadline, found := ctx.Deadline()
	if !found || deadline.Before(started.Add(59*time.Second)) || deadline.After(started.Add(61*time.Second)) {
		t.Fatalf("deadline = %v, found = %t", deadline, found)
	}
	withoutDeadline, cancelWithoutDeadline := newToolExecutionContext(context.Background(), 0)
	defer cancelWithoutDeadline()
	if _, found := withoutDeadline.Deadline(); found {
		t.Fatal("zero attempt timeout added a deadline")
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

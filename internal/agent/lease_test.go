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
	"strings"
	"testing"
	"time"

	"github.com/superdurable/dex/sdk-go/dex"
)

type leaseRefresherForTestOnly struct{}

func (leaseRefresherForTestOnly) Refresh(
	context.Context,
	LeaseRefreshRequest,
) (LeaseRefreshResult, error) {
	return LeaseRefreshResult{}, nil
}

func TestLeaseInitializationValidation(t *testing.T) {
	now := time.Now()
	valid := &LeaseInitialization{
		State:     MustJSONObject(`{"token":"initial","config":{"installation":7}}`),
		RefreshAt: now.Add(15 * time.Minute),
	}
	if err := validateLeaseInitialization(valid, now); err != nil {
		t.Fatalf("valid initial Lease: %v", err)
	}
	tests := []struct {
		name    string
		initial *LeaseInitialization
	}{
		{name: "missing"},
		{name: "empty state", initial: &LeaseInitialization{RefreshAt: valid.RefreshAt}},
		{name: "array state", initial: &LeaseInitialization{State: JSONObject(`[]`), RefreshAt: valid.RefreshAt}},
		{name: "oversized state", initial: &LeaseInitialization{
			State:     JSONObject(`{"value":"` + strings.Repeat("x", MaximumLeaseStateBytes) + `"}`),
			RefreshAt: valid.RefreshAt,
		}},
		{name: "past refresh", initial: &LeaseInitialization{State: valid.State, RefreshAt: now}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateLeaseInitialization(test.initial, now); err == nil {
				t.Fatal("validateLeaseInitialization() error = nil")
			}
		})
	}
}

func TestLeaseExtensionConfigurationAndRegistration(t *testing.T) {
	flow := NewFlow(
		modelClientForLeaseTestOnly{},
		toolRegistryForLeaseTestOnly{},
		WithLeaseExtension(leaseRefresherForTestOnly{}, nil),
	)
	if flow.lease == nil || flow.lease.config.AttemptTimeout != 2*time.Minute ||
		flow.lease.config.RetryTotalDuration != 30*time.Minute {
		t.Fatalf("Lease defaults = %+v", flow.lease)
	}
	if _, err := dex.NewRegistry([]dex.Flow{flow}); err != nil {
		t.Fatalf("register Flow: %v", err)
	}
	config := NewAgentConfig()
	configured := flow.modelConfig(config)
	if configured.SystemPrompt == config.SystemPrompt || !strings.Contains(configured.SystemPrompt, "new tool call") {
		t.Fatalf("Lease recovery prompt = %q", configured.SystemPrompt)
	}
	if config.SystemPrompt != NewAgentConfig().SystemPrompt {
		t.Fatal("modelConfig mutated the durable Agent config")
	}
	initial := &LeaseInitialization{
		State:     MustJSONObject(`{"token":"initial"}`),
		RefreshAt: time.Now().Add(time.Minute),
	}
	if err := flow.validateInitialLease(initial, time.Now()); err != nil {
		t.Fatalf("validate configured initial Lease: %v", err)
	}
	if err := NewFlow(modelClientForLeaseTestOnly{}, toolRegistryForLeaseTestOnly{}).
		validateInitialLease(initial, time.Now()); err == nil {
		t.Fatal("unconfigured Flow accepted an initial Lease")
	}
	if err := flow.validateInitialLease(nil, time.Now()); err == nil {
		t.Fatal("configured Flow accepted a missing initial Lease")
	}
}

func TestStableLeaseRefreshIDUsesFlowAndGeneration(t *testing.T) {
	first := stableLeaseRefreshID("flow-one", 2)
	if first == "" || first != stableLeaseRefreshID("flow-one", 2) {
		t.Fatalf("unstable refresh ID %q", first)
	}
	if first == stableLeaseRefreshID("flow-one", 3) || first == stableLeaseRefreshID("flow-two", 2) {
		t.Fatal("refresh ID does not distinguish Flow and generation")
	}
}

func TestToolDefinitionMapsToDexStepOptions(t *testing.T) {
	flow := NewFlow(modelClientForLeaseTestOnly{}, toolRegistryForLeaseTestOnly{})
	definition := ToolDefinition{
		Name:               "fixture",
		AttemptTimeout:     45 * time.Second,
		MaximumAttempts:    7,
		RetryTotalDuration: 9 * time.Minute,
	}
	options := flow.toolStepOptions(definition)
	if options.ExecuteMethodTimeout != definition.AttemptTimeout ||
		options.ExecuteRetry == nil ||
		options.ExecuteRetry.MaximumAttempts != int32(definition.MaximumAttempts) ||
		options.ExecuteRetry.TotalDuration != definition.RetryTotalDuration ||
		options.ExecuteFailure == nil {
		t.Fatalf("tool StepOptions = %#v", options)
	}
}

type modelClientForLeaseTestOnly struct{}

func (modelClientForLeaseTestOnly) Complete(context.Context, ModelRequest) (ModelReply, error) {
	return ModelReply{}, nil
}

func (modelClientForLeaseTestOnly) Summarize(context.Context, SummarizeRequest) (string, error) {
	return "", nil
}

func (modelClientForLeaseTestOnly) CountTokens(Model, []AgentMessage) int { return 0 }

type toolRegistryForLeaseTestOnly struct{}

func (toolRegistryForLeaseTestOnly) ServerNames() []string                             { return nil }
func (toolRegistryForLeaseTestOnly) RegisteredTools() []RegisteredTool                 { return nil }
func (toolRegistryForLeaseTestOnly) Definitions([]string, []ToolName) []ToolDefinition { return nil }
func (toolRegistryForLeaseTestOnly) Execute(context.Context, ToolInvocation) (ToolExecutionResult, error) {
	return ToolExecutionResult{}, nil
}

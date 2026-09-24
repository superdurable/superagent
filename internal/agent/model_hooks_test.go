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
)

func TestNewFlowRejectsIncompleteModelHooks(t *testing.T) {
	t.Parallel()
	for _, config := range []*ModelHooksConfig{
		{BeforeModelCall: func(context.Context, FlowID, CallID, Model, bool) (bool, error) { return true, nil }},
		{AfterModelCall: func(context.Context, FlowID, CallID, ModelUsage) error { return nil }},
	} {
		config := config
		t.Run("incomplete", func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Fatal("NewFlow panic = nil")
				}
			}()
			NewFlow(hookTestModel{}, hookTestTools{}, WithModelHooks(config))
		})
	}
}

func TestNewFlowAcceptsNilModelHooks(t *testing.T) {
	t.Parallel()
	flow := NewFlow(hookTestModel{}, hookTestTools{}, WithModelHooks(nil))
	if flow.modelHooks != nil {
		t.Fatalf("model hooks = %#v, want nil", flow.modelHooks)
	}
}

type hookTestModel struct{}

func (hookTestModel) Complete(context.Context, ModelRequest) (ModelReply, ModelUsage, error) {
	return ModelReply{}, ModelUsage{}, nil
}

func (hookTestModel) Summarize(context.Context, SummarizeRequest) (string, ModelUsage, error) {
	return "", ModelUsage{}, nil
}

func (hookTestModel) CountTokens(Model, []AgentMessage) int { return 0 }

type hookTestTools struct{}

func (hookTestTools) ServerNames() []string { return nil }

func (hookTestTools) RegisteredTools() []RegisteredTool { return nil }

func (hookTestTools) Definitions([]string, []ToolName) []ToolDefinition { return nil }

func (hookTestTools) Execute(context.Context, ToolInvocation) (ToolExecutionResult, error) {
	return ToolExecutionResult{}, nil
}

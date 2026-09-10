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

package consumer_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/superdurable/dex/sdk-go/dex"
	"github.com/superdurable/superagent/agent"
	"github.com/superdurable/superagent/model"
)

type modelClient struct{}

func (modelClient) Complete(context.Context, agent.ModelRequest) (agent.ModelReply, error) {
	return agent.ModelReply{}, nil
}

func (modelClient) Summarize(context.Context, agent.SummarizeRequest) (string, error) {
	return "", nil
}

func (modelClient) CountTokens(agent.Model, []agent.AgentMessage) int {
	return 0
}

type toolRegistry struct{}

func (toolRegistry) ServerNames() []string {
	return nil
}

func (toolRegistry) RegisteredTools() []agent.RegisteredTool {
	return nil
}

func (toolRegistry) Definitions([]string, []agent.ToolName) []agent.ToolDefinition {
	return nil
}

func (toolRegistry) Execute(context.Context, agent.ToolInvocation) (agent.ToolExecutionResult, error) {
	return agent.ToolExecutionResult{}, nil
}

var (
	_ agent.ModelClient                            = modelClient{}
	_ agent.ToolRegistry                           = toolRegistry{}
	_ dex.Flow                                     = (*agent.Flow)(nil)
	_ func(*dex.Client, *agent.Flow) *agent.Client = agent.NewClient
)

func TestExternalModuleCanConstructAndRegisterAgent(t *testing.T) {
	t.Parallel()
	config := agent.NewAgentConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("default config: %v", err)
	}
	if _, err := agent.ParseJSONObject(`{"type":"object"}`); err != nil {
		t.Fatalf("parse JSON object: %v", err)
	}
	flow := agent.NewFlow(modelClient{}, toolRegistry{})
	if _, err := dex.NewRegistry([]dex.Flow{flow}); err != nil {
		t.Fatalf("register public Agent Flow: %v", err)
	}
}

func TestExternalModuleCanConstructProviderRouter(t *testing.T) {
	t.Parallel()
	credentials := model.NewCredentialStore()
	if err := credentials.SetDefaultAPIKey(agent.ProviderOpenAI, "test-key"); err != nil {
		t.Fatalf("set public credential: %v", err)
	}
	httpClient := &http.Client{}
	anthropic, err := model.NewAnthropicClient(credentials, httpClient, "")
	if err != nil {
		t.Fatalf("construct public Anthropic adapter: %v", err)
	}
	gemini, err := model.NewGeminiClient(credentials, httpClient, "")
	if err != nil {
		t.Fatalf("construct public Gemini adapter: %v", err)
	}
	groq, err := model.NewGroqClient(credentials, httpClient, "")
	if err != nil {
		t.Fatalf("construct public Groq adapter: %v", err)
	}
	client := model.NewClient(
		model.NewMockClient(),
		model.NewOpenAIClient(credentials, httpClient, ""),
		anthropic,
		gemini,
		groq,
	)
	var _ agent.ModelClient = client
}

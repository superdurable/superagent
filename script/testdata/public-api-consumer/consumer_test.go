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
	"github.com/superdurable/superagent/toolcontract"
)

type modelClient struct{}

func (modelClient) Complete(context.Context, agent.ModelRequest) (agent.ModelReply, agent.ModelUsage, error) {
	return agent.ModelReply{}, agent.ModelUsage{}, nil
}

func (modelClient) Summarize(context.Context, agent.SummarizeRequest) (string, agent.ModelUsage, error) {
	return "", agent.ModelUsage{}, nil
}

func (modelClient) CountTokens(agent.Model, []agent.AgentMessage) int {
	return 0
}

type toolRegistry struct{}

type expirationHandler struct{}

func (expirationHandler) HandleInactivityExpiration(
	context.Context,
	agent.InactivityExpiration,
) error {
	return nil
}

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
	_ agent.ModelClient                                                                                            = modelClient{}
	_ agent.ToolRegistry                                                                                           = toolRegistry{}
	_ agent.InactivityExpirationHandler                                                                            = expirationHandler{}
	_ dex.Flow                                                                                                     = (*agent.Flow)(nil)
	_ func(*dex.Client, *agent.Flow) *agent.Client                                                                 = agent.NewClient
	_ func(*agent.Client, context.Context, agent.FlowID, agent.StartRequest) (agent.RunID, error)                  = (*agent.Client).Start
	_ func(*agent.Client, context.Context, agent.FlowID, agent.UserMessage) error                                  = (*agent.Client).SendMessage
	_ func(*agent.Client, context.Context, agent.FlowID, agent.AnswerQuestionsRequest) error                       = (*agent.Client).AnswerQuestions
	_ func(*agent.Client, context.Context, agent.FlowID, agent.SteerMessageRequest) error                          = (*agent.Client).SteerMessage
	_ func(*agent.Client, context.Context, agent.FlowID, agent.MessageID) error                                    = (*agent.Client).DeleteQueuedMessage
	_ func(*agent.Client, context.Context, agent.FlowID, agent.ToolApprovalRequest) error                          = (*agent.Client).ApproveTool
	_ func(*agent.Client, context.Context, agent.FlowID, agent.PlanExecutionRequest) error                         = (*agent.Client).ExecutePlan
	_ func(*agent.Client, context.Context, agent.FlowID) (agent.AgentSnapshot, error)                              = (*agent.Client).GetSnapshot
	_ func(*agent.Client, context.Context, agent.FlowID, agent.WaitingInputRound) (agent.WaitingInputRound, error) = (*agent.Client).WaitForWaitingInputRound
	_ func(*agent.Client, context.Context, agent.FlowID, agent.Sequence) (agent.HistoryPage, error)                = (*agent.Client).GetArchivedMessages
	_ func(*agent.Client, context.Context, agent.FlowID, agent.EventStream, int) ([]agent.StreamEvent, error)      = (*agent.Client).ListRecentEvents
	_ agent.EventKind                                                                                              = agent.EventKindPlanTaskUpdated
	_ agent.EventKind                                                                                              = agent.EventKindInputConsumed
	_ agent.EventKind                                                                                              = agent.EventKindSnapshotRequired
	_ agent.ToolRunningType                                                                                        = agent.ToolRunningTypeShortRunning
	_ agent.PlanTaskIndex                                                                                          = 0
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
	flow := agent.NewFlow(
		modelClient{},
		toolRegistry{},
		agent.WithInactivityExpirationHandler(expirationHandler{}),
	)
	if _, err := dex.NewRegistry([]dex.Flow{flow}); err != nil {
		t.Fatalf("register public Agent Flow: %v", err)
	}
	metadata := agent.MustJSONObject(`{"resource_id":"resource-1"}`)
	invocation := agent.ToolInvocation{RuntimeMetadata: metadata}
	if invocation.RuntimeMetadata != metadata {
		t.Fatal("public ToolInvocation runtime metadata is unavailable")
	}
	if agent.MaximumUserMessageContentBytes != 256<<10 {
		t.Fatal("public Agent admission limits are unavailable")
	}
	if agent.MaximumRuntimeMetadataBytes != 16<<10 {
		t.Fatal("public runtime metadata limit is unavailable")
	}
	definition := agent.ToolDefinition{
		RunningType: agent.ToolRunningTypeLongRunning,
	}
	if definition.RunningType != agent.ToolRunningTypeLongRunning {
		t.Fatal("public tool running policy is unavailable")
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
		model.NewOpenAIClient(credentials, httpClient, "", &model.OpenAIClientConfig{}),
		anthropic,
		gemini,
		groq,
	)
	var _ agent.ModelClient = client
}

func TestExternalModuleCanUseBuiltinToolContracts(t *testing.T) {
	t.Parallel()
	input, err := toolcontract.DecodeWriteTodosInput(
		`{"todos":[{"content":"publish","status":"in_progress"}]}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Todos) != 1 || input.Todos[0].Status != toolcontract.WriteTodosStatusInProgress {
		t.Fatalf("decoded write_todos input = %#v", input)
	}
	wait, err := toolcontract.DecodeDurableWaitInput(`{"duration_seconds":1,"reason":"publish"}`)
	if err != nil || wait.DurationSeconds != 1 {
		t.Fatalf("decoded durable_wait input = %#v, %v", wait, err)
	}
	questions, err := toolcontract.DecodeRequestUserInput(`{
		"questions":[{
			"id":"release",
			"header":"Release",
			"question":"Publish now?",
			"options":[
				{"label":"Yes","description":"Publish now."},
				{"label":"No","description":"Wait."}
			]
		}]
	}`)
	if err != nil || len(questions.Questions) != 1 {
		t.Fatalf("decoded request_user_input input = %#v, %v", questions, err)
	}
	for name, schema := range map[string]string{
		"write_todos":        toolcontract.WriteTodosInputSchema(),
		"durable_wait":       toolcontract.DurableWaitInputSchema(),
		"request_user_input": toolcontract.RequestUserInputSchema(),
	} {
		if _, err := agent.ParseJSONObject(schema); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
	}
}

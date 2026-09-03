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

package api

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/superdurable/superagent/internal/agent"
	transportapi "github.com/superdurable/superagent/internal/api/generated"
)

func TestStartAgentQualifiesProviderModel(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{}
	handler := newTestHandler(service, fakeCredentials{agent.ProviderOpenAI: true})
	response, err := handler.StartAgent(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.(*transportapi.StartAgentResponse); !ok {
		t.Fatalf("response type = %T", response)
	}
	if service.started.Model != "openai/gpt-5-mini" {
		t.Fatalf("model = %q", service.started.Model)
	}
	if service.started.CompactionTriggerFraction != 0.85 || service.started.CompactionKeepFraction != 0.10 {
		t.Fatalf("compaction defaults = %v/%v", service.started.CompactionTriggerFraction, service.started.CompactionKeepFraction)
	}
}

func TestStartAgentRejectsMissingProviderCredential(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.StartAgent(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.(*transportapi.StartAgentBadRequest); !ok {
		t.Fatalf("response type = %T", response)
	}
	if service.startCalls != 0 {
		t.Fatalf("Start calls = %d", service.startCalls)
	}
}

func TestCommandRejectionMapsToConflict(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{sendErr: &agent.CommandRejectedError{Command: agent.CommandSendMessage}}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.SendMessage(context.Background(), &transportapi.SendMessageRequest{
		FlowId: "flow-1", Content: "hello", PlanMode: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.(*transportapi.SendMessageConflict); !ok {
		t.Fatalf("response type = %T", response)
	}
}

func TestReadEventMapsTypedActivity(t *testing.T) {
	t.Parallel()
	callID := agent.CallID("call-1")
	toolName := agent.ToolName("lookup")
	service := &fakeAgentService{event: agent.StreamEvent{
		Kind: agent.StreamEventKindActivity,
		Activity: agent.AgentEvent{
			Kind: agent.EventKindToolCompleted, Message: "done", CallID: &callID, ToolName: &toolName,
		},
		ResumeToken: "resume-1", CreatedAt: time.Unix(1, 0).UTC(), Source: "turn-1",
	}}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.ReadEvent(context.Background(), transportapi.ReadEventParams{
		FlowId: "flow-1", Stream: transportapi.EventStreamActivity,
	})
	if err != nil {
		t.Fatal(err)
	}
	event, ok := response.(*transportapi.StreamEvent)
	if !ok || !event.IsActivityStreamEvent() {
		t.Fatalf("response = %#v", response)
	}
	activity, _ := event.GetActivityStreamEvent()
	if activity.Value.Kind != transportapi.EventKindToolCompleted || activity.Value.CallId.Or("") != "call-1" {
		t.Fatalf("activity = %#v", activity)
	}
}

func TestPortalUsesTypedCredentialAndToolProjection(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(&fakeAgentService{}, fakeCredentials{agent.ProviderOpenAI: true})
	response, err := handler.GetPortal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	portal, ok := response.(*transportapi.Portal)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	if len(portal.Providers) != 5 || !portal.Providers[1].Configured {
		t.Fatalf("providers = %#v", portal.Providers)
	}
	if len(portal.Tools) != 1 || portal.Tools[0].Server.Or("") != "files" {
		t.Fatalf("tools = %#v", portal.Tools)
	}
}

func validStartRequest() *transportapi.StartAgentRequest {
	return &transportapi.StartAgentRequest{
		FlowId: "flow-1", Provider: transportapi.ProviderOpenai, Model: "gpt-5-mini",
		SystemPrompt: "be helpful", MaxContextTokens: 32_000, MessageRetentionLimit: 2_000,
		McpEnabled: true, EnabledMcpServers: []string{"files"}, EnabledTools: []transportapi.ToolName{"lookup"},
	}
}

func newTestHandler(service *fakeAgentService, credentials fakeCredentials) *Handler {
	return NewHandler(service, fakeToolCatalog{}, credentials, func() bool { return true }, slog.New(slog.DiscardHandler))
}

type fakeAgentService struct {
	started    agent.AgentConfig
	startCalls int
	sendErr    error
	event      agent.StreamEvent
	eventErr   error
}

func (service *fakeAgentService) Start(_ context.Context, _ agent.FlowID, config agent.AgentConfig) (agent.RunID, error) {
	service.startCalls++
	service.started = config
	return "run-1", nil
}

func (service *fakeAgentService) SendMessage(context.Context, agent.FlowID, agent.UserMessage) error {
	return service.sendErr
}

func (*fakeAgentService) ApproveTool(context.Context, agent.FlowID, agent.ToolApprovalRequest) error {
	return nil
}

func (*fakeAgentService) ExecutePlan(context.Context, agent.FlowID, agent.PlanExecutionRequest) error {
	return nil
}

func (service *fakeAgentService) ReadEvent(context.Context, agent.FlowID, agent.EventStream, agent.ResumeToken) (agent.StreamEvent, error) {
	return service.event, service.eventErr
}

type fakeCredentials map[agent.Provider]bool

func (credentials fakeCredentials) HasAPIKey(_ agent.FlowID, provider agent.Provider) bool {
	return credentials[provider]
}

type fakeToolCatalog struct{}

func (fakeToolCatalog) ServerNames() []string { return []string{"files"} }

func (fakeToolCatalog) RegisteredTools() []agent.RegisteredTool {
	return []agent.RegisteredTool{{ServerName: "files", RemoteName: "lookup", Definition: fakeToolDefinition()}}
}

func (fakeToolCatalog) Definitions([]string, []agent.ToolName) []agent.ToolDefinition {
	return []agent.ToolDefinition{fakeToolDefinition()}
}

func fakeToolDefinition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "lookup", Description: "Look up a file", InputSchema: agent.MustJSONObject(`{"type":"object"}`)}
}

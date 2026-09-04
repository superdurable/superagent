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

	"github.com/superdurable/dex/sdk-go/dex"
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

func TestReadEventMapsPollTimeoutToTypedResponse(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{eventErr: context.DeadlineExceeded}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.ReadEvent(context.Background(), transportapi.ReadEventParams{
		FlowId: "flow-1", Stream: transportapi.EventStreamAssistant,
	})
	if err != nil {
		t.Fatal(err)
	}
	timeout, ok := response.(*transportapi.PollTimeout)
	if !ok || timeout.Reason != transportapi.PollTimeoutReasonTimeout {
		t.Fatalf("response = %#v", response)
	}
}

func TestGetAgentSnapshotMapsAtomicDomainView(t *testing.T) {
	t.Parallel()
	createdAt := time.Unix(1, 0).UTC()
	callID := agent.CallID("call-1")
	toolName := agent.ToolName("lookup")
	service := &fakeAgentService{snapshot: agent.AgentSnapshot{
		RunID:      "run-1",
		FlowStatus: agent.FlowStatusRunning,
		History: agent.HistoryPage{Messages: []agent.SequencedMessage{{
			Sequence: 1,
			Message: agent.AgentMessage{
				Role:       agent.MessageRoleAssistant,
				Content:    "hello",
				ToolCalls:  []agent.ToolCall{{ID: callID, Name: toolName, Arguments: agent.MustJSONObject(`{"path":"README.md"}`)}},
				ToolCallID: &callID,
				ToolName:   &toolName,
				CreatedAt:  createdAt,
			},
		}}},
		Description: &agent.AgentDescription{
			Status:                     agent.AgentStatusWaitingForToolApproval,
			Model:                      "openai/gpt-5-mini",
			SystemPrompt:               "be helpful",
			FirstRetainedSequence:      1,
			LastSequence:               1,
			PendingApproval:            &agent.PendingApproval{CallID: callID, ToolName: toolName, Arguments: agent.MustJSONObject(`{"path":"README.md"}`)},
			Plan:                       &agent.AgentPlan{Revision: 1, Status: agent.PlanStatusDraft, Tasks: []agent.PlanTask{{Content: "inspect", Status: agent.TaskStatusPending}}},
			PendingQueuedMessageCount:  1,
			PendingSteeredMessageCount: 0,
			AvailableMCPServers:        []string{"files"},
			AvailableTools:             []agent.ToolName{"lookup"},
		},
		Queued:  []agent.PendingUserMessage{{MessageID: "message-1", Value: agent.UserMessage{Content: "later"}}},
		Steered: []agent.PendingUserMessage{},
	}}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.GetAgentSnapshot(context.Background(), transportapi.GetAgentSnapshotParams{
		FlowId: "flow-1",
		Limit:  transportapi.NewOptInt(50),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := response.(*transportapi.AgentSnapshotHeaders)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	if result.CacheControl != transportapi.GetAgentSnapshotOKCacheControlNoStore {
		t.Fatalf("Cache-Control = %q", result.CacheControl)
	}
	if validationErr := result.Validate(); validationErr != nil {
		t.Fatalf("validate response: %v", validationErr)
	}
	snapshot := &result.Response
	if snapshot.RunId != "run-1" || snapshot.FlowStatus != transportapi.FlowStatusRunning ||
		len(snapshot.History.Messages) != 1 || len(snapshot.Queued) != 1 {
		t.Fatalf("Snapshot = %#v", snapshot)
	}
	message := snapshot.History.Messages[0].Message
	if message.Role != transportapi.MessageRoleAssistant ||
		message.CreatedAt != createdAt ||
		message.ToolCalls[0].ArgumentsJson != `{"path":"README.md"}` {
		t.Fatalf("Snapshot message = %#v", message)
	}
	description, ok := snapshot.Description.Get()
	if !ok || description.PendingApproval.IsNull() || description.Plan.IsNull() {
		t.Fatalf("Snapshot description = %#v", snapshot.Description)
	}
}

func TestGetAgentSnapshotMapsTerminalFlowResult(t *testing.T) {
	t.Parallel()
	errorType := agent.FlowErrorTypeWorkerMethod
	errorMessage := "worker failed"
	handler := newTestHandler(&fakeAgentService{snapshot: agent.AgentSnapshot{
		RunID:        "run-terminal",
		FlowStatus:   agent.FlowStatusFailed,
		ErrorType:    &errorType,
		ErrorMessage: &errorMessage,
		History:      agent.HistoryPage{Messages: []agent.SequencedMessage{}},
		Queued:       []agent.PendingUserMessage{},
		Steered:      []agent.PendingUserMessage{},
	}}, fakeCredentials{})
	response, err := handler.GetAgentSnapshot(context.Background(), transportapi.GetAgentSnapshotParams{
		FlowId: "flow-terminal",
		Limit:  transportapi.NewOptInt(50),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := response.(*transportapi.AgentSnapshotHeaders)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	snapshot := result.Response
	if snapshot.FlowStatus != transportapi.FlowStatusFailed || !snapshot.Description.IsNull() {
		t.Fatalf("terminal Snapshot = %#v", snapshot)
	}
	mappedErrorType, ok := snapshot.ErrorType.Get()
	if !ok || mappedErrorType != transportapi.FlowErrorTypeWorkerMethod {
		t.Fatalf("terminal error type = %#v", snapshot.ErrorType)
	}
	if mappedMessage, ok := snapshot.ErrorMessage.Get(); !ok || mappedMessage != errorMessage {
		t.Fatalf("terminal error message = %#v", snapshot.ErrorMessage)
	}
}

func TestGetAgentSnapshotPreservesDexFlowLifecycleErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want func(transportapi.GetAgentSnapshotRes) bool
	}{
		{
			name: "missing",
			err:  &dex.FlowNotFoundError{ServiceError: &dex.ServiceError{}},
			want: func(response transportapi.GetAgentSnapshotRes) bool {
				_, ok := response.(*transportapi.GetAgentSnapshotNotFound)
				return ok
			},
		},
		{
			name: "inactive",
			err:  &dex.FlowNotActiveError{ServiceError: &dex.ServiceError{}},
			want: func(response transportapi.GetAgentSnapshotRes) bool {
				_, ok := response.(*transportapi.GetAgentSnapshotServiceUnavailable)
				return ok
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newTestHandler(&fakeAgentService{snapshotErr: test.err}, fakeCredentials{})
			response, err := handler.GetAgentSnapshot(context.Background(), transportapi.GetAgentSnapshotParams{
				FlowId: "flow-1",
				Limit:  transportapi.NewOptInt(50),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !test.want(response) {
				t.Fatalf("response type = %T", response)
			}
		})
	}
}

func TestQueueMutationsUseOnlyGeneratedMessageID(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{}
	handler := newTestHandler(service, fakeCredentials{})
	request := &transportapi.QueueMutationRequest{FlowId: "flow-1", MessageId: "message-1"}

	deleted, err := handler.DeleteQueuedMessage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	deleteResult, ok := deleted.(*transportapi.QueueMutationResponse)
	if !ok || deleteResult.Action != transportapi.QueueActionDeleted || service.deletedMessageID != "message-1" {
		t.Fatalf("delete response = %#v, message ID = %q", deleted, service.deletedMessageID)
	}

	steered, err := handler.SteerQueuedMessage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	steerResult, ok := steered.(*transportapi.QueueMutationResponse)
	if !ok || steerResult.Action != transportapi.QueueActionSteered || service.steeredMessageID != "message-1" {
		t.Fatalf("steer response = %#v, message ID = %q", steered, service.steeredMessageID)
	}
}

func TestStaleQueueMutationMapsToConflict(t *testing.T) {
	t.Parallel()
	stale := &agent.PendingMessageNotFoundError{MessageID: "message-1"}
	service := &fakeAgentService{deleteErr: stale, steerErr: stale}
	handler := newTestHandler(service, fakeCredentials{})
	request := &transportapi.QueueMutationRequest{FlowId: "flow-1", MessageId: "message-1"}

	deleted, err := handler.DeleteQueuedMessage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deleted.(*transportapi.DeleteQueuedMessageConflict); !ok {
		t.Fatalf("delete response type = %T", deleted)
	}

	steered, err := handler.SteerQueuedMessage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := steered.(*transportapi.SteerQueuedMessageConflict); !ok {
		t.Fatalf("steer response type = %T", steered)
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
	started          agent.AgentConfig
	startCalls       int
	sendErr          error
	snapshot         agent.AgentSnapshot
	snapshotErr      error
	deletedMessageID agent.MessageID
	deleteErr        error
	steeredMessageID agent.MessageID
	steerErr         error
	event            agent.StreamEvent
	eventErr         error
}

func (service *fakeAgentService) Start(_ context.Context, _ agent.FlowID, config agent.AgentConfig) (agent.RunID, error) {
	service.startCalls++
	service.started = config
	return "run-1", nil
}

func (service *fakeAgentService) SendMessage(context.Context, agent.FlowID, agent.UserMessage) error {
	return service.sendErr
}

func (service *fakeAgentService) Snapshot(
	context.Context,
	agent.FlowID,
	agent.SnapshotRequest,
) (agent.AgentSnapshot, error) {
	return service.snapshot, service.snapshotErr
}

func (service *fakeAgentService) DeleteQueuedMessage(
	_ context.Context,
	_ agent.FlowID,
	messageID agent.MessageID,
) error {
	service.deletedMessageID = messageID
	return service.deleteErr
}

func (service *fakeAgentService) SteerMessage(
	_ context.Context,
	_ agent.FlowID,
	request agent.SteerMessageRequest,
) error {
	service.steeredMessageID = request.MessageID
	return service.steerErr
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

type emptyToolCatalog struct{}

func (emptyToolCatalog) ServerNames() []string { return []string{} }

func (emptyToolCatalog) RegisteredTools() []agent.RegisteredTool { return []agent.RegisteredTool{} }

func (emptyToolCatalog) Definitions([]string, []agent.ToolName) []agent.ToolDefinition {
	return []agent.ToolDefinition{}
}

func fakeToolDefinition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "lookup", Description: "Look up a file", InputSchema: agent.MustJSONObject(`{"type":"object"}`)}
}

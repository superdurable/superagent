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
	"github.com/superdurable/superagent/internal/config"
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

func TestAnswerQuestionsMapsExactPendingBatch(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.AnswerQuestions(context.Background(), &transportapi.AnswerQuestionsRequest{
		FlowId: "flow-1", CallId: "call-1", Answers: []transportapi.UserInputAnswer{{
			QuestionId: "environment", Answer: "Production",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.(*transportapi.Accepted); !ok {
		t.Fatalf("response type = %T", response)
	}
	if service.answeredFlowID != "flow-1" || service.answer.CallID != "call-1" ||
		len(service.answer.Answers) != 1 || service.answer.Answers[0].QuestionID != "environment" ||
		service.answer.Answers[0].Answer != "Production" {
		t.Fatalf("answer = %q / %#v", service.answeredFlowID, service.answer)
	}
}

func TestAnswerQuestionsRejectionMapsToConflict(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{answerErr: &agent.CommandRejectedError{Command: agent.CommandAnswerQuestions}}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.AnswerQuestions(context.Background(), &transportapi.AnswerQuestionsRequest{
		FlowId: "flow-1", CallId: "stale-call", Answers: []transportapi.UserInputAnswer{{
			QuestionId: "environment", Answer: "Production",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.(*transportapi.AnswerQuestionsConflict); !ok {
		t.Fatalf("response type = %T", response)
	}
}

func TestExecutePlanRejectionExplainsTheExecutionBoundary(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{
		executeErr: &agent.CommandRejectedError{Command: agent.CommandExecutePlan},
	}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.ExecutePlan(context.Background(), &transportapi.ExecutePlanRequest{
		FlowId: "flow-1", Revision: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	conflict, ok := response.(*transportapi.ExecutePlanConflict)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	if conflict.Detail != "the Agent is not at an executable wait or the Plan revision changed" {
		t.Fatalf("conflict detail = %q", conflict.Detail)
	}
}

func TestReadEventMapsTypedActivity(t *testing.T) {
	t.Parallel()
	callID := agent.CallID("call-1")
	toolName := agent.ToolName("lookup")
	messageSequence := agent.Sequence(2)
	planBaseRevision := agent.PlanRevision(3)
	planRevision := agent.PlanRevision(4)
	planTaskIndex := agent.PlanTaskIndex(1)
	planTaskStatus := agent.TaskStatusInProgress
	service := &fakeAgentService{event: agent.StreamEvent{
		Kind: agent.StreamEventKindActivity,
		Activity: agent.AgentEvent{
			Kind: agent.EventKindPlanTaskUpdated, Message: "done", CallID: &callID, ToolName: &toolName,
			MessageSequence:  &messageSequence,
			PlanBaseRevision: &planBaseRevision, PlanRevision: &planRevision,
			PlanTaskIndex: &planTaskIndex, PlanTaskStatus: &planTaskStatus,
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
	if activity.Value.Kind != transportapi.EventKindPlanTaskUpdated ||
		activity.Value.CallId.Or("") != "call-1" ||
		activity.Value.MessageSequence.Or(0) != 2 ||
		activity.Value.PlanBaseRevision.Or(0) != 3 ||
		activity.Value.PlanRevision.Or(0) != 4 ||
		activity.Value.PlanTaskIndex.Or(-1) != 1 ||
		activity.Value.PlanTaskStatus.Or("") != transportapi.TaskStatusInProgress {
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

func TestListRecentEventsMapsChronologicalTailAndConfiguredLimit(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{recentEvents: []agent.StreamEvent{
		{
			Kind: agent.StreamEventKindReasoning, Text: "first", ResumeToken: "resume-1",
			CreatedAt: time.Unix(1, 0).UTC(), Source: "model-1",
		},
		{
			Kind: agent.StreamEventKindAssistant, Text: "second", ResumeToken: "resume-2",
			CreatedAt: time.Unix(2, 0).UTC(), Source: "model-1",
		},
	}}
	handler := newTestHandler(service, fakeCredentials{})
	handler.events.RecoveryLimit = 2
	response, err := handler.ListRecentEvents(context.Background(), transportapi.ListRecentEventsParams{
		FlowId: "flow-1",
		Stream: transportapi.EventStreamAssistant,
	})
	if err != nil {
		t.Fatal(err)
	}
	recent, ok := response.(*transportapi.RecentEvents)
	if !ok || len(recent.Events) != 2 {
		t.Fatalf("response = %#v", response)
	}
	first, isReasoning := recent.Events[0].GetReasoningStreamEvent()
	second, isAssistant := recent.Events[1].GetAssistantStreamEvent()
	if !isReasoning || !isAssistant || first.Value != "first" || second.Value != "second" {
		t.Fatalf("events = %#v", recent.Events)
	}
	if service.recentEventLimit != 2 {
		t.Fatalf("recovery limit = %d, want 2", service.recentEventLimit)
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
			InteractionStatus:          agent.AgentInteractionStatusWaiting,
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

func TestGetArchivedMessagesMapsExactChunkAndBoundary(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{archived: agent.HistoryPage{
		Messages: []agent.SequencedMessage{{
			Sequence: 1,
			Message:  agent.AgentMessage{Role: agent.MessageRoleUser, Content: "old", CreatedAt: time.Unix(1, 0).UTC()},
		}},
	}}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.GetArchivedMessages(context.Background(), transportapi.GetArchivedMessagesParams{
		FlowId: "flow-1", BeforeSequence: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	page, ok := response.(*transportapi.HistoryPageHeaders)
	if !ok || len(page.Response.Messages) != 1 || page.Response.Messages[0].Message.Content != "old" {
		t.Fatalf("response = %#v", response)
	}
	if page.CacheControl != transportapi.GetArchivedMessagesOKCacheControlNoStore {
		t.Fatalf("Cache-Control = %q", page.CacheControl)
	}

	invalid, err := handler.GetArchivedMessages(context.Background(), transportapi.GetArchivedMessagesParams{
		FlowId: "flow-1", BeforeSequence: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := invalid.(*transportapi.GetArchivedMessagesBadRequest); !ok {
		t.Fatalf("invalid boundary response = %T", invalid)
	}
	tooEarly, err := handler.GetArchivedMessages(context.Background(), transportapi.GetArchivedMessagesParams{
		FlowId: "flow-1", BeforeSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tooEarly.(*transportapi.GetArchivedMessagesBadRequest); !ok {
		t.Fatalf("too-early boundary response = %T", tooEarly)
	}
}

func TestWaitForAgentInteractionStatusReturnsDurableValueAndTimeout(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{}
	handler := newTestHandler(service, fakeCredentials{})
	response, err := handler.WaitForAgentInteractionStatus(context.Background(), transportapi.WaitForAgentInteractionStatusParams{
		FlowId: "flow-1", ExpectedStatus: transportapi.AgentInteractionStatusWaiting,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, ok := response.(*transportapi.AgentInteractionState)
	if !ok || state.Status != transportapi.AgentInteractionStatusWaiting || service.waitedStatus != agent.AgentInteractionStatusWaiting {
		t.Fatalf("response = %#v, waited = %q", response, service.waitedStatus)
	}

	service.waitErr = context.DeadlineExceeded
	response, err = handler.WaitForAgentInteractionStatus(context.Background(), transportapi.WaitForAgentInteractionStatusParams{
		FlowId: "flow-1", ExpectedStatus: transportapi.AgentInteractionStatusSubmitted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.(*transportapi.PollTimeout); !ok {
		t.Fatalf("timeout response = %T", response)
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
	return NewHandler(
		service,
		fakeToolCatalog{},
		credentials,
		&config.Events{RecoveryLimit: agent.MaximumRecentEventLimit},
		func() bool { return true },
		slog.New(slog.DiscardHandler),
	)
}

type fakeAgentService struct {
	started          agent.AgentConfig
	startCalls       int
	sendErr          error
	answeredFlowID   agent.FlowID
	answer           agent.AnswerQuestionsRequest
	answerErr        error
	snapshot         agent.AgentSnapshot
	snapshotErr      error
	archived         agent.HistoryPage
	archivedErr      error
	waitErr          error
	waitedStatus     agent.AgentInteractionStatus
	deletedMessageID agent.MessageID
	deleteErr        error
	steeredMessageID agent.MessageID
	steerErr         error
	executeErr       error
	event            agent.StreamEvent
	eventErr         error
	recentEvents     []agent.StreamEvent
	recentEventsErr  error
	recentEventLimit int
}

var _ AgentService = (*fakeAgentService)(nil)

func (service *fakeAgentService) Start(_ context.Context, _ agent.FlowID, request agent.StartRequest) (agent.RunID, error) {
	service.startCalls++
	service.started = request.Config
	return "run-1", nil
}

func (service *fakeAgentService) SendMessage(context.Context, agent.FlowID, agent.UserMessage) error {
	return service.sendErr
}

func (service *fakeAgentService) AnswerQuestions(
	_ context.Context,
	flowID agent.FlowID,
	request agent.AnswerQuestionsRequest,
) error {
	service.answeredFlowID = flowID
	service.answer = request
	return service.answerErr
}

func (service *fakeAgentService) GetSnapshot(
	context.Context,
	agent.FlowID,
) (agent.AgentSnapshot, error) {
	return service.snapshot, service.snapshotErr
}

func (service *fakeAgentService) GetArchivedMessages(
	context.Context,
	agent.FlowID,
	agent.Sequence,
) (agent.HistoryPage, error) {
	return service.archived, service.archivedErr
}

func (service *fakeAgentService) WaitForInteractionStatus(
	_ context.Context,
	_ agent.FlowID,
	status agent.AgentInteractionStatus,
) error {
	service.waitedStatus = status
	return service.waitErr
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

func (service *fakeAgentService) ExecutePlan(context.Context, agent.FlowID, agent.PlanExecutionRequest) error {
	return service.executeErr
}

func (service *fakeAgentService) ReadEvent(context.Context, agent.FlowID, agent.EventStream, agent.ResumeToken) (agent.StreamEvent, error) {
	return service.event, service.eventErr
}

func (service *fakeAgentService) ListRecentEvents(
	_ context.Context,
	_ agent.FlowID,
	_ agent.EventStream,
	limit int,
) ([]agent.StreamEvent, error) {
	service.recentEventLimit = limit
	return service.recentEvents, service.recentEventsErr
}

type fakeCredentials map[agent.Provider]bool

func (credentials fakeCredentials) HasAPIKey(_ agent.FlowID, provider agent.Provider) bool {
	return credentials[provider]
}

type fakeToolCatalog struct{}

var _ ToolCatalog = fakeToolCatalog{}

func (fakeToolCatalog) ServerNames() []string { return []string{"files"} }

func (fakeToolCatalog) RegisteredTools() []agent.RegisteredTool {
	return []agent.RegisteredTool{{ServerName: "files", RemoteName: "lookup", Definition: fakeToolDefinition()}}
}

func (fakeToolCatalog) Definitions([]string, []agent.ToolName) []agent.ToolDefinition {
	return []agent.ToolDefinition{fakeToolDefinition()}
}

type emptyToolCatalog struct{}

var _ ToolCatalog = emptyToolCatalog{}

func (emptyToolCatalog) ServerNames() []string { return []string{} }

func (emptyToolCatalog) RegisteredTools() []agent.RegisteredTool { return []agent.RegisteredTool{} }

func (emptyToolCatalog) Definitions([]string, []agent.ToolName) []agent.ToolDefinition {
	return []agent.ToolDefinition{}
}

func fakeToolDefinition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "lookup", Description: "Look up a file", InputSchema: agent.MustJSONObject(`{"type":"object"}`)}
}

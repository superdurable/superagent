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

// Package api adapts the generated HTTP contract to the typed Agent domain.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/superdurable/dex/sdk-go/dex"
	"github.com/superdurable/superagent/internal/agent"
	transportapi "github.com/superdurable/superagent/internal/api/generated"
)

// AgentService is the command and live-event surface consumed by HTTP.
type AgentService interface {
	Start(context.Context, agent.FlowID, agent.AgentConfig) (agent.RunID, error)
	SendMessage(context.Context, agent.FlowID, agent.UserMessage) error
	Snapshot(context.Context, agent.FlowID, agent.SnapshotRequest) (agent.AgentSnapshot, error)
	DeleteQueuedMessage(context.Context, agent.FlowID, agent.MessageID) error
	SteerMessage(context.Context, agent.FlowID, agent.SteerMessageRequest) error
	ApproveTool(context.Context, agent.FlowID, agent.ToolApprovalRequest) error
	ExecutePlan(context.Context, agent.FlowID, agent.PlanExecutionRequest) error
	ReadEvent(context.Context, agent.FlowID, agent.EventStream, agent.ResumeToken) (agent.StreamEvent, error)
}

// ToolCatalog is the immutable MCP projection consumed by the launch portal.
type ToolCatalog interface {
	ServerNames() []string
	RegisteredTools() []agent.RegisteredTool
	Definitions([]string, []agent.ToolName) []agent.ToolDefinition
}

// CredentialLookup reports process or Flow-specific provider configuration.
type CredentialLookup interface {
	HasAPIKey(agent.FlowID, agent.Provider) bool
}

// Readiness reports whether every required runtime dependency is ready.
type Readiness func() bool

// Handler implements the generated Phase 1 OpenAPI server.
type Handler struct {
	agent       AgentService
	tools       ToolCatalog
	credentials CredentialLookup
	ready       Readiness
	logger      *slog.Logger
}

// NewHandler constructs a generated-contract handler from required dependencies.
func NewHandler(
	agentService AgentService,
	tools ToolCatalog,
	credentials CredentialLookup,
	ready Readiness,
	logger *slog.Logger,
) *Handler {
	if agentService == nil || tools == nil || credentials == nil || ready == nil || logger == nil {
		panic("API handler dependencies are required")
	}
	return &Handler{
		agent:       agentService,
		tools:       tools,
		credentials: credentials,
		ready:       ready,
		logger:      logger,
	}
}

// GetHealth reports process liveness without probing dependencies.
func (*Handler) GetHealth(context.Context) (*transportapi.Health, error) {
	return healthy(), nil
}

// GetReadiness reports whether initialization completed and dependencies remain owned.
func (handler *Handler) GetReadiness(context.Context) (transportapi.GetReadinessRes, error) {
	if handler.ready() {
		return healthy(), nil
	}
	problem := newProblem(503, "Service Unavailable", "required dependencies are not ready")
	return &problem, nil
}

// GetPortal returns provider and currently discovered MCP choices.
func (handler *Handler) GetPortal(context.Context) (transportapi.GetPortalRes, error) {
	registered := handler.tools.RegisteredTools()
	servers := make(map[agent.ToolName]string, len(registered))
	for _, tool := range registered {
		servers[tool.Definition.Name] = tool.ServerName
	}
	definitions := handler.tools.Definitions(nil, nil)
	tools := make([]transportapi.PortalTool, 0, len(definitions))
	for _, definition := range definitions {
		server := transportapi.NilString{}
		if name, found := servers[definition.Name]; found {
			server.SetTo(name)
		} else {
			server.SetToNull()
		}
		tools = append(tools, transportapi.PortalTool{
			Name:             transportapi.ToolName(definition.Name),
			Description:      definition.Description,
			RequiresApproval: definition.RequiresApproval,
			Server:           server,
		})
	}
	return &transportapi.Portal{
		Providers:    handler.portalProviders(),
		McpServers:   handler.tools.ServerNames(),
		Tools:        tools,
		BuiltInTools: []transportapi.ToolName{transportapi.ToolName(agent.ToolNameWriteTodos), transportapi.ToolName(agent.ToolNameRequestUserInput), transportapi.ToolName(agent.ToolNameDurableWait)},
	}, nil
}

// StartAgent validates transport choices and starts one durable Flow.
func (handler *Handler) StartAgent(ctx context.Context, request *transportapi.StartAgentRequest) (transportapi.StartAgentRes, error) {
	provider, err := domainProvider(request.Provider)
	if err != nil {
		return startProblem(problemBadRequest(err)), nil
	}
	model, err := qualifyModel(provider, request.Model)
	if err != nil {
		return startProblem(problemBadRequest(err)), nil
	}
	flowID := agent.FlowID(request.FlowId)
	if provider != agent.ProviderMock && !handler.credentials.HasAPIKey(flowID, provider) {
		return startProblem(problemBadRequest(fmt.Errorf("provider %q is not configured; set %s and restart Superagent", provider, providerEnvironmentVariable(provider)))), nil
	}
	config := agent.NewAgentConfig()
	config.Model = model
	config.SystemPrompt = request.SystemPrompt
	config.MaxContextTokens = request.MaxContextTokens
	config.MessageRetentionLimit = request.MessageRetentionLimit
	config.MCPEnabled = request.McpEnabled
	config.EnabledMCPServers = append([]string(nil), request.EnabledMcpServers...)
	config.EnabledTools = make([]agent.ToolName, len(request.EnabledTools))
	for index, name := range request.EnabledTools {
		config.EnabledTools[index] = agent.ToolName(name)
	}
	config.CompactionTriggerFraction = request.CompactionTriggerFraction.Or(config.CompactionTriggerFraction)
	config.CompactionKeepFraction = request.CompactionKeepFraction.Or(config.CompactionKeepFraction)
	if request.CompactionModel.IsSet() && !request.CompactionModel.IsNull() {
		compactionModel, qualifyErr := qualifyModel(provider, request.CompactionModel.Value)
		if qualifyErr != nil {
			return startProblem(problemBadRequest(fmt.Errorf("compaction model: %w", qualifyErr))), nil
		}
		config.CompactionModel = &compactionModel
	}
	if err := config.Validate(); err != nil {
		return startProblem(problemBadRequest(err)), nil
	}
	if _, err := handler.agent.Start(ctx, flowID, config); err != nil {
		return handler.startError(ctx, flowID, err), nil
	}
	return &transportapi.StartAgentResponse{FlowId: request.FlowId}, nil
}

// SendMessage durably accepts one user message command.
func (handler *Handler) SendMessage(ctx context.Context, request *transportapi.SendMessageRequest) (transportapi.SendMessageRes, error) {
	err := handler.agent.SendMessage(ctx, agent.FlowID(request.FlowId), agent.UserMessage{
		Content:  request.Content,
		PlanMode: request.PlanMode,
	})
	if err != nil {
		return handler.sendMessageError(ctx, agent.FlowID(request.FlowId), err), nil
	}
	return accepted(), nil
}

// GetAgentSnapshot returns one atomic durable application view.
func (handler *Handler) GetAgentSnapshot(
	ctx context.Context,
	params transportapi.GetAgentSnapshotParams,
) (transportapi.GetAgentSnapshotRes, error) {
	request := agent.SnapshotRequest{Limit: params.Limit.Or(50)}
	if beforeSequence, ok := params.BeforeSequence.Get(); ok {
		sequence := agent.Sequence(beforeSequence)
		request.BeforeSequence = &sequence
	}
	flowID := agent.FlowID(params.FlowId)
	snapshot, err := handler.agent.Snapshot(ctx, flowID, request)
	if err != nil {
		return handler.snapshotError(ctx, flowID, err), nil
	}
	result, err := transportSnapshot(snapshot)
	if err != nil {
		handler.logFailure(ctx, flowID, err)
		problem := newProblem(503, "Service Unavailable", "the Agent Snapshot could not be encoded")
		return (*transportapi.GetAgentSnapshotServiceUnavailable)(&problem), nil
	}
	return &transportapi.AgentSnapshotHeaders{
		CacheControl: transportapi.GetAgentSnapshotOKCacheControlNoStore,
		Response:     result,
	}, nil
}

// DeleteQueuedMessage removes one exact pending queued user message.
func (handler *Handler) DeleteQueuedMessage(
	ctx context.Context,
	request *transportapi.QueueMutationRequest,
) (transportapi.DeleteQueuedMessageRes, error) {
	flowID := agent.FlowID(request.FlowId)
	messageID := agent.MessageID(request.MessageId)
	if err := handler.agent.DeleteQueuedMessage(ctx, flowID, messageID); err != nil {
		return handler.deleteQueuedMessageError(ctx, flowID, err), nil
	}
	return &transportapi.QueueMutationResponse{
		MessageId: request.MessageId,
		Action:    transportapi.QueueActionDeleted,
	}, nil
}

// SteerQueuedMessage atomically moves one queued message into steering.
func (handler *Handler) SteerQueuedMessage(
	ctx context.Context,
	request *transportapi.QueueMutationRequest,
) (transportapi.SteerQueuedMessageRes, error) {
	flowID := agent.FlowID(request.FlowId)
	messageID := agent.MessageID(request.MessageId)
	if err := handler.agent.SteerMessage(ctx, flowID, agent.SteerMessageRequest{MessageID: messageID}); err != nil {
		return handler.steerQueuedMessageError(ctx, flowID, err), nil
	}
	return &transportapi.QueueMutationResponse{
		MessageId: request.MessageId,
		Action:    transportapi.QueueActionSteered,
	}, nil
}

// ExecutePlan durably accepts one exact plan revision command.
func (handler *Handler) ExecutePlan(ctx context.Context, request *transportapi.ExecutePlanRequest) (transportapi.ExecutePlanRes, error) {
	err := handler.agent.ExecutePlan(ctx, agent.FlowID(request.FlowId), agent.PlanExecutionRequest{
		Revision: agent.PlanRevision(request.Revision),
	})
	if err != nil {
		return handler.executePlanError(ctx, agent.FlowID(request.FlowId), err), nil
	}
	return accepted(), nil
}

// ApproveTool durably resolves one exact pending tool approval.
func (handler *Handler) ApproveTool(ctx context.Context, request *transportapi.ToolApprovalRequest) (transportapi.ApproveToolRes, error) {
	err := handler.agent.ApproveTool(ctx, agent.FlowID(request.FlowId), agent.ToolApprovalRequest{
		CallID:   agent.CallID(request.CallId),
		Approved: request.Approved,
	})
	if err != nil {
		return handler.approveToolError(ctx, agent.FlowID(request.FlowId), err), nil
	}
	return accepted(), nil
}

// ReadEvent returns one typed best-effort Stream message.
func (handler *Handler) ReadEvent(ctx context.Context, params transportapi.ReadEventParams) (transportapi.ReadEventRes, error) {
	stream, err := domainEventStream(params.Stream)
	if err != nil {
		problem := problemBadRequest(err)
		return (*transportapi.ReadEventBadRequest)(&problem), nil
	}
	event, err := handler.agent.ReadEvent(ctx, agent.FlowID(params.FlowId), stream, agent.ResumeToken(params.ResumeToken.Or("")))
	if err != nil {
		return handler.readEventError(ctx, agent.FlowID(params.FlowId), err)
	}
	result, err := transportStreamEvent(event)
	if err != nil {
		handler.logFailure(ctx, agent.FlowID(params.FlowId), err)
		problem := newProblem(503, "Service Unavailable", "the event could not be encoded")
		return (*transportapi.ReadEventServiceUnavailable)(&problem), nil
	}
	return &result, nil
}

func (handler *Handler) portalProviders() []transportapi.PortalProvider {
	definitions := []struct {
		provider     agent.Provider
		transport    transportapi.Provider
		label        string
		prefix       string
		defaultModel string
	}{
		{agent.ProviderMock, transportapi.ProviderMock, "Local mock", "", "mock/dex"},
		{agent.ProviderOpenAI, transportapi.ProviderOpenai, "OpenAI", "openai", "gpt-5-mini"},
		{agent.ProviderAnthropic, transportapi.ProviderAnthropic, "Anthropic", "anthropic", "claude-sonnet-4-5"},
		{agent.ProviderGemini, transportapi.ProviderGemini, "Google Gemini", "gemini", "gemini-2.5-flash"},
		{agent.ProviderGroq, transportapi.ProviderGroq, "Groq", "groq", "llama-3.3-70b-versatile"},
	}
	providers := make([]transportapi.PortalProvider, 0, len(definitions))
	for _, definition := range definitions {
		environment := transportapi.NilString{}
		if definition.provider == agent.ProviderMock {
			environment.SetToNull()
		} else {
			environment.SetTo(providerEnvironmentVariable(definition.provider))
		}
		providers = append(providers, transportapi.PortalProvider{
			ID:                            definition.transport,
			Label:                         definition.label,
			ModelPrefix:                   definition.prefix,
			DefaultModel:                  definition.defaultModel,
			CredentialEnvironmentVariable: environment,
			Configured:                    definition.provider == agent.ProviderMock || handler.credentials.HasAPIKey("", definition.provider),
		})
	}
	return providers
}

func domainProvider(provider transportapi.Provider) (agent.Provider, error) {
	switch provider {
	case transportapi.ProviderMock:
		return agent.ProviderMock, nil
	case transportapi.ProviderOpenai:
		return agent.ProviderOpenAI, nil
	case transportapi.ProviderAnthropic:
		return agent.ProviderAnthropic, nil
	case transportapi.ProviderGemini:
		return agent.ProviderGemini, nil
	case transportapi.ProviderGroq:
		return agent.ProviderGroq, nil
	default:
		return "", &agent.EnumValidationError{Type: "Provider", Value: string(provider)}
	}
}

func domainEventStream(stream transportapi.EventStream) (agent.EventStream, error) {
	switch stream {
	case transportapi.EventStreamReasoning:
		return agent.EventStreamReasoning, nil
	case transportapi.EventStreamAssistant:
		return agent.EventStreamAssistant, nil
	case transportapi.EventStreamActivity:
		return agent.EventStreamActivity, nil
	default:
		return "", &agent.EnumValidationError{Type: "EventStream", Value: string(stream)}
	}
}

func qualifyModel(provider agent.Provider, requested string) (agent.Model, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("model must not be empty")
	}
	model := agent.Model(requested)
	if strings.Contains(requested, "/") {
		modelProvider, err := model.Provider()
		if err != nil {
			return "", err
		}
		if modelProvider != provider {
			return "", fmt.Errorf("model provider %q does not match selected provider %q", modelProvider, provider)
		}
		return model, nil
	}
	if provider == agent.ProviderMock {
		return "", errors.New("mock model must be mock/dex")
	}
	return agent.Model(string(provider) + "/" + requested), nil
}

func providerEnvironmentVariable(provider agent.Provider) string {
	switch provider {
	case agent.ProviderOpenAI:
		return "OPENAI_API_KEY"
	case agent.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case agent.ProviderGemini:
		return "GEMINI_API_KEY"
	case agent.ProviderGroq:
		return "GROQ_API_KEY"
	case agent.ProviderMock:
		return ""
	default:
		return ""
	}
}

func transportSnapshot(snapshot agent.AgentSnapshot) (transportapi.AgentSnapshot, error) {
	history, err := transportHistoryPage(snapshot.History)
	if err != nil {
		return transportapi.AgentSnapshot{}, err
	}
	flowStatus, err := transportFlowStatus(snapshot.FlowStatus)
	if err != nil {
		return transportapi.AgentSnapshot{}, err
	}
	description, err := transportOptionalAgentDescription(snapshot.Description)
	if err != nil {
		return transportapi.AgentSnapshot{}, err
	}
	errorType, err := transportOptionalFlowErrorType(snapshot.ErrorType)
	if err != nil {
		return transportapi.AgentSnapshot{}, err
	}
	errorMessage := transportapi.NilString{}
	if snapshot.ErrorMessage == nil {
		errorMessage.SetToNull()
	} else {
		errorMessage.SetTo(*snapshot.ErrorMessage)
	}
	return transportapi.AgentSnapshot{
		RunId:        transportapi.RunID(snapshot.RunID),
		FlowStatus:   flowStatus,
		ErrorType:    errorType,
		ErrorMessage: errorMessage,
		History:      history,
		Description:  description,
		Queued:       transportPendingUserMessages(snapshot.Queued),
		Steered:      transportPendingUserMessages(snapshot.Steered),
	}, nil
}

func transportOptionalAgentDescription(description *agent.AgentDescription) (transportapi.NilAgentDescription, error) {
	result := transportapi.NilAgentDescription{}
	if description == nil {
		result.SetToNull()
		return result, nil
	}
	mapped, err := transportAgentDescription(*description)
	if err != nil {
		return transportapi.NilAgentDescription{}, err
	}
	result.SetTo(mapped)
	return result, nil
}

func transportOptionalFlowErrorType(errorType *agent.FlowErrorType) (transportapi.NilFlowErrorType, error) {
	result := transportapi.NilFlowErrorType{}
	if errorType == nil {
		result.SetToNull()
		return result, nil
	}
	mapped, err := transportFlowErrorType(*errorType)
	if err != nil {
		return transportapi.NilFlowErrorType{}, err
	}
	result.SetTo(mapped)
	return result, nil
}

func transportHistoryPage(page agent.HistoryPage) (transportapi.HistoryPage, error) {
	messages := make([]transportapi.SequencedMessage, 0, len(page.Messages))
	for _, message := range page.Messages {
		mapped, err := transportSequencedMessage(message)
		if err != nil {
			return transportapi.HistoryPage{}, err
		}
		messages = append(messages, mapped)
	}
	nextBeforeSequence := transportapi.NilSequence{}
	if page.NextBeforeSequence == nil {
		nextBeforeSequence.SetToNull()
	} else {
		nextBeforeSequence.SetTo(transportapi.Sequence(*page.NextBeforeSequence))
	}
	return transportapi.HistoryPage{
		Messages:           messages,
		NextBeforeSequence: nextBeforeSequence,
	}, nil
}

func transportSequencedMessage(message agent.SequencedMessage) (transportapi.SequencedMessage, error) {
	mapped, err := transportAgentMessage(message.Message)
	if err != nil {
		return transportapi.SequencedMessage{}, err
	}
	return transportapi.SequencedMessage{
		Sequence: transportapi.Sequence(message.Sequence),
		Message:  mapped,
	}, nil
}

func transportAgentMessage(message agent.AgentMessage) (transportapi.AgentMessage, error) {
	role, err := transportMessageRole(message.Role)
	if err != nil {
		return transportapi.AgentMessage{}, err
	}
	toolCalls := make([]transportapi.ToolCall, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		toolCalls = append(toolCalls, transportapi.ToolCall{
			ID:            transportapi.CallID(call.ID),
			Name:          transportapi.ToolName(call.Name),
			ArgumentsJson: call.Arguments.String(),
		})
	}
	toolCallID := transportapi.NilCallID{}
	if message.ToolCallID == nil {
		toolCallID.SetToNull()
	} else {
		toolCallID.SetTo(transportapi.CallID(*message.ToolCallID))
	}
	toolName := transportapi.NilToolName{}
	if message.ToolName == nil {
		toolName.SetToNull()
	} else {
		toolName.SetTo(transportapi.ToolName(*message.ToolName))
	}
	return transportapi.AgentMessage{
		Role:       role,
		Content:    message.Content,
		ToolCalls:  toolCalls,
		ToolCallId: toolCallID,
		ToolName:   toolName,
		CreatedAt:  message.CreatedAt,
	}, nil
}

func transportAgentDescription(description agent.AgentDescription) (transportapi.AgentDescription, error) {
	status, err := transportAgentStatus(description.Status)
	if err != nil {
		return transportapi.AgentDescription{}, err
	}
	plan, err := transportOptionalAgentPlan(description.Plan)
	if err != nil {
		return transportapi.AgentDescription{}, err
	}
	availableTools := make([]transportapi.ToolName, len(description.AvailableTools))
	for index, name := range description.AvailableTools {
		availableTools[index] = transportapi.ToolName(name)
	}
	availableMCPServers := make([]string, len(description.AvailableMCPServers))
	copy(availableMCPServers, description.AvailableMCPServers)
	return transportapi.AgentDescription{
		Status:                     status,
		Model:                      string(description.Model),
		SystemPrompt:               description.SystemPrompt,
		FirstRetainedSequence:      int64(description.FirstRetainedSequence),
		LastSequence:               int64(description.LastSequence),
		SummarizedThroughSequence:  int64(description.SummarizedThroughSequence),
		PendingApproval:            transportOptionalPendingApproval(description.PendingApproval),
		PendingTimer:               transportOptionalPendingTimer(description.PendingTimer),
		PendingUserInput:           transportOptionalPendingUserInput(description.PendingUserInput),
		Plan:                       plan,
		IsPlanExecutionRequested:   description.IsPlanExecutionRequested,
		PendingQueuedMessageCount:  description.PendingQueuedMessageCount,
		PendingSteeredMessageCount: description.PendingSteeredMessageCount,
		AvailableMcpServers:        availableMCPServers,
		AvailableTools:             availableTools,
	}, nil
}

func transportPendingUserMessages(messages []agent.PendingUserMessage) []transportapi.PendingUserMessage {
	result := make([]transportapi.PendingUserMessage, 0, len(messages))
	for _, message := range messages {
		result = append(result, transportapi.PendingUserMessage{
			MessageId: transportapi.MessageID(message.MessageID),
			Value: transportapi.UserMessage{
				Content:  message.Value.Content,
				PlanMode: message.Value.PlanMode,
			},
		})
	}
	return result
}

func transportOptionalPendingApproval(pending *agent.PendingApproval) transportapi.NilPendingApproval {
	result := transportapi.NilPendingApproval{}
	if pending == nil {
		result.SetToNull()
		return result
	}
	result.SetTo(transportapi.PendingApproval{
		CallId:        transportapi.CallID(pending.CallID),
		ToolName:      transportapi.ToolName(pending.ToolName),
		ArgumentsJson: pending.Arguments.String(),
	})
	return result
}

func transportOptionalPendingTimer(pending *agent.PendingTimer) transportapi.NilPendingTimer {
	result := transportapi.NilPendingTimer{}
	if pending == nil {
		result.SetToNull()
		return result
	}
	result.SetTo(transportapi.PendingTimer{
		CallId:          transportapi.CallID(pending.CallID),
		DurationSeconds: pending.DurationSeconds,
		Reason:          pending.Reason,
	})
	return result
}

func transportOptionalPendingUserInput(pending *agent.PendingUserInput) transportapi.NilPendingUserInput {
	result := transportapi.NilPendingUserInput{}
	if pending == nil {
		result.SetToNull()
		return result
	}
	result.SetTo(transportapi.PendingUserInput{
		CallId:  transportapi.CallID(pending.CallID),
		Prompt:  pending.Prompt,
		Choices: append([]string(nil), pending.Choices...),
	})
	return result
}

func transportOptionalAgentPlan(plan *agent.AgentPlan) (transportapi.NilAgentPlan, error) {
	result := transportapi.NilAgentPlan{}
	if plan == nil {
		result.SetToNull()
		return result, nil
	}
	status, err := transportPlanStatus(plan.Status)
	if err != nil {
		return transportapi.NilAgentPlan{}, err
	}
	tasks := make([]transportapi.PlanTask, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		taskStatus, err := transportTaskStatus(task.Status)
		if err != nil {
			return transportapi.NilAgentPlan{}, err
		}
		tasks = append(tasks, transportapi.PlanTask{Content: task.Content, Status: taskStatus})
	}
	result.SetTo(transportapi.AgentPlan{
		Revision: int64(plan.Revision),
		Status:   status,
		Tasks:    tasks,
	})
	return result, nil
}

func transportFlowStatus(status agent.FlowStatus) (transportapi.FlowStatus, error) {
	switch status {
	case agent.FlowStatusRunning:
		return transportapi.FlowStatusRunning, nil
	case agent.FlowStatusCompleted:
		return transportapi.FlowStatusCompleted, nil
	case agent.FlowStatusFailed:
		return transportapi.FlowStatusFailed, nil
	case agent.FlowStatusTerminated:
		return transportapi.FlowStatusTerminated, nil
	case agent.FlowStatusCanceled:
		return transportapi.FlowStatusCanceled, nil
	case agent.FlowStatusContinuedAsNew:
		return transportapi.FlowStatusContinuedAsNew, nil
	default:
		return "", &agent.EnumValidationError{Type: "FlowStatus", Value: string(status)}
	}
}

func transportFlowErrorType(errorType agent.FlowErrorType) (transportapi.FlowErrorType, error) {
	switch errorType {
	case agent.FlowErrorTypeStepDecision:
		return transportapi.FlowErrorTypeStepDecision, nil
	case agent.FlowErrorTypeClientAPI:
		return transportapi.FlowErrorTypeClientAPI, nil
	case agent.FlowErrorTypeWorkerMethod:
		return transportapi.FlowErrorTypeWorkerMethod, nil
	case agent.FlowErrorTypeInvalidUserCode:
		return transportapi.FlowErrorTypeInvalidUserCode, nil
	case agent.FlowErrorTypeInternal:
		return transportapi.FlowErrorTypeInternal, nil
	case agent.FlowErrorTypeTimeout:
		return transportapi.FlowErrorTypeTimeout, nil
	default:
		return "", &agent.EnumValidationError{Type: "FlowErrorType", Value: string(errorType)}
	}
}

func transportAgentStatus(status agent.AgentStatus) (transportapi.AgentStatus, error) {
	switch status {
	case agent.AgentStatusInitializing:
		return transportapi.AgentStatusInitializing, nil
	case agent.AgentStatusWaitingForMessage:
		return transportapi.AgentStatusWaitingForMessage, nil
	case agent.AgentStatusCompactingContext:
		return transportapi.AgentStatusCompactingContext, nil
	case agent.AgentStatusCallingModel:
		return transportapi.AgentStatusCallingModel, nil
	case agent.AgentStatusRoutingTool:
		return transportapi.AgentStatusRoutingTool, nil
	case agent.AgentStatusWaitingForToolApproval:
		return transportapi.AgentStatusWaitingForToolApproval, nil
	case agent.AgentStatusExecutingTool:
		return transportapi.AgentStatusExecutingTool, nil
	case agent.AgentStatusWaitingForTimer:
		return transportapi.AgentStatusWaitingForTimer, nil
	case agent.AgentStatusApplyingSteering:
		return transportapi.AgentStatusApplyingSteering, nil
	default:
		return "", &agent.EnumValidationError{Type: "AgentStatus", Value: string(status)}
	}
}

func transportMessageRole(role agent.MessageRole) (transportapi.MessageRole, error) {
	switch role {
	case agent.MessageRoleSystem:
		return transportapi.MessageRoleSystem, nil
	case agent.MessageRoleUser:
		return transportapi.MessageRoleUser, nil
	case agent.MessageRoleAssistant:
		return transportapi.MessageRoleAssistant, nil
	case agent.MessageRoleTool:
		return transportapi.MessageRoleTool, nil
	default:
		return "", &agent.EnumValidationError{Type: "MessageRole", Value: string(role)}
	}
}

func transportPlanStatus(status agent.PlanStatus) (transportapi.PlanStatus, error) {
	switch status {
	case agent.PlanStatusDraft:
		return transportapi.PlanStatusDraft, nil
	case agent.PlanStatusActive:
		return transportapi.PlanStatusActive, nil
	case agent.PlanStatusCompleted:
		return transportapi.PlanStatusCompleted, nil
	default:
		return "", &agent.EnumValidationError{Type: "PlanStatus", Value: string(status)}
	}
}

func transportTaskStatus(status agent.TaskStatus) (transportapi.TaskStatus, error) {
	switch status {
	case agent.TaskStatusPending:
		return transportapi.TaskStatusPending, nil
	case agent.TaskStatusInProgress:
		return transportapi.TaskStatusInProgress, nil
	case agent.TaskStatusCompleted:
		return transportapi.TaskStatusCompleted, nil
	default:
		return "", &agent.EnumValidationError{Type: "TaskStatus", Value: string(status)}
	}
}

func transportStreamEvent(event agent.StreamEvent) (transportapi.StreamEvent, error) {
	baseToken := transportapi.ResumeToken(event.ResumeToken)
	switch event.Kind {
	case agent.StreamEventKindReasoning:
		return transportapi.NewReasoningStreamEventStreamEvent(transportapi.ReasoningStreamEvent{
			Kind: transportapi.ReasoningStreamEventKindReasoningSummary, Value: event.Text,
			ResumeToken: baseToken, CreatedAt: event.CreatedAt, Source: event.Source,
		}), nil
	case agent.StreamEventKindAssistant:
		return transportapi.NewAssistantStreamEventStreamEvent(transportapi.AssistantStreamEvent{
			Kind: transportapi.AssistantStreamEventKindAssistantText, Value: event.Text,
			ResumeToken: baseToken, CreatedAt: event.CreatedAt, Source: event.Source,
		}), nil
	case agent.StreamEventKindActivity:
		activity, err := transportActivity(event.Activity)
		if err != nil {
			return transportapi.StreamEvent{}, err
		}
		return transportapi.NewActivityStreamEventStreamEvent(transportapi.ActivityStreamEvent{
			Kind: transportapi.ActivityStreamEventKindActivity, Value: activity,
			ResumeToken: baseToken, CreatedAt: event.CreatedAt, Source: event.Source,
		}), nil
	default:
		return transportapi.StreamEvent{}, &agent.EnumValidationError{Type: "StreamEventKind", Value: string(event.Kind)}
	}
}

func transportActivity(event agent.AgentEvent) (transportapi.AgentEvent, error) {
	kind, err := transportEventKind(event.Kind)
	if err != nil {
		return transportapi.AgentEvent{}, err
	}
	callID := transportapi.NilCallID{}
	if event.CallID == nil {
		callID.SetToNull()
	} else {
		callID.SetTo(transportapi.CallID(*event.CallID))
	}
	toolName := transportapi.NilToolName{}
	if event.ToolName == nil {
		toolName.SetToNull()
	} else {
		toolName.SetTo(transportapi.ToolName(*event.ToolName))
	}
	messageSequence := transportapi.NilSequence{}
	if event.MessageSequence == nil {
		messageSequence.SetToNull()
	} else {
		messageSequence.SetTo(transportapi.Sequence(*event.MessageSequence))
	}
	return transportapi.AgentEvent{
		Kind:            kind,
		Message:         event.Message,
		CallId:          callID,
		ToolName:        toolName,
		MessageSequence: messageSequence,
	}, nil
}

func transportEventKind(kind agent.EventKind) (transportapi.EventKind, error) {
	switch kind {
	case agent.EventKindPlanStarted:
		return transportapi.EventKindPlanStarted, nil
	case agent.EventKindPlanUpdated:
		return transportapi.EventKindPlanUpdated, nil
	case agent.EventKindSteeringApplied:
		return transportapi.EventKindSteeringApplied, nil
	case agent.EventKindCompactionFailed:
		return transportapi.EventKindCompactionFailed, nil
	case agent.EventKindCompacted:
		return transportapi.EventKindCompacted, nil
	case agent.EventKindModelStarted:
		return transportapi.EventKindModelStarted, nil
	case agent.EventKindModelFailed:
		return transportapi.EventKindModelFailed, nil
	case agent.EventKindModelCompleted:
		return transportapi.EventKindModelCompleted, nil
	case agent.EventKindModelToolCall:
		return transportapi.EventKindModelToolCall, nil
	case agent.EventKindUserInputRequested:
		return transportapi.EventKindUserInputRequested, nil
	case agent.EventKindToolProgress:
		return transportapi.EventKindToolProgress, nil
	case agent.EventKindToolFailed:
		return transportapi.EventKindToolFailed, nil
	case agent.EventKindToolCompleted:
		return transportapi.EventKindToolCompleted, nil
	default:
		return "", &agent.EnumValidationError{Type: "EventKind", Value: string(kind)}
	}
}

func healthy() *transportapi.Health {
	return &transportapi.Health{Status: transportapi.HealthStatusOk}
}

func accepted() *transportapi.Accepted {
	return &transportapi.Accepted{Accepted: transportapi.AcceptedAcceptedTrue}
}

func problemBadRequest(err error) transportapi.Problem {
	return newProblem(400, "Bad Request", err.Error())
}

func newProblem(status int, title string, detail string) transportapi.Problem {
	return transportapi.Problem{
		Type:   url.URL{Scheme: "about", Opaque: "blank"},
		Title:  title,
		Status: status,
		Detail: detail,
	}
}

type failureKind uint8

const (
	failureUnavailable failureKind = iota
	failureNotFound
	failureConflict
)

func classifyFailure(err error) failureKind {
	var notFound *dex.FlowNotFoundError
	var conflict *dex.RPCLockConflictError
	var inactive *dex.FlowNotActiveError
	var duplicate *dex.FlowAlreadyStartedError
	var channelMessageNotFound *dex.ChannelMessageNotFoundError
	var rejected *agent.CommandRejectedError
	var pendingMessageNotFound *agent.PendingMessageNotFoundError
	switch {
	case errors.As(err, &notFound):
		return failureNotFound
	case errors.As(err, &conflict),
		errors.As(err, &inactive),
		errors.As(err, &duplicate),
		errors.As(err, &channelMessageNotFound),
		errors.As(err, &rejected),
		errors.As(err, &pendingMessageNotFound):
		return failureConflict
	default:
		return failureUnavailable
	}
}

func (handler *Handler) logFailure(ctx context.Context, flowID agent.FlowID, err error) {
	handler.logger.ErrorContext(ctx, "Agent operation failed",
		slog.String("flow_id", string(flowID)),
		slog.String("error_type", fmt.Sprintf("%T", err)),
	)
}

func (handler *Handler) startError(ctx context.Context, flowID agent.FlowID, err error) transportapi.StartAgentRes {
	handler.logFailure(ctx, flowID, err)
	switch classifyFailure(err) {
	case failureConflict:
		problem := newProblem(409, "Conflict", "the Flow ID has already been used")
		return (*transportapi.StartAgentConflict)(&problem)
	default:
		problem := newProblem(503, "Service Unavailable", "the Agent Flow could not be started")
		return (*transportapi.StartAgentServiceUnavailable)(&problem)
	}
}

func (handler *Handler) sendMessageError(ctx context.Context, flowID agent.FlowID, err error) transportapi.SendMessageRes {
	handler.logFailure(ctx, flowID, err)
	problem, kind := commandProblem(err)
	switch kind {
	case failureNotFound:
		return (*transportapi.SendMessageNotFound)(&problem)
	case failureConflict:
		return (*transportapi.SendMessageConflict)(&problem)
	default:
		return (*transportapi.SendMessageServiceUnavailable)(&problem)
	}
}

func (handler *Handler) executePlanError(ctx context.Context, flowID agent.FlowID, err error) transportapi.ExecutePlanRes {
	handler.logFailure(ctx, flowID, err)
	problem, kind := commandProblem(err)
	switch kind {
	case failureNotFound:
		return (*transportapi.ExecutePlanNotFound)(&problem)
	case failureConflict:
		return (*transportapi.ExecutePlanConflict)(&problem)
	default:
		return (*transportapi.ExecutePlanServiceUnavailable)(&problem)
	}
}

func (handler *Handler) approveToolError(ctx context.Context, flowID agent.FlowID, err error) transportapi.ApproveToolRes {
	handler.logFailure(ctx, flowID, err)
	problem, kind := commandProblem(err)
	switch kind {
	case failureNotFound:
		return (*transportapi.ApproveToolNotFound)(&problem)
	case failureConflict:
		return (*transportapi.ApproveToolConflict)(&problem)
	default:
		return (*transportapi.ApproveToolServiceUnavailable)(&problem)
	}
}

func (handler *Handler) snapshotError(ctx context.Context, flowID agent.FlowID, err error) transportapi.GetAgentSnapshotRes {
	handler.logFailure(ctx, flowID, err)
	switch classifyFailure(err) {
	case failureNotFound:
		problem := newProblem(404, "Not Found", "the Agent Flow does not exist")
		return (*transportapi.GetAgentSnapshotNotFound)(&problem)
	default:
		problem := newProblem(503, "Service Unavailable", "the Agent Snapshot is unavailable")
		return (*transportapi.GetAgentSnapshotServiceUnavailable)(&problem)
	}
}

func (handler *Handler) deleteQueuedMessageError(
	ctx context.Context,
	flowID agent.FlowID,
	err error,
) transportapi.DeleteQueuedMessageRes {
	handler.logFailure(ctx, flowID, err)
	switch classifyFailure(err) {
	case failureNotFound:
		problem := newProblem(404, "Not Found", "the Agent Flow does not exist")
		return (*transportapi.DeleteQueuedMessageNotFound)(&problem)
	case failureConflict:
		problem := newProblem(409, "Conflict", "the queued message is no longer pending")
		return (*transportapi.DeleteQueuedMessageConflict)(&problem)
	default:
		problem := newProblem(503, "Service Unavailable", "the queued message could not be deleted")
		return (*transportapi.DeleteQueuedMessageServiceUnavailable)(&problem)
	}
}

func (handler *Handler) steerQueuedMessageError(
	ctx context.Context,
	flowID agent.FlowID,
	err error,
) transportapi.SteerQueuedMessageRes {
	handler.logFailure(ctx, flowID, err)
	switch classifyFailure(err) {
	case failureNotFound:
		problem := newProblem(404, "Not Found", "the Agent Flow does not exist")
		return (*transportapi.SteerQueuedMessageNotFound)(&problem)
	case failureConflict:
		problem := newProblem(409, "Conflict", "the queued message is no longer pending")
		return (*transportapi.SteerQueuedMessageConflict)(&problem)
	default:
		problem := newProblem(503, "Service Unavailable", "the queued message could not be steered")
		return (*transportapi.SteerQueuedMessageServiceUnavailable)(&problem)
	}
}

func commandProblem(err error) (transportapi.Problem, failureKind) {
	kind := classifyFailure(err)
	switch kind {
	case failureNotFound:
		return newProblem(404, "Not Found", "the Agent Flow does not exist"), kind
	case failureConflict:
		return newProblem(409, "Conflict", "the command conflicts with current durable state"), kind
	default:
		return newProblem(503, "Service Unavailable", "the command could not be completed"), kind
	}
}

func (handler *Handler) readEventError(ctx context.Context, flowID agent.FlowID, err error) (transportapi.ReadEventRes, error) {
	if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
		return nil, ctx.Err()
	}
	var pollTimeout *dex.LongPollTimeoutError
	if errors.As(err, &pollTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return &transportapi.PollTimeout{Reason: transportapi.PollTimeoutReasonTimeout}, nil
	}
	handler.logFailure(ctx, flowID, err)
	switch classifyFailure(err) {
	case failureNotFound:
		problem := newProblem(404, "Not Found", "the Agent Flow does not exist")
		return (*transportapi.ReadEventNotFound)(&problem), nil
	case failureConflict:
		problem := newProblem(410, "Gone", "the Agent Flow is no longer active")
		return (*transportapi.ReadEventGone)(&problem), nil
	default:
		problem := newProblem(503, "Service Unavailable", "the event Stream is unavailable")
		return (*transportapi.ReadEventServiceUnavailable)(&problem), nil
	}
}

func startProblem(problem transportapi.Problem) transportapi.StartAgentRes {
	return (*transportapi.StartAgentBadRequest)(&problem)
}

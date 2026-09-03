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
	return transportapi.AgentEvent{Kind: kind, Message: event.Message, CallId: callID, ToolName: toolName}, nil
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
	var rejected *agent.CommandRejectedError
	switch {
	case errors.As(err, &notFound):
		return failureNotFound
	case errors.As(err, &conflict), errors.As(err, &inactive), errors.As(err, &duplicate), errors.As(err, &rejected):
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
		return &transportapi.ReadEventGatewayTimeout{}, nil
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

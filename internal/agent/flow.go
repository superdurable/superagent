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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/superdurable/dex/sdk-go/dex"
)

var (
	agentConfigAttribute            = dex.DefineAttribute[AgentConfig]("AgentConfig")
	agentRuntimeMetadataAttribute   = dex.DefineAttribute[JSONObject]("AgentRuntimeMetadata")
	agentLeaseStateAttribute        = dex.DefineAttribute[JSONObject]("AgentLeaseState")
	agentLeaseScheduleAttribute     = dex.DefineAttribute[leaseSchedule]("AgentLeaseSchedule")
	agentStateAttribute             = dex.DefineAttribute[AgentState]("AgentState")
	agentInteractionStatusAttribute = dex.DefineAttribute[AgentInteractionStatus]("AgentInteractionStatus")
	contextSummaryAttribute         = dex.DefineAttribute[ContextSummary]("ContextSummary")
	currentMessagesAttribute        = dex.DefineAttributeMap[AgentMessage]("CurrentMessages")
	archivedMessagesAttribute       = dex.DefineAttributeMap[ArchivedMessageChunk]("ArchivedMessages")
	agentPlanAttribute              = dex.DefineAttribute[AgentPlan]("AgentPlan")
	pendingApprovalAttribute        = dex.DefineAttribute[PendingApproval]("PendingApproval")
	pendingTimerAttribute           = dex.DefineAttribute[PendingTimer]("PendingTimer")
	pendingUserInputAttribute       = dex.DefineAttribute[PendingUserInput]("PendingUserInput")
	queuedUserMessagesChannel       = dex.DefineChannel[UserMessage]("QueuedUserMessages")
	steeredUserMessagesChannel      = dex.DefineChannel[UserMessage]("SteeredUserMessages")
	toolApprovalsChannel            = dex.DefineChannelMap[ToolApproval]("ToolApprovals")
	planExecutionsChannel           = dex.DefineChannelMap[PlanExecutionRequest]("PlanExecutions")
	reasoningSummaryStream          = dex.DefineStream[string]("ReasoningSummary", 10<<20)
	assistantTextStream             = dex.DefineStream[string]("AssistantText", 10<<20)
	agentActivityStream             = dex.DefineStream[AgentEvent]("AgentActivity", 10<<20)
)

// Flow is the durable AI Agent state machine.
type Flow struct {
	modelClient ModelClient
	tools       ToolRegistry
	lease       *leaseExtension
}

var _ dex.Flow = (*Flow)(nil)

// NewFlow constructs an Agent from its model and trusted tool boundaries.
func NewFlow(modelClient ModelClient, tools ToolRegistry, options ...FlowOption) *Flow {
	if modelClient == nil {
		panic("model client is required")
	}
	if tools == nil {
		panic("tool registry is required")
	}
	flow := &Flow{modelClient: modelClient, tools: tools}
	for _, option := range options {
		if option == nil {
			panic("Flow option is required")
		}
		option.applyFlowOption(flow)
	}
	return flow
}

// FlowOption configures one optional Agent capability.
type FlowOption interface {
	applyFlowOption(*Flow)
}

type leaseFlowOption struct {
	refresher LeaseRefresher
	config    LeaseExtensionConfig
}

func (option leaseFlowOption) applyFlowOption(flow *Flow) {
	if flow.lease != nil {
		panic("Lease extension is already configured")
	}
	flow.lease = &leaseExtension{refresher: option.refresher, config: option.config}
}

// WithLeaseExtension enables durable maintenance of one opaque runtime Lease.
func WithLeaseExtension(refresher LeaseRefresher, config *LeaseExtensionConfig) FlowOption {
	if refresher == nil {
		panic("Lease refresher is required")
	}
	resolved := LeaseExtensionConfig{
		AttemptTimeout:     2 * time.Minute,
		RetryTotalDuration: 30 * time.Minute,
	}
	if config != nil {
		if config.AttemptTimeout != 0 {
			resolved.AttemptTimeout = config.AttemptTimeout
		}
		if config.RetryTotalDuration != 0 {
			resolved.RetryTotalDuration = config.RetryTotalDuration
		}
	}
	if err := resolved.validate(); err != nil {
		panic(fmt.Sprintf("invalid Lease extension config: %v", err))
	}
	return leaseFlowOption{refresher: refresher, config: resolved}
}

type leaseExtension struct {
	refresher LeaseRefresher
	config    LeaseExtensionConfig
}

type leaseSchedule struct {
	Generation int64     `json:"generation"`
	RefreshAt  time.Time `json:"refresh_at"`
}

const flowTypeAIAgent = "AIAgentFlow"

const leaseRecoveryPrompt = "If a tool reports that its runtime lease, credential, or token expired and a refresh may recover it, issue one new tool call so it can read the latest lease state. Do not automatically retry the same lease failure more than once; report the repeated failure to the user."

// GetFlowType pins the durable Flow identity.
func (*Flow) GetFlowType() string {
	return flowTypeAIAgent
}

// GetSteps registers the state-machine nodes.
func (flow *Flow) GetSteps() []dex.StepDef {
	return []dex.StepDef{
		dex.DefineStartStep(initStep{flow: flow}),
		dex.DefineStep(awaitUserStep{flow: flow}),
		dex.DefineStep(compactContextStep{flow: flow}),
		dex.DefineStep(callModelStep{flow: flow}),
		dex.DefineStep(checkSteeredStep{flow: flow}),
		dex.DefineStep(routeToolStep{flow: flow}),
		dex.DefineStep(awaitToolApprovalStep{flow: flow}),
		dex.DefineStep(executeToolStep{flow: flow}),
		dex.DefineStep(executeToolWithRetryStep{flow: flow}),
		dex.DefineStep(recoverToolExecutionStep{flow: flow}),
		dex.DefineStep(durableWaitStep{flow: flow}),
		dex.DefineStep(waitForLeaseRefreshStep{flow: flow}),
		dex.DefineStep(refreshLeaseStep{flow: flow}),
	}
}

// GetPersistenceSchema registers every durable value and best-effort stream.
func (*Flow) GetPersistenceSchema() dex.PersistenceSchema {
	return dex.PersistenceSchema{
		Attributes: []dex.AttributeDef{
			agentConfigAttribute,
			agentRuntimeMetadataAttribute,
			agentLeaseStateAttribute,
			agentLeaseScheduleAttribute,
			agentStateAttribute,
			agentInteractionStatusAttribute,
			contextSummaryAttribute,
			currentMessagesAttribute,
			archivedMessagesAttribute,
			agentPlanAttribute,
			pendingApprovalAttribute,
			pendingTimerAttribute,
			pendingUserInputAttribute,
		},
		Channels: []dex.ChannelDef{
			queuedUserMessagesChannel,
			steeredUserMessagesChannel,
			toolApprovalsChannel,
			planExecutionsChannel,
		},
		Streams: []dex.StreamDef{
			reasoningSummaryStream,
			assistantTextStream,
			agentActivityStream,
		},
	}
}

// SendMessage queues one non-empty user message when no question is pending.
func (*Flow) SendMessage(ctx dex.Context, input UserMessage) (*dex.RPCResult[bool], error) {
	if strings.TrimSpace(input.Content) == "" {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	pending, err := getPendingUserInput(ctx)
	if err != nil {
		return nil, err
	}
	if pending != nil {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if err := queuedUserMessagesChannel.Publish(ctx, input); err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	return &dex.RPCResult[bool]{Output: true}, nil
}

// AnswerQuestions closes and answers one exact pending input batch atomically.
func (*Flow) AnswerQuestions(ctx dex.Context, input AnswerQuestionsRequest) (*dex.RPCResult[bool], error) {
	if strings.TrimSpace(string(input.CallID)) == "" {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	pending, err := getPendingUserInput(ctx)
	if err != nil {
		return nil, err
	}
	if pending == nil || pending.CallID != input.CallID {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	message, isValid := acceptedAnsweredUserMessage(*pending, input.Answers)
	if !isValid {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if err := pendingUserInputAttribute.Delete(ctx); err != nil {
		return nil, err
	}
	if err := queuedUserMessagesChannel.Publish(ctx, message); err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	return &dex.RPCResult[bool]{Output: true}, nil
}

// SteerMessage atomically moves a queued message into the Steer queue.
func (*Flow) SteerMessage(ctx dex.Context, input SteerMessageRequest) (*dex.RPCResult[bool], error) {
	if strings.TrimSpace(string(input.MessageID)) == "" {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	message, found, err := queuedUserMessagesChannel.FindPendingMessage(ctx, string(input.MessageID))
	if err != nil {
		return nil, err
	}
	if !found {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if err := queuedUserMessagesChannel.Delete(ctx, string(input.MessageID)); err != nil {
		return nil, err
	}
	if err := steeredUserMessagesChannel.Publish(ctx, message.Value); err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	return &dex.RPCResult[bool]{Output: true}, nil
}

// GetSnapshot returns one atomic durable application view without consuming Channels.
func (flow *Flow) GetSnapshot(ctx dex.Context, _ dex.None) (*dex.RPCResult[AgentSnapshot], error) {
	queued, err := queuedUserMessagesChannel.PendingMessages(ctx)
	if err != nil {
		return nil, err
	}
	steered, err := steeredUserMessagesChannel.PendingMessages(ctx)
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		return &dex.RPCResult[AgentSnapshot]{Output: flow.initializingSnapshot(ctx, queued, steered)}, nil
	}
	if err != nil {
		return nil, err
	}
	config, err := agentConfigAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	start := max(state.FirstRetainedSequence, state.CurrentFirstSequence)
	messages := make([]SequencedMessage, 0, int(state.LastSequence-start+1))
	for sequence := start; sequence <= state.LastSequence; sequence++ {
		message, messageErr := currentMessagesAttribute.Get(ctx, sequenceKey(sequence))
		if messageErr != nil {
			return nil, messageErr
		}
		messages = append(messages, SequencedMessage{Sequence: sequence, Message: message})
	}
	history := HistoryPage{Messages: messages}
	if start > state.FirstRetainedSequence {
		next := start
		history.NextBeforeSequence = &next
	}
	description, err := flow.describe(ctx, config, state, len(queued), len(steered))
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[AgentSnapshot]{Output: AgentSnapshot{
		RunID:       RunID(ctx.RunID()),
		FlowStatus:  FlowStatusRunning,
		History:     history,
		Description: &description,
		Queued:      pendingUserMessages(queued),
		Steered:     pendingUserMessages(steered),
	}}, nil
}

// GetArchivedMessages returns one retained immutable history chunk.
func (*Flow) GetArchivedMessages(
	ctx dex.Context,
	before Sequence,
) (*dex.RPCResult[archivedMessagesRPCOutput], error) {
	first, isValid := archivedMessageChunkFirst(before)
	if !isValid {
		return &dex.RPCResult[archivedMessagesRPCOutput]{}, nil
	}
	state, err := agentStateAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		return &dex.RPCResult[archivedMessagesRPCOutput]{}, nil
	}
	if err != nil {
		return nil, err
	}
	if first < state.FirstRetainedSequence {
		return &dex.RPCResult[archivedMessagesRPCOutput]{}, nil
	}
	chunk, err := archivedMessagesAttribute.Get(ctx, sequenceKey(first))
	if isAttributeNotFound(err) {
		return &dex.RPCResult[archivedMessagesRPCOutput]{}, nil
	}
	if err != nil {
		return nil, err
	}
	page := HistoryPage{Messages: chunk.Messages}
	if first > state.FirstRetainedSequence {
		next := first
		page.NextBeforeSequence = &next
	}
	return &dex.RPCResult[archivedMessagesRPCOutput]{Output: archivedMessagesRPCOutput{
		Page:  page,
		Found: true,
	}}, nil
}

// DeleteQueuedMessage removes one exact pending user message.
func (*Flow) DeleteQueuedMessage(ctx dex.Context, messageID MessageID) (*dex.RPCResult[bool], error) {
	if strings.TrimSpace(string(messageID)) == "" {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	_, found, err := queuedUserMessagesChannel.FindPendingMessage(ctx, string(messageID))
	if err != nil {
		return nil, err
	}
	if !found {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if err := queuedUserMessagesChannel.Delete(ctx, string(messageID)); err != nil {
		return nil, err
	}
	return &dex.RPCResult[bool]{Output: true}, nil
}

// ApproveTool publishes an approval only for the current exact call ID.
func (*Flow) ApproveTool(ctx dex.Context, input ToolApprovalRequest) (*dex.RPCResult[bool], error) {
	pending, err := getPendingApproval(ctx)
	if err != nil {
		return nil, err
	}
	if pending == nil || pending.CallID != input.CallID {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if err := pendingApprovalAttribute.Delete(ctx); err != nil {
		return nil, err
	}
	if err := toolApprovalsChannel.Publish(ctx, string(input.CallID), ToolApproval{Approved: input.Approved}); err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	return &dex.RPCResult[bool]{Output: true}, nil
}

// ExecutePlan schedules an exact waiting draft or active plan revision.
func (*Flow) ExecutePlan(ctx dex.Context, input PlanExecutionRequest) (*dex.RPCResult[bool], error) {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := getAgentPlan(ctx)
	if err != nil {
		return nil, err
	}
	pendingInput, err := getPendingUserInput(ctx)
	if err != nil {
		return nil, err
	}
	pendingApproval, err := getPendingApproval(ctx)
	if err != nil {
		return nil, err
	}
	pendingTimer, err := getPendingTimer(ctx)
	if err != nil {
		return nil, err
	}
	canExecute := plan != nil &&
		state.Status == AgentStatusWaitingForMessage &&
		state.PendingPlanExecutionRevision == nil &&
		pendingInput == nil &&
		pendingApproval == nil &&
		pendingTimer == nil &&
		queuedUserMessagesChannel.Size(ctx) == 0 &&
		steeredUserMessagesChannel.Size(ctx) == 0 &&
		plan.Revision == input.Revision &&
		(plan.Status == PlanStatusDraft || plan.Status == PlanStatusActive)
	if !canExecute {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	revision := plan.Revision
	state.PendingPlanExecutionRevision = &revision
	state.PlanNoProgressAttempts = 0
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return nil, err
	}
	if err := planExecutionsChannel.Publish(ctx, fmt.Sprint(plan.Revision), input); err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	return &dex.RPCResult[bool]{Output: true}, nil
}

func (flow *Flow) describe(
	ctx dex.Context,
	config AgentConfig,
	state AgentState,
	queuedMessageCount int,
	steeredMessageCount int,
) (AgentDescription, error) {
	pendingApproval, err := getPendingApproval(ctx)
	if err != nil {
		return AgentDescription{}, err
	}
	pendingTimer, err := getPendingTimer(ctx)
	if err != nil {
		return AgentDescription{}, err
	}
	pendingUserInput, err := getPendingUserInput(ctx)
	if err != nil {
		return AgentDescription{}, err
	}
	plan, err := getAgentPlan(ctx)
	if err != nil {
		return AgentDescription{}, err
	}
	interactionStatus, err := agentInteractionStatusAttribute.Get(ctx)
	if err != nil {
		return AgentDescription{}, err
	}
	definitions := flow.toolDefinitions(config)
	availableTools := make([]ToolName, 0, len(definitions)+1)
	availableTools = append(availableTools, ToolNameWriteTodos)
	for _, definition := range definitions {
		availableTools = append(availableTools, definition.Name)
	}
	return AgentDescription{
		Status:                     state.Status,
		InteractionStatus:          interactionStatus,
		Model:                      config.Model,
		SystemPrompt:               config.SystemPrompt,
		FirstRetainedSequence:      state.FirstRetainedSequence,
		LastSequence:               state.LastSequence,
		SummarizedThroughSequence:  state.SummarizedThroughSequence,
		PendingApproval:            pendingApproval,
		PendingTimer:               pendingTimer,
		PendingUserInput:           pendingUserInput,
		Plan:                       plan,
		IsPlanExecutionRequested:   state.PendingPlanExecutionRevision != nil,
		PendingQueuedMessageCount:  queuedMessageCount,
		PendingSteeredMessageCount: steeredMessageCount,
		AvailableMCPServers:        flow.tools.ServerNames(),
		AvailableTools:             availableTools,
	}, nil
}

func (flow *Flow) initializingSnapshot(
	ctx dex.Context,
	queued []dex.ChannelMessage[UserMessage],
	steered []dex.ChannelMessage[UserMessage],
) AgentSnapshot {
	return AgentSnapshot{
		RunID:      RunID(ctx.RunID()),
		FlowStatus: FlowStatusRunning,
		History:    HistoryPage{Messages: []SequencedMessage{}},
		Description: &AgentDescription{
			Status:                     AgentStatusInitializing,
			InteractionStatus:          AgentInteractionStatusSubmitted,
			FirstRetainedSequence:      1,
			PendingQueuedMessageCount:  len(queued),
			PendingSteeredMessageCount: len(steered),
			AvailableMCPServers:        flow.tools.ServerNames(),
			AvailableTools: []ToolName{
				ToolNameWriteTodos,
				ToolNameDurableWait,
				ToolNameRequestUserInput,
			},
		},
		Queued:  pendingUserMessages(queued),
		Steered: pendingUserMessages(steered),
	}
}

func pendingUserMessages(messages []dex.ChannelMessage[UserMessage]) []PendingUserMessage {
	result := make([]PendingUserMessage, 0, len(messages))
	for _, message := range messages {
		result = append(result, PendingUserMessage{
			MessageID: MessageID(message.MessageID),
			Value:     message.Value,
		})
	}
	return result
}

func (flow *Flow) validateConfig(config AgentConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if !config.MCPEnabled && (len(config.EnabledMCPServers) > 0 || len(config.EnabledTools) > 0) {
		return errors.New("disabled MCP cannot select servers or tools")
	}
	unknownServers := difference(config.EnabledMCPServers, flow.tools.ServerNames())
	if len(unknownServers) > 0 {
		return fmt.Errorf("unknown MCP servers: %v", unknownServers)
	}
	availableTools := make([]ToolName, 0)
	for _, definition := range flow.toolDefinitions(config) {
		if err := validateToolExecutionPolicy(definition); err != nil {
			return fmt.Errorf("tool %q: %w", definition.Name, err)
		}
		availableTools = append(availableTools, definition.Name)
	}
	unknownTools := difference(config.EnabledTools, availableTools)
	if len(unknownTools) > 0 {
		return fmt.Errorf("unknown tools: %v", unknownTools)
	}
	return nil
}

func validateToolExecutionPolicy(definition ToolDefinition) error {
	switch {
	case definition.MaximumAttempts <= 0:
		return errors.New("maximum attempts must be positive")
	case definition.MaximumAttempts > math.MaxInt32:
		return errors.New("maximum attempts exceeds the Dex limit")
	case definition.AttemptTimeout < 0:
		return errors.New("attempt timeout must not be negative")
	case definition.RetryTotalDuration < 0:
		return errors.New("retry total duration must not be negative")
	default:
		return nil
	}
}

func (flow *Flow) validateInitialLease(initial *LeaseInitialization, now time.Time) error {
	if flow.lease != nil {
		return validateLeaseInitialization(initial, now)
	}
	if initial != nil {
		return errors.New("initial Lease requires the Lease extension")
	}
	return nil
}

func (flow *Flow) currentToolStepOptions(ctx dex.Context) (*dex.StepOptions, error) {
	call, err := flow.currentToolCall(ctx)
	if err != nil {
		return nil, err
	}
	config, err := agentConfigAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	definition, err := flow.invocationToolDefinition(config, state, call.Name)
	if err != nil {
		return nil, err
	}
	if err := validateToolExecutionPolicy(definition); err != nil {
		return nil, fmt.Errorf("tool %q: %w", definition.Name, err)
	}
	return flow.toolStepOptions(definition), nil
}

func (flow *Flow) toolStepOptions(definition ToolDefinition) *dex.StepOptions {
	return &dex.StepOptions{
		ExecuteMethodTimeout:     definition.AttemptTimeout,
		HeartbeatTimeout:         toolStepOptions.HeartbeatTimeout,
		ExecuteLoadAttributeMaps: toolStepOptions.ExecuteLoadAttributeMaps,
		ExecuteRetry: &dex.RetryPolicy{
			// #nosec G115 -- validateToolExecutionPolicy rejects values outside int32.
			MaximumAttempts: int32(definition.MaximumAttempts),
			TotalDuration:   definition.RetryTotalDuration,
		},
		ExecuteFailure: dex.ProceedToOnExecuteFailure(
			recoverToolExecutionStep{flow: flow},
			messageMutationStepOptions,
		),
	}
}

func (flow *Flow) toolDefinitions(config AgentConfig) []ToolDefinition {
	definitions := []ToolDefinition{}
	if config.MCPEnabled {
		definitions = append(definitions, flow.tools.Definitions(config.EnabledMCPServers, config.EnabledTools)...)
	}
	definitions = append(definitions, durableWaitDefinition(), requestUserInputDefinition())
	return definitions
}

func (flow *Flow) invocationToolDefinitions(config AgentConfig, state AgentState) []ToolDefinition {
	switch state.InteractionMode {
	case InteractionModePlanning:
		if state.PlanningRequiresWrite || state.PlanningAllowsWrite {
			return []ToolDefinition{writeTodosDefinition()}
		}
		return []ToolDefinition{}
	case InteractionModeExecuting:
		return append([]ToolDefinition{writeTodosDefinition()}, flow.toolDefinitions(config)...)
	default:
		return flow.toolDefinitions(config)
	}
}

func (flow *Flow) invocationToolDefinition(config AgentConfig, state AgentState, name ToolName) (ToolDefinition, error) {
	for _, definition := range flow.invocationToolDefinitions(config, state) {
		if definition.Name == name {
			return definition, nil
		}
	}
	return ToolDefinition{}, fmt.Errorf("unknown or disabled tool %q", name)
}

func (flow *Flow) beginUserTurn(ctx dex.Context, message UserMessage) error {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return err
	}
	plan, err := getAgentPlan(ctx)
	if err != nil {
		return err
	}
	switch {
	case message.PlanMode:
		state.InteractionMode = InteractionModePlanning
		state.PlanningRequiresWrite = true
		state.PlanningAllowsWrite = true
	case message.AnsweredInputCallID != nil && plan != nil && plan.Status == PlanStatusActive:
		state.InteractionMode = InteractionModeExecuting
		state.PlanningRequiresWrite = false
		state.PlanningAllowsWrite = false
	case plan != nil && (plan.Status == PlanStatusDraft || plan.Status == PlanStatusActive):
		state.InteractionMode = InteractionModePlanning
		state.PlanningRequiresWrite = false
		state.PlanningAllowsWrite = true
	default:
		state.InteractionMode = InteractionModeChat
		state.PlanningRequiresWrite = false
		state.PlanningAllowsWrite = false
	}
	state.Status = AgentStatusCallingModel
	state.PendingToolCalls = []ToolCall{}
	state.PendingToolIndex = 0
	state.PendingPlanExecutionRevision = nil
	state.PlanNoProgressAttempts = 0
	if setErr := agentStateAttribute.Set(ctx, state); setErr != nil {
		return setErr
	}
	_, err = flow.appendMessage(ctx, AgentMessage{
		Role:                 MessageRoleUser,
		Content:              message.Content,
		ToolCalls:            []ToolCall{},
		ProviderContextItems: []ProviderContextItem{},
	})
	return err
}

func (flow *Flow) beginSteeredTurn(ctx dex.Context, messages []UserMessage) error {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return err
	}
	pendingCalls := append([]ToolCall(nil), state.PendingToolCalls[state.PendingToolIndex:]...)
	state.Status = AgentStatusApplyingSteering
	state.PendingToolCalls = []ToolCall{}
	state.PendingToolIndex = 0
	state.PendingPlanExecutionRevision = nil
	state.PlanNoProgressAttempts = 0
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return err
	}
	if err := deletePendingApproval(ctx); err != nil {
		return err
	}
	if err := deletePendingTimer(ctx); err != nil {
		return err
	}
	if err := deletePendingUserInput(ctx); err != nil {
		return err
	}
	for _, call := range pendingCalls {
		result, err := encodeToolResult(toolResultPayload{
			Status: toolResultStatusInterrupted,
			Error:  toolErrorSupersededBySteering,
		}, ToolOutcomeKnownFailure, true)
		if err != nil {
			return err
		}
		if err := flow.appendToolResult(ctx, call, result); err != nil {
			return err
		}
	}
	for _, message := range messages {
		if err := flow.beginUserTurn(ctx, message); err != nil {
			return err
		}
	}
	return flow.writeActivity(ctx, AgentEvent{
		Kind:    EventKindSteeringApplied,
		Message: fmt.Sprintf("Applied %d steered user message(s).", len(messages)),
	})
}

func (flow *Flow) getPlan(ctx dex.Context) (*AgentPlan, error) {
	return getAgentPlan(ctx)
}

func (flow *Flow) replacePlan(ctx dex.Context, tasks []PlanTask) (PlanRevision, error) {
	previousPlan, err := flow.getPlan(ctx)
	if err != nil {
		return 0, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return 0, err
	}
	revision := state.PlanRevision + 1
	var status PlanStatus
	if len(tasks) == 0 {
		if err := deleteAgentPlan(ctx); err != nil {
			return 0, err
		}
	} else {
		status = PlanStatusActive
		if state.InteractionMode == InteractionModePlanning {
			status = PlanStatusDraft
		} else if allTasksCompleted(tasks) {
			status = PlanStatusCompleted
		}
		if err := agentPlanAttribute.Set(ctx, AgentPlan{
			Revision: revision,
			Status:   status,
			Tasks:    tasks,
		}); err != nil {
			return 0, err
		}
	}
	state.PlanRevision = revision
	state.PlanningRequiresWrite = false
	state.PlanningAllowsWrite = false
	state.PendingPlanExecutionRevision = nil
	state.PlanNoProgressAttempts = 0
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return 0, err
	}
	message := fmt.Sprintf("Updated %s plan revision %d.", status, revision)
	if len(tasks) == 0 {
		message = fmt.Sprintf("Cleared plan at revision %d.", revision)
	}
	if err := flow.writeActivity(ctx, AgentEvent{Kind: EventKindPlanUpdated, Message: message}); err != nil {
		return 0, err
	}
	if err := flow.writePlanTaskActivities(ctx, previousPlan, revision, tasks); err != nil {
		return 0, err
	}
	return revision, nil
}

func (flow *Flow) writePlanTaskActivities(
	ctx dex.Context,
	previousPlan *AgentPlan,
	revision PlanRevision,
	tasks []PlanTask,
) error {
	for _, event := range planTaskActivities(previousPlan, revision, tasks) {
		if err := flow.writeActivity(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func planTaskActivities(
	previousPlan *AgentPlan,
	revision PlanRevision,
	tasks []PlanTask,
) []AgentEvent {
	if previousPlan == nil {
		return nil
	}
	result := []AgentEvent{}
	for index, task := range tasks {
		if index >= len(previousPlan.Tasks) {
			break
		}
		previousTask := previousPlan.Tasks[index]
		if previousTask.Content != task.Content || previousTask.Status == task.Status {
			continue
		}
		baseRevision := previousPlan.Revision
		taskIndex := PlanTaskIndex(index)
		taskStatus := task.Status
		result = append(result, AgentEvent{
			Kind:             EventKindPlanTaskUpdated,
			Message:          planTaskActivityMessage(taskIndex, taskStatus),
			PlanBaseRevision: &baseRevision,
			PlanRevision:     &revision,
			PlanTaskIndex:    &taskIndex,
			PlanTaskStatus:   &taskStatus,
		})
	}
	return result
}

func planTaskActivityMessage(index PlanTaskIndex, status TaskStatus) string {
	position := int(index) + 1
	switch status {
	case TaskStatusInProgress:
		return fmt.Sprintf("Started plan task %d.", position)
	case TaskStatusCompleted:
		return fmt.Sprintf("Completed plan task %d.", position)
	case TaskStatusPending:
		return fmt.Sprintf("Reset plan task %d to pending.", position)
	default:
		return fmt.Sprintf("Updated plan task %d.", position)
	}
}

func (flow *Flow) appendMessage(ctx dex.Context, message AgentMessage) (Sequence, error) {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return 0, err
	}
	sequence := state.NextSequence
	message.CreatedAt = time.Now().UTC()
	if message.ToolCalls == nil {
		message.ToolCalls = []ToolCall{}
	}
	if message.ProviderContextItems == nil {
		message.ProviderContextItems = []ProviderContextItem{}
	}
	if err := currentMessagesAttribute.Set(ctx, sequenceKey(sequence), message); err != nil {
		return 0, err
	}
	state.NextSequence = sequence + 1
	state.LastSequence = sequence
	if err := flow.archiveCurrentMessages(ctx, &state); err != nil {
		return 0, err
	}
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return 0, err
	}
	return sequence, nil
}

func (*Flow) archiveCurrentMessages(ctx dex.Context, state *AgentState) error {
	if state.LastSequence-state.CurrentFirstSequence+1 < Sequence(currentMessageLimit) {
		return nil
	}
	first := state.CurrentFirstSequence
	messages := make([]SequencedMessage, 0, archiveMessageChunkSize)
	for sequence := first; sequence < first+Sequence(archiveMessageChunkSize); sequence++ {
		message, err := currentMessagesAttribute.Get(ctx, sequenceKey(sequence))
		if err != nil {
			return err
		}
		messages = append(messages, SequencedMessage{Sequence: sequence, Message: message})
	}
	if err := archivedMessagesAttribute.Set(ctx, sequenceKey(first), ArchivedMessageChunk{Messages: messages}); err != nil {
		return err
	}
	for sequence := first; sequence < first+Sequence(archiveMessageChunkSize); sequence++ {
		if err := currentMessagesAttribute.Delete(ctx, sequenceKey(sequence)); err != nil {
			return err
		}
	}
	state.CurrentFirstSequence += Sequence(archiveMessageChunkSize)
	return nil
}

func (flow *Flow) appendToolResult(ctx dex.Context, call ToolCall, result ToolExecutionResult) error {
	callID := call.ID
	toolName := call.Name
	_, err := flow.appendMessage(ctx, AgentMessage{
		Role:                 MessageRoleTool,
		Content:              result.Content,
		ToolCalls:            []ToolCall{},
		ToolCallID:           &callID,
		ToolName:             &toolName,
		ProviderContextItems: []ProviderContextItem{},
	})
	return err
}

func (flow *Flow) appendToolResultAndCancelRemaining(
	ctx dex.Context,
	call ToolCall,
	result ToolExecutionResult,
	cancellationReason toolErrorCode,
) error {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return err
	}
	remaining := append([]ToolCall(nil), state.PendingToolCalls[state.PendingToolIndex+1:]...)
	state.PendingToolCalls = []ToolCall{}
	state.PendingToolIndex = 0
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return err
	}
	if err := flow.appendToolResult(ctx, call, result); err != nil {
		return err
	}
	for _, remainingCall := range remaining {
		cancellation, encodeErr := encodeToolResult(toolResultPayload{
			Status: toolResultStatusInterrupted,
			Error:  cancellationReason,
		}, ToolOutcomeKnownFailure, true)
		if encodeErr != nil {
			return encodeErr
		}
		if err := flow.appendToolResult(ctx, remainingCall, cancellation); err != nil {
			return err
		}
	}
	return nil
}

func (*Flow) hasNextToolCall(ctx dex.Context) (bool, error) {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return false, err
	}
	return state.PendingToolIndex+1 < len(state.PendingToolCalls), nil
}

func (*Flow) advanceTool(ctx dex.Context) error {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return err
	}
	state.PendingToolIndex++
	return agentStateAttribute.Set(ctx, state)
}

func (*Flow) clearPendingToolCalls(ctx dex.Context) error {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return err
	}
	state.PendingToolCalls = []ToolCall{}
	state.PendingToolIndex = 0
	return agentStateAttribute.Set(ctx, state)
}

func (*Flow) currentToolCall(ctx dex.Context) (ToolCall, error) {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return ToolCall{}, err
	}
	if state.PendingToolIndex < 0 || state.PendingToolIndex >= len(state.PendingToolCalls) {
		return ToolCall{}, errors.New("the Agent has no pending tool call")
	}
	return state.PendingToolCalls[state.PendingToolIndex], nil
}

func (*Flow) updateStatus(ctx dex.Context, status AgentStatus) error {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return err
	}
	if state.Status == status {
		return nil
	}
	state.Status = status
	return agentStateAttribute.Set(ctx, state)
}

func (*Flow) getSummary(ctx dex.Context) (ContextSummary, error) {
	summary, err := getContextSummary(ctx)
	if err != nil {
		return ContextSummary{}, err
	}
	if summary == nil {
		return ContextSummary{}, nil
	}
	return *summary, nil
}

func (flow *Flow) contextMessages(ctx dex.Context, config AgentConfig, state AgentState) ([]AgentMessage, error) {
	result := []AgentMessage{}
	if state.InteractionMode != InteractionModePlanning {
		result = append(result, AgentMessage{
			Role:    MessageRoleSystem,
			Content: "When you need user input, call request_user_input instead of asking only in assistant text. Ask 1-3 related questions in one batch. If no reply is required, finish without a follow-up question.",
		})
	}
	summary, err := flow.getSummary(ctx)
	if err != nil {
		return nil, err
	}
	if summary.Content != "" {
		result = append(result, AgentMessage{
			Role:    MessageRoleSystem,
			Content: fmt.Sprintf("Conversation summary through message %d:\n%s", summary.SummarizedThroughSequence, summary.Content),
		})
	}
	start := max(state.FirstRetainedSequence, state.SummarizedThroughSequence+1)
	messages, err := flow.loadMessages(ctx, start, state.LastSequence, config)
	if err != nil {
		return nil, err
	}
	result = append(result, messages...)
	planMessage, err := flow.planContextMessage(ctx, state)
	if err != nil {
		return nil, err
	}
	if planMessage != nil {
		result = append(result, *planMessage)
	}
	return result, nil
}

func (flow *Flow) planContextMessage(ctx dex.Context, state AgentState) (*AgentMessage, error) {
	plan, err := flow.getPlan(ctx)
	if err != nil {
		return nil, err
	}
	if plan == nil && state.InteractionMode != InteractionModePlanning {
		return nil, nil
	}
	planJSON := "null"
	if plan != nil {
		encoded, err := json.Marshal(plan)
		if err != nil {
			return nil, err
		}
		planJSON = string(encoded)
	}
	instruction := "This completed plan is durable reference state."
	if state.InteractionMode == InteractionModePlanning {
		instruction = "This is a planning-only turn. Do not execute business tools or claim that planned work was performed."
	} else if plan != nil && plan.Status == PlanStatusActive {
		instruction = "The user approved this plan. Execute it and use write_todos to keep task statuses accurate. If required information is missing, keep dependent tasks pending, call request_user_input with 1-3 related questions, and stop until the user answers."
		if state.PlanNoProgressAttempts > 0 {
			instruction += " The previous response left this active plan unfinished without requesting a tool. Continue the work now, call request_user_input for missing information, or use write_todos to record the accurate final state. Do not ask for required input only in assistant text."
		}
	}
	return &AgentMessage{
		Role:    MessageRoleSystem,
		Content: "Current durable plan: " + planJSON + "\n" + instruction,
	}, nil
}

func (flow *Flow) loadMessages(ctx dex.Context, start Sequence, end Sequence, config AgentConfig) ([]AgentMessage, error) {
	if end < start {
		return []AgentMessage{}, nil
	}
	result := make([]AgentMessage, 0, int(end-start+1))
	for sequence := start; sequence <= end; sequence++ {
		message, err := flow.messageAt(ctx, sequence)
		if err != nil {
			return nil, err
		}
		result = append(result, projectMessage(message, config.MaxContextTokens))
	}
	return result, nil
}

func (*Flow) messageAt(ctx dex.Context, sequence Sequence) (AgentMessage, error) {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return AgentMessage{}, err
	}
	if sequence >= state.CurrentFirstSequence {
		return currentMessagesAttribute.Get(ctx, sequenceKey(sequence))
	}
	first := ((sequence - 1) / Sequence(archiveMessageChunkSize) * Sequence(archiveMessageChunkSize)) + 1
	chunk, err := archivedMessagesAttribute.Get(ctx, sequenceKey(first))
	if err != nil {
		return AgentMessage{}, err
	}
	index := sequence - first
	if index < 0 || index >= Sequence(len(chunk.Messages)) || chunk.Messages[index].Sequence != sequence {
		return AgentMessage{}, fmt.Errorf("archived message %d is missing from chunk %d", sequence, first)
	}
	return chunk.Messages[index].Message, nil
}

func (flow *Flow) compactionCutoff(ctx dex.Context, config AgentConfig, state AgentState) (Sequence, error) {
	start := max(state.FirstRetainedSequence, state.SummarizedThroughSequence+1)
	if start >= state.LastSequence {
		return state.SummarizedThroughSequence, nil
	}
	keepTokens := max(1, int(float64(config.MaxContextTokens)*config.CompactionKeepFraction))
	retainedTokens := 0
	cutoff := state.LastSequence - 1
	for sequence := state.LastSequence; sequence >= start; sequence-- {
		message, err := flow.messageAt(ctx, sequence)
		if err != nil {
			return 0, err
		}
		message = projectMessage(message, config.MaxContextTokens)
		retainedTokens += flow.modelClient.CountTokens(config.Model, []AgentMessage{message})
		if retainedTokens > keepTokens {
			cutoff = sequence
			break
		}
		cutoff = sequence - 1
	}
	cutoff = max(state.SummarizedThroughSequence, cutoff)
	messages, err := flow.loadMessages(ctx, start, cutoff, config)
	if err != nil {
		return 0, err
	}
	return toolSafeCompactionCutoff(messages, start, cutoff), nil
}

func (flow *Flow) pendingCompactionCutoff(ctx dex.Context) (*Sequence, error) {
	config, err := agentConfigAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err = flow.trimSummarizedMessages(ctx, config, state)
	if err != nil {
		return nil, err
	}
	messages, err := flow.contextMessages(ctx, config, state)
	if err != nil {
		return nil, err
	}
	messages = append([]AgentMessage{{Role: MessageRoleSystem, Content: config.SystemPrompt}}, messages...)
	tokenCount := flow.modelClient.CountTokens(config.Model, messages)
	hasRetentionPressure := state.LastSequence-state.FirstRetainedSequence+1 > Sequence(config.MessageRetentionLimit)
	if tokenCount < int(float64(config.MaxContextTokens)*config.CompactionTriggerFraction) && !hasRetentionPressure {
		return nil, nil
	}
	cutoff, err := flow.compactionCutoff(ctx, config, state)
	if err != nil {
		return nil, err
	}
	if cutoff <= state.SummarizedThroughSequence {
		return nil, nil
	}
	return &cutoff, nil
}

func (*Flow) trimSummarizedMessages(ctx dex.Context, config AgentConfig, state AgentState) (AgentState, error) {
	retained := max(Sequence(0), state.LastSequence-state.FirstRetainedSequence+1)
	first := state.FirstRetainedSequence
	for retained > Sequence(config.MessageRetentionLimit) &&
		first < state.CurrentFirstSequence &&
		first+Sequence(archiveMessageChunkSize)-1 <= state.SummarizedThroughSequence {
		if err := archivedMessagesAttribute.Delete(ctx, sequenceKey(first)); err != nil {
			return AgentState{}, err
		}
		first += Sequence(archiveMessageChunkSize)
		retained -= Sequence(archiveMessageChunkSize)
	}
	if first != state.FirstRetainedSequence {
		state.FirstRetainedSequence = first
		if err := agentStateAttribute.Set(ctx, state); err != nil {
			return AgentState{}, err
		}
	}
	return state, nil
}

func (flow *Flow) continueAfterTool(ctx dex.Context) (*dex.StepDecision, error) {
	hasNext, err := flow.hasNextToolCall(ctx)
	if err != nil {
		return nil, err
	}
	if hasNext {
		if err := flow.advanceTool(ctx); err != nil {
			return nil, err
		}
		return dex.GoTo(checkSteeredStep{flow: flow}, continueRouteTool), nil
	}
	if err := flow.clearPendingToolCalls(ctx); err != nil {
		return nil, err
	}
	return dex.GoTo(checkSteeredStep{flow: flow}, continueCompactContext), nil
}

func (*Flow) writeActivity(ctx dex.Context, event AgentEvent) error {
	event.Message = condenseActivityMessage(event.Message)
	return agentActivityStream.Write(ctx, event)
}

func condenseActivityMessage(message string) string {
	const maximumRunes = 200
	condensed := strings.Join(strings.Fields(message), " ")
	runes := []rune(condensed)
	if len(runes) <= maximumRunes {
		return condensed
	}
	return string(runes[:maximumRunes-1]) + "…"
}

func getAgentPlan(ctx dex.Context) (*AgentPlan, error) {
	value, err := agentPlanAttribute.Get(ctx)
	return optionalAgentPlan(value, err)
}

func getPendingApproval(ctx dex.Context) (*PendingApproval, error) {
	value, err := pendingApprovalAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func getPendingTimer(ctx dex.Context) (*PendingTimer, error) {
	value, err := pendingTimerAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func getPendingUserInput(ctx dex.Context) (*PendingUserInput, error) {
	value, err := pendingUserInputAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func getContextSummary(ctx dex.Context) (*ContextSummary, error) {
	value, err := contextSummaryAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func optionalAgentPlan(value AgentPlan, err error) (*AgentPlan, error) {
	if isAttributeNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func deleteAgentPlan(ctx dex.Context) error {
	plan, err := getAgentPlan(ctx)
	if err != nil || plan == nil {
		return err
	}
	return agentPlanAttribute.Delete(ctx)
}

func deletePendingApproval(ctx dex.Context) error {
	value, err := getPendingApproval(ctx)
	if err != nil || value == nil {
		return err
	}
	return pendingApprovalAttribute.Delete(ctx)
}

func deletePendingTimer(ctx dex.Context) error {
	value, err := getPendingTimer(ctx)
	if err != nil || value == nil {
		return err
	}
	return pendingTimerAttribute.Delete(ctx)
}

func deletePendingUserInput(ctx dex.Context) error {
	value, err := getPendingUserInput(ctx)
	if err != nil || value == nil {
		return err
	}
	return pendingUserInputAttribute.Delete(ctx)
}

func isAttributeNotFound(err error) bool {
	var notFound *dex.AttributeNotFoundError
	return errors.As(err, &notFound)
}

func difference[T ~string](values []T, allowed []T) []T {
	result := make([]T, 0)
	for _, value := range values {
		if !slices.Contains(allowed, value) && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return result
}

func allTasksCompleted(tasks []PlanTask) bool {
	for _, task := range tasks {
		if task.Status != TaskStatusCompleted {
			return false
		}
	}
	return true
}

const (
	continueAwaitUser         continuation = "await_user"
	continueCallModel         continuation = "call_model"
	continueCompactContext    continuation = "compact_context"
	continueRouteTool         continuation = "route_tool"
	continueAwaitToolApproval continuation = "await_tool_approval"
	continueExecuteTool       continuation = "execute_tool"
	continueExecuteToolRetry  continuation = "execute_tool_with_retry"
	continueDurableWait       continuation = "durable_wait"

	stepTypeInit           stepType = "Init"
	stepTypeAwaitUser      stepType = "AwaitUser"
	stepTypeCompactContext stepType = "CompactContext"
	stepTypeCallModel      stepType = "CallModel"
	stepTypeCheckSteered   stepType = "CheckSteered"
	stepTypeRouteTool      stepType = "RouteTool"
	stepTypeAwaitApproval  stepType = "AwaitToolApproval"
	stepTypeExecuteTool    stepType = "ExecuteTool"
	stepTypeExecuteRetry   stepType = "ExecuteToolWithRetry"
	stepTypeRecoverTool    stepType = "RecoverToolExecution"
	stepTypeDurableWait    stepType = "DurableWait"
	stepTypeWaitLease      stepType = "WaitForLeaseRefresh"
	stepTypeRefreshLease   stepType = "RefreshLease"

	maximumSteeringMessageCount       = 2_147_483_647
	maximumAutomaticPlanRecoveryCount = 1
)

type continuation string
type stepType string

type heartbeatPhase string

const (
	heartbeatPhaseCompacting      heartbeatPhase = "compacting"
	heartbeatPhaseAssistantStream heartbeatPhase = "assistant_stream"
	heartbeatPhaseReasoningStream heartbeatPhase = "reasoning_stream"
	heartbeatPhaseActivityStream  heartbeatPhase = "activity_stream"
	heartbeatPhaseToolProgress    heartbeatPhase = "tool_progress"
)

type compactionHeartbeat struct {
	Phase           heartbeatPhase `json:"phase"`
	ThroughSequence Sequence       `json:"through_sequence"`
}

type modelHeartbeat struct {
	Phase     heartbeatPhase `json:"phase"`
	EventKind *EventKind     `json:"event_kind,omitempty"`
}

type toolHeartbeat struct {
	Phase    heartbeatPhase `json:"phase"`
	ToolName ToolName       `json:"tool_name"`
}

var (
	messageMutationStepOptions = &dex.StepOptions{
		ExecuteLoadAttributeMaps: []dex.AttributeDef{currentMessagesAttribute},
		ExecuteLockAttributes: []dex.AttributeLock{
			dex.LockAttribute(pendingUserInputAttribute),
			dex.LockAttribute(pendingApprovalAttribute),
		},
	}
	messageContextStepOptions = &dex.StepOptions{
		ExecuteLoadAttributeMaps: []dex.AttributeDef{
			currentMessagesAttribute,
			archivedMessagesAttribute,
		},
		ExecuteLockAttributes: []dex.AttributeLock{dex.LockAttribute(pendingUserInputAttribute)},
	}
	modelStepOptions = &dex.StepOptions{
		ExecuteMethodTimeout:     10 * time.Minute,
		HeartbeatTimeout:         5 * time.Minute,
		ExecuteLoadAttributeMaps: messageContextStepOptions.ExecuteLoadAttributeMaps,
		ExecuteRetry: &dex.RetryPolicy{
			MaximumAttempts: 3,
			TotalDuration:   30 * time.Minute,
		},
	}
	toolStepOptions = &dex.StepOptions{
		ExecuteMethodTimeout:     2 * time.Hour,
		HeartbeatTimeout:         5 * time.Minute,
		ExecuteLoadAttributeMaps: messageMutationStepOptions.ExecuteLoadAttributeMaps,
		ExecuteRetry: &dex.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
)

type initStep struct {
	dex.StepDefaultsNoWaitFor[AgentConfig]
	flow *Flow
}

var _ dex.Step[AgentConfig] = initStep{}

func (initStep) GetStepType() string { return string(stepTypeInit) }

func (step initStep) Execute(ctx dex.Context, input AgentConfig) (*dex.StepDecision, error) {
	if err := step.flow.validateConfig(input); err != nil {
		return nil, err
	}
	if err := agentConfigAttribute.Set(ctx, input); err != nil {
		return nil, err
	}
	if err := agentStateAttribute.Set(ctx, NewAgentState()); err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	if step.flow.lease == nil {
		return dex.GoTo(awaitUserStep{flow: step.flow}, nil), nil
	}
	if _, err := agentLeaseStateAttribute.Get(ctx); err != nil {
		return nil, fmt.Errorf("read initial Agent Lease state: %w", err)
	}
	if _, err := agentLeaseScheduleAttribute.Get(ctx); err != nil {
		return nil, fmt.Errorf("read initial Agent Lease schedule: %w", err)
	}
	return dex.GoToMany(
		dex.MovementOf(awaitUserStep{flow: step.flow}, nil),
		dex.MovementOf(waitForLeaseRefreshStep{flow: step.flow}, nil),
	), nil
}

type awaitUserStep struct {
	dex.StepDefaults
	flow *Flow
}

var _ dex.Step[dex.None] = awaitUserStep{}

func (awaitUserStep) GetStepType() string { return string(stepTypeAwaitUser) }

func (awaitUserStep) GetStepOptions() *dex.StepOptions { return messageMutationStepOptions }

func (step awaitUserStep) WaitFor(ctx dex.Context, _ dex.None) (*dex.Wait, error) {
	if err := step.flow.updateStatus(ctx, AgentStatusWaitingForMessage); err != nil {
		return nil, err
	}
	plan, err := step.flow.getPlan(ctx)
	if err != nil {
		return nil, err
	}
	pendingInput, err := getPendingUserInput(ctx)
	if err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusWaiting); err != nil {
		return nil, err
	}
	if pendingInput == nil && plan != nil && plan.Status != PlanStatusCompleted {
		return dex.AnyOf(
			steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
			queuedUserMessagesChannel.ForOne(),
			planExecutionsChannel.ForOne(planRevisionKey(plan.Revision)),
		), nil
	}
	return dex.AnyOf(
		steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
		queuedUserMessagesChannel.ForOne(),
	), nil
}

func (step awaitUserStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	steered, err := steeredUserMessagesChannel.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(steered) > 0 {
		if beginErr := step.flow.beginSteeredTurn(ctx, steered); beginErr != nil {
			return nil, beginErr
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCompactContext), nil
	}
	queued, err := queuedUserMessagesChannel.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(queued) > 0 {
		if beginErr := step.flow.beginUserTurn(ctx, queued[0]); beginErr != nil {
			return nil, beginErr
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCompactContext), nil
	}

	plan, err := step.flow.getPlan(ctx)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, errors.New("the Agent wait completed without an input")
	}
	executions, err := planExecutionsChannel.GetConditionResults(ctx, planRevisionKey(plan.Revision))
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	if len(executions) == 0 || executions[0].Revision != plan.Revision ||
		state.PendingPlanExecutionRevision == nil || *state.PendingPlanExecutionRevision != plan.Revision {
		state.PendingPlanExecutionRevision = nil
		if err := agentStateAttribute.Set(ctx, state); err != nil {
			return nil, err
		}
		return dex.GoTo(awaitUserStep{flow: step.flow}, nil), nil
	}
	if plan.Status == PlanStatusDraft {
		plan.Status = PlanStatusActive
		if err := agentPlanAttribute.Set(ctx, *plan); err != nil {
			return nil, err
		}
	}
	state.Status = AgentStatusCallingModel
	state.InteractionMode = InteractionModeExecuting
	state.PlanningRequiresWrite = false
	state.PlanningAllowsWrite = false
	state.PendingPlanExecutionRevision = nil
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return nil, err
	}
	if err := step.flow.writeActivity(ctx, AgentEvent{
		Kind:    EventKindPlanStarted,
		Message: fmt.Sprintf("Executing plan revision %d.", plan.Revision),
	}); err != nil {
		return nil, err
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCompactContext), nil
}

type compactContextStep struct {
	dex.StepDefaultsNoWaitFor[Sequence]
	flow *Flow
}

var _ dex.Step[Sequence] = compactContextStep{}

func (compactContextStep) GetStepType() string { return string(stepTypeCompactContext) }

func (compactContextStep) GetStepOptions() *dex.StepOptions { return modelStepOptions }

func (step compactContextStep) Execute(ctx dex.Context, input Sequence) (*dex.StepDecision, error) {
	if err := step.flow.updateStatus(ctx, AgentStatusCompactingContext); err != nil {
		return nil, err
	}
	config, err := agentConfigAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	messages, err := step.flow.loadMessages(
		ctx,
		state.SummarizedThroughSequence+1,
		input,
		config,
	)
	if err != nil {
		return nil, err
	}
	previousSummary, err := step.flow.getSummary(ctx)
	if err != nil {
		return nil, err
	}
	if heartbeatErr := ctx.RecordHeartbeat(compactionHeartbeat{
		Phase:           heartbeatPhaseCompacting,
		ThroughSequence: input,
	}); heartbeatErr != nil {
		return nil, heartbeatErr
	}
	summary, err := step.flow.modelClient.Summarize(ctx, SummarizeRequest{
		Config:          config,
		PreviousSummary: previousSummary.Content,
		Messages:        messages,
		FlowID:          FlowID(ctx.FlowID()),
	})
	if err != nil {
		if eventErr := step.flow.writeActivity(ctx, AgentEvent{Kind: EventKindCompactionFailed, Message: "Context compaction failed."}); eventErr != nil {
			return nil, errors.Join(err, eventErr)
		}
		return nil, err
	}
	generation := state.CompactionGeneration + 1
	if err := contextSummaryAttribute.Set(ctx, ContextSummary{
		Generation:                generation,
		SummarizedThroughSequence: input,
		Content:                   summary,
	}); err != nil {
		return nil, err
	}
	state.SummarizedThroughSequence = input
	state.CompactionGeneration = generation
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return nil, err
	}
	if _, err := step.flow.trimSummarizedMessages(ctx, config, state); err != nil {
		return nil, err
	}
	activity := AgentEvent{
		Kind:    EventKindCompacted,
		Message: fmt.Sprintf("Compacted conversation through message %d.", input),
	}
	activity.Message = condenseActivityMessage(activity.Message)
	if err := agentActivityStream.Write(ctx, activity); err != nil {
		return nil, err
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCallModel), nil
}

type callModelStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = callModelStep{}

func (callModelStep) GetStepType() string { return string(stepTypeCallModel) }

func (callModelStep) GetStepOptions() *dex.StepOptions { return modelStepOptions }

func (step callModelStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	if err := step.flow.updateStatus(ctx, AgentStatusCallingModel); err != nil {
		return nil, err
	}
	config, err := agentConfigAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	tools := step.flow.invocationToolDefinitions(config, state)
	var forcedToolName ToolName
	if state.InteractionMode == InteractionModePlanning && state.PlanningRequiresWrite {
		forcedToolName = ToolNameWriteTodos
	}
	assistantWriter, err := dex.NewBufferedTextStream(ctx, assistantTextStream)
	if err != nil {
		return nil, err
	}
	reasoningWriter, err := dex.NewBufferedTextStream(ctx, reasoningSummaryStream)
	if err != nil {
		return nil, err
	}
	progress := modelProgress{
		ctx:               ctx,
		activityWriteFunc: step.flow.writeActivity,
		messageSequence:   state.NextSequence,
	}
	writeAssistant := func(chunk string) error {
		if heartbeatErr := ctx.RecordHeartbeat(modelHeartbeat{Phase: heartbeatPhaseAssistantStream}); heartbeatErr != nil {
			return heartbeatErr
		}
		return assistantWriter.Write(chunk)
	}
	writeReasoning := func(chunk string) error {
		if heartbeatErr := ctx.RecordHeartbeat(modelHeartbeat{Phase: heartbeatPhaseReasoningStream}); heartbeatErr != nil {
			return heartbeatErr
		}
		return reasoningWriter.Write(chunk)
	}
	if activityErr := progress.writeActivity(AgentEvent{Kind: EventKindModelStarted, Message: "Calling " + string(config.Model) + "."}); activityErr != nil {
		return nil, activityErr
	}
	messages, err := step.flow.contextMessages(ctx, config, state)
	if err != nil {
		return nil, err
	}
	reply, err := step.flow.modelClient.Complete(ctx, ModelRequest{
		Config:         step.flow.modelConfig(config),
		Messages:       messages,
		Tools:          tools,
		WriteAssistant: writeAssistant,
		WriteReasoning: writeReasoning,
		WriteActivity:  progress.writeActivity,
		ForcedTool:     forcedToolName,
		FlowID:         FlowID(ctx.FlowID()),
	})
	if err != nil {
		if eventErr := progress.writeActivity(AgentEvent{Kind: EventKindModelFailed, Message: "Model request failed."}); eventErr != nil {
			return nil, errors.Join(err, eventErr)
		}
		return nil, err
	}
	if strings.TrimSpace(reply.Content) == "" && len(reply.ToolCalls) == 0 {
		return nil, errors.New("the model returned no content or tool calls")
	}
	messageSequence, appendErr := step.flow.appendMessage(ctx, AgentMessage{
		Role:                 MessageRoleAssistant,
		Content:              reply.Content,
		ToolCalls:            reply.ToolCalls,
		ProviderContextItems: reply.ProviderContextItems,
	})
	if appendErr != nil {
		return nil, appendErr
	}
	if messageSequence != progress.messageSequence {
		return nil, fmt.Errorf("assistant message sequence changed from %d to %d", progress.messageSequence, messageSequence)
	}
	eventMessage := "Model response completed."
	if len(reply.ToolCalls) > 0 {
		names := make([]string, 0, len(reply.ToolCalls))
		for _, call := range reply.ToolCalls {
			names = append(names, string(call.Name))
		}
		eventMessage = "Model requested: " + strings.Join(names, ", ")
	}
	if activityErr := progress.writeActivity(AgentEvent{Kind: EventKindModelCompleted, Message: eventMessage}); activityErr != nil {
		return nil, activityErr
	}
	state, err = agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err = step.flow.trimSummarizedMessages(ctx, config, state)
	if err != nil {
		return nil, err
	}
	if len(reply.ToolCalls) == 0 {
		plan, err := step.flow.getPlan(ctx)
		if err != nil {
			return nil, err
		}
		if plan == nil || plan.Status == PlanStatusCompleted {
			state.InteractionMode = InteractionModeChat
			state.PlanningRequiresWrite = false
			state.PlanNoProgressAttempts = 0
			if err := agentStateAttribute.Set(ctx, state); err != nil {
				return nil, err
			}
		} else if plan.Status == PlanStatusActive &&
			state.InteractionMode == InteractionModeExecuting &&
			!allTasksCompleted(plan.Tasks) {
			state.PlanNoProgressAttempts++
			if err := agentStateAttribute.Set(ctx, state); err != nil {
				return nil, err
			}
			if state.PlanNoProgressAttempts <= maximumAutomaticPlanRecoveryCount {
				return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCallModel), nil
			}
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueAwaitUser), nil
	}
	state.Status = AgentStatusRoutingTool
	state.PendingToolCalls = reply.ToolCalls
	state.PendingToolIndex = 0
	state.PlanNoProgressAttempts = 0
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return nil, err
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, continueRouteTool), nil
}

func (flow *Flow) modelConfig(config AgentConfig) AgentConfig {
	if flow.lease == nil {
		return config
	}
	config.SystemPrompt += "\n\n" + leaseRecoveryPrompt
	return config
}

type checkSteeredStep struct {
	dex.StepDefaults
	flow *Flow
}

var _ dex.Step[continuation] = checkSteeredStep{}

func (checkSteeredStep) GetStepType() string { return string(stepTypeCheckSteered) }

func (checkSteeredStep) GetStepOptions() *dex.StepOptions { return messageContextStepOptions }

func (checkSteeredStep) WaitFor(_ dex.Context, _ continuation) (*dex.Wait, error) {
	return dex.Until(steeredUserMessagesChannel.AtMost(maximumSteeringMessageCount)), nil
}

func (step checkSteeredStep) Execute(ctx dex.Context, input continuation) (*dex.StepDecision, error) {
	messages, err := steeredUserMessagesChannel.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(messages) > 0 {
		if err := step.flow.beginSteeredTurn(ctx, messages); err != nil {
			return nil, err
		}
		input = continueCompactContext
	}
	switch input {
	case continueCompactContext:
		cutoff, err := step.flow.pendingCompactionCutoff(ctx)
		if err != nil {
			return nil, err
		}
		if cutoff != nil {
			return dex.GoTo(compactContextStep{flow: step.flow}, *cutoff), nil
		}
		return dex.GoTo(callModelStep{flow: step.flow}, nil), nil
	case continueAwaitUser:
		return dex.GoTo(awaitUserStep{flow: step.flow}, nil), nil
	case continueCallModel:
		return dex.GoTo(callModelStep{flow: step.flow}, nil), nil
	case continueRouteTool:
		return dex.GoTo(routeToolStep{flow: step.flow}, nil), nil
	case continueAwaitToolApproval:
		return dex.GoTo(awaitToolApprovalStep{flow: step.flow}, nil), nil
	case continueExecuteTool:
		return dex.GoTo(executeToolStep{flow: step.flow}, nil), nil
	case continueExecuteToolRetry:
		options, err := step.flow.currentToolStepOptions(ctx)
		if err != nil {
			return nil, err
		}
		return dex.GoTo(
			executeToolWithRetryStep{flow: step.flow},
			nil,
			dex.WithStepOptions(options),
		), nil
	case continueDurableWait:
		return dex.GoTo(durableWaitStep{flow: step.flow}, nil), nil
	default:
		return nil, fmt.Errorf("unknown Agent continuation %q", input)
	}
}

type routeToolStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = routeToolStep{}

func (routeToolStep) GetStepType() string { return string(stepTypeRouteTool) }

func (routeToolStep) GetStepOptions() *dex.StepOptions { return messageMutationStepOptions }

func (step routeToolStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	if err := step.flow.updateStatus(ctx, AgentStatusRoutingTool); err != nil {
		return nil, err
	}
	call, err := step.flow.currentToolCall(ctx)
	if err != nil {
		return nil, err
	}
	config, err := agentConfigAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	definition, err := step.flow.invocationToolDefinition(config, state, call.Name)
	if err != nil {
		result, encodeErr := encodeToolResult(toolResultPayload{
			Status: toolResultStatusFailed,
			Error:  toolErrorUnknownOrDisabled,
			Tool:   call.Name,
		}, ToolOutcomeKnownFailure, true)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if err := step.flow.appendToolResult(ctx, call, result); err != nil {
			return nil, err
		}
		return step.flow.continueAfterTool(ctx)
	}
	if call.Name == ToolNameWriteTodos {
		count := 0
		for _, pending := range state.PendingToolCalls {
			if pending.Name == ToolNameWriteTodos {
				count++
			}
		}
		if count > 1 {
			result, encodeErr := encodeToolResult(toolResultPayload{
				Status: toolResultStatusFailed,
				Error:  toolErrorMultiplePlanWrites,
			}, ToolOutcomeKnownFailure, true)
			if encodeErr != nil {
				return nil, encodeErr
			}
			if err := step.flow.appendToolResult(ctx, call, result); err != nil {
				return nil, err
			}
			return step.flow.continueAfterTool(ctx)
		}
		tasks, parseErr := planTasks(call)
		if parseErr != nil {
			result, encodeErr := encodeToolResult(toolResultPayload{
				Status:  toolResultStatusFailed,
				Error:   toolErrorInvalidPlan,
				Message: parseErr.Error(),
			}, ToolOutcomeKnownFailure, true)
			if encodeErr != nil {
				return nil, encodeErr
			}
			if err := step.flow.appendToolResult(ctx, call, result); err != nil {
				return nil, err
			}
			return step.flow.continueAfterTool(ctx)
		}
		revision, err := step.flow.replacePlan(ctx, tasks)
		if err != nil {
			return nil, err
		}
		status := toolResultStatusUpdated
		if len(tasks) == 0 {
			status = toolResultStatusCleared
		}
		result, encodeErr := encodeToolResult(toolResultPayload{
			Status:    status,
			Revision:  revision,
			TaskCount: len(tasks),
		}, ToolOutcomeSucceeded, false)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if err := step.flow.appendToolResult(ctx, call, result); err != nil {
			return nil, err
		}
		return step.flow.continueAfterTool(ctx)
	}
	if call.Name == ToolNameDurableWait {
		arguments, parseErr := durableWaitArgumentsFor(call)
		if parseErr != nil {
			result, encodeErr := encodeToolResult(toolResultPayload{
				Status:  toolResultStatusFailed,
				Error:   toolErrorInvalidDuration,
				Message: parseErr.Error(),
			}, ToolOutcomeKnownFailure, true)
			if encodeErr != nil {
				return nil, encodeErr
			}
			if err := step.flow.appendToolResult(ctx, call, result); err != nil {
				return nil, err
			}
			return step.flow.continueAfterTool(ctx)
		}
		if err := pendingTimerAttribute.Set(ctx, PendingTimer{
			CallID:          call.ID,
			DurationSeconds: arguments.DurationSeconds,
			Reason:          arguments.Reason,
		}); err != nil {
			return nil, err
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueDurableWait), nil
	}
	if call.Name == ToolNameRequestUserInput {
		pendingInput, pendingErr := getPendingUserInput(ctx)
		if pendingErr != nil {
			return nil, pendingErr
		}
		if pendingInput != nil {
			result, encodeErr := encodeToolResult(toolResultPayload{
				Status: toolResultStatusFailed,
				Error:  toolErrorUserInputPending,
			}, ToolOutcomeKnownFailure, true)
			if encodeErr != nil {
				return nil, encodeErr
			}
			if err := step.flow.appendToolResult(ctx, call, result); err != nil {
				return nil, err
			}
			return step.flow.continueAfterTool(ctx)
		}
		arguments, parseErr := userInputArgumentsFor(call)
		if parseErr != nil {
			result, encodeErr := encodeToolResult(toolResultPayload{
				Status:  toolResultStatusFailed,
				Error:   toolErrorInvalidUserInput,
				Message: parseErr.Error(),
			}, ToolOutcomeKnownFailure, true)
			if encodeErr != nil {
				return nil, encodeErr
			}
			if err := step.flow.appendToolResult(ctx, call, result); err != nil {
				return nil, err
			}
			return step.flow.continueAfterTool(ctx)
		}
		if err := pendingUserInputAttribute.Set(ctx, PendingUserInput{
			CallID:    call.ID,
			Questions: arguments.Questions,
		}); err != nil {
			return nil, err
		}
		result, encodeErr := encodeToolResult(toolResultPayload{
			Status:    toolResultStatusWaitingForUser,
			Questions: arguments.Questions,
		}, ToolOutcomeSucceeded, false)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if err := step.flow.appendToolResultAndCancelRemaining(
			ctx,
			call,
			result,
			toolErrorSupersededByUserInput,
		); err != nil {
			return nil, err
		}
		callID := call.ID
		toolName := call.Name
		if err := step.flow.writeActivity(ctx, AgentEvent{
			Kind:     EventKindUserInputRequested,
			Message:  fmt.Sprintf("Requested answers to %d question(s).", len(arguments.Questions)),
			CallID:   &callID,
			ToolName: &toolName,
		}); err != nil {
			return nil, err
		}
		return dex.GoTo(awaitUserStep{flow: step.flow}, nil), nil
	}
	if definition.RequiresApproval {
		if err := pendingApprovalAttribute.Set(ctx, PendingApproval{
			CallID:    call.ID,
			ToolName:  call.Name,
			Arguments: call.Arguments,
		}); err != nil {
			return nil, err
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueAwaitToolApproval), nil
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, continueExecuteToolRetry), nil
}

type awaitToolApprovalStep struct {
	dex.StepDefaults
	flow *Flow
}

var _ dex.Step[dex.None] = awaitToolApprovalStep{}

func (awaitToolApprovalStep) GetStepType() string { return string(stepTypeAwaitApproval) }

func (awaitToolApprovalStep) GetStepOptions() *dex.StepOptions { return messageMutationStepOptions }

func (step awaitToolApprovalStep) WaitFor(ctx dex.Context, _ dex.None) (*dex.Wait, error) {
	call, err := step.flow.currentToolCall(ctx)
	if err != nil {
		return nil, err
	}
	if err := step.flow.updateStatus(ctx, AgentStatusWaitingForToolApproval); err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusWaiting); err != nil {
		return nil, err
	}
	return dex.AnyOf(
		steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
		toolApprovalsChannel.ForOne(string(call.ID)),
	), nil
}

func (step awaitToolApprovalStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	steered, err := steeredUserMessagesChannel.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(steered) > 0 {
		if beginErr := step.flow.beginSteeredTurn(ctx, steered); beginErr != nil {
			return nil, beginErr
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCompactContext), nil
	}
	call, err := step.flow.currentToolCall(ctx)
	if err != nil {
		return nil, err
	}
	approvals, err := toolApprovalsChannel.GetConditionResults(ctx, string(call.ID))
	if err != nil {
		return nil, err
	}
	if len(approvals) == 0 {
		return nil, errors.New("the approval wait completed without a decision")
	}
	if deleteErr := pendingApprovalAttribute.Delete(ctx); deleteErr != nil && !isAttributeNotFound(deleteErr) {
		return nil, deleteErr
	}
	if approvals[0].Approved {
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueExecuteToolRetry), nil
	}
	result, encodeErr := encodeToolResult(toolResultPayload{
		Status: toolResultStatusFailed,
		Error:  toolErrorRejectedByUser,
	}, ToolOutcomeKnownFailure, true)
	if encodeErr != nil {
		return nil, encodeErr
	}
	if appendErr := step.flow.appendToolResult(ctx, call, result); appendErr != nil {
		return nil, appendErr
	}
	hasNext, err := step.flow.hasNextToolCall(ctx)
	if err != nil {
		return nil, err
	}
	if hasNext {
		if advanceErr := step.flow.advanceTool(ctx); advanceErr != nil {
			return nil, advanceErr
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueRouteTool), nil
	}
	if err := step.flow.clearPendingToolCalls(ctx); err != nil {
		return nil, err
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCompactContext), nil
}

type executeToolStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = executeToolStep{}

func (executeToolStep) GetStepType() string { return string(stepTypeExecuteTool) }

func (executeToolStep) GetStepOptions() *dex.StepOptions { return toolStepOptions }

func (step executeToolStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	if statusErr := step.flow.updateStatus(ctx, AgentStatusExecutingTool); statusErr != nil {
		return nil, statusErr
	}
	call, callErr := step.flow.currentToolCall(ctx)
	if callErr != nil {
		return nil, callErr
	}
	config, configErr := agentConfigAttribute.Get(ctx)
	if configErr != nil {
		return nil, configErr
	}
	runtimeMetadata, metadataErr := agentRuntimeMetadataAttribute.Get(ctx)
	if isAttributeNotFound(metadataErr) {
		runtimeMetadata = MustJSONObject(`{}`)
	} else if metadataErr != nil {
		return nil, metadataErr
	}
	leaseState, leaseErr := step.flow.currentLeaseState(ctx)
	if leaseErr != nil {
		return nil, leaseErr
	}
	progress := toolProgress{ctx: ctx, flow: step.flow, call: call}
	result, executeErr := step.flow.tools.Execute(ctx, ToolInvocation{
		FlowID:          FlowID(ctx.FlowID()),
		RuntimeMetadata: runtimeMetadata,
		LeaseState:      leaseState,
		Name:            call.Name,
		Arguments:       call.Arguments,
		EnabledServers:  config.EnabledMCPServers,
		WriteProgress:   progress.write,
		CallID:          call.ID,
		Attempt:         ctx.Attempt(),
		FirstAttemptAt:  ctx.FirstAttemptAt(),
	})
	if executeErr != nil {
		callID := call.ID
		toolName := call.Name
		if activityErr := step.flow.writeActivity(ctx, AgentEvent{
			Kind:     EventKindToolFailed,
			Message:  fmt.Sprintf("%s failed with %s.", call.Name, errorTypeName(executeErr)),
			CallID:   &callID,
			ToolName: &toolName,
		}); activityErr != nil {
			return nil, errors.Join(executeErr, activityErr)
		}
		failureResult, encodeErr := encodeToolResult(toolResultPayload{
			Status:    toolResultStatusFailed,
			Outcome:   ToolOutcomeUnknown,
			ErrorType: errorTypeName(executeErr),
		}, ToolOutcomeUnknown, true)
		if encodeErr != nil {
			return nil, errors.Join(executeErr, encodeErr)
		}
		result = failureResult
	}
	next, finishErr := step.flow.finishToolExecution(ctx, call, result)
	if finishErr != nil {
		return nil, finishErr
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, next), nil
}

func (flow *Flow) currentLeaseState(ctx dex.Context) (*JSONObject, error) {
	if flow.lease == nil {
		return nil, nil
	}
	state, err := agentLeaseStateAttribute.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Agent Lease state: %w", err)
	}
	return &state, nil
}

func (flow *Flow) finishToolExecution(
	ctx dex.Context,
	call ToolCall,
	result ToolExecutionResult,
) (continuation, error) {
	if err := flow.appendToolResult(ctx, call, result); err != nil {
		return "", err
	}
	callID := call.ID
	toolName := call.Name
	if err := flow.writeActivity(ctx, AgentEvent{
		Kind:     EventKindToolCompleted,
		Message:  fmt.Sprintf("Completed %s.", call.Name),
		CallID:   &callID,
		ToolName: &toolName,
	}); err != nil {
		return "", err
	}
	hasNext, nextErr := flow.hasNextToolCall(ctx)
	if nextErr != nil {
		return "", nextErr
	}
	if hasNext {
		if advanceErr := flow.advanceTool(ctx); advanceErr != nil {
			return "", advanceErr
		}
		return continueRouteTool, nil
	}
	if err := flow.clearPendingToolCalls(ctx); err != nil {
		return "", err
	}
	return continueCompactContext, nil
}

type executeToolWithRetryStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = executeToolWithRetryStep{}

func (executeToolWithRetryStep) GetStepType() string { return string(stepTypeExecuteRetry) }

func (step executeToolWithRetryStep) GetStepOptions() *dex.StepOptions {
	return &dex.StepOptions{
		ExecuteMethodTimeout:     toolStepOptions.ExecuteMethodTimeout,
		HeartbeatTimeout:         toolStepOptions.HeartbeatTimeout,
		ExecuteLoadAttributeMaps: toolStepOptions.ExecuteLoadAttributeMaps,
		ExecuteRetry:             toolStepOptions.ExecuteRetry,
		ExecuteFailure: dex.ProceedToOnExecuteFailure(
			recoverToolExecutionStep{flow: step.flow},
			messageMutationStepOptions,
		),
	}
}

func (step executeToolWithRetryStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	if err := step.flow.updateStatus(ctx, AgentStatusExecutingTool); err != nil {
		return nil, err
	}
	call, err := step.flow.currentToolCall(ctx)
	if err != nil {
		return nil, err
	}
	config, err := agentConfigAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	runtimeMetadata, err := agentRuntimeMetadataAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		runtimeMetadata = MustJSONObject(`{}`)
	} else if err != nil {
		return nil, err
	}
	leaseState, err := step.flow.currentLeaseState(ctx)
	if err != nil {
		return nil, err
	}
	progress := toolProgress{ctx: ctx, flow: step.flow, call: call}
	if writeErr := progress.write(fmt.Sprintf("Calling %s (attempt %d).", call.Name, ctx.Attempt())); writeErr != nil {
		return nil, writeErr
	}
	result, err := step.flow.tools.Execute(ctx, ToolInvocation{
		FlowID:          FlowID(ctx.FlowID()),
		RuntimeMetadata: runtimeMetadata,
		LeaseState:      leaseState,
		Name:            call.Name,
		Arguments:       call.Arguments,
		EnabledServers:  config.EnabledMCPServers,
		WriteProgress:   progress.write,
		CallID:          call.ID,
		Attempt:         ctx.Attempt(),
		FirstAttemptAt:  ctx.FirstAttemptAt(),
	})
	if err != nil {
		return nil, fmt.Errorf("execute tool %q: %w", call.Name, err)
	}
	if validationErr := result.Outcome.Validate(); validationErr != nil {
		return nil, fmt.Errorf("tool %q outcome: %w", call.Name, validationErr)
	}
	next, finishErr := step.flow.finishToolExecution(ctx, call, result)
	if finishErr != nil {
		return nil, finishErr
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, next), nil
}

type recoverToolExecutionStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = recoverToolExecutionStep{}

func (recoverToolExecutionStep) GetStepType() string { return string(stepTypeRecoverTool) }

func (recoverToolExecutionStep) GetStepOptions() *dex.StepOptions {
	return messageMutationStepOptions
}

func (step recoverToolExecutionStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	call, err := step.flow.currentToolCall(ctx)
	if err != nil {
		return nil, err
	}
	failure := ctx.RecoveryError()
	if failure == nil {
		return nil, errors.New("tool recovery is missing the exhausted failure")
	}
	result, err := encodeToolResult(toolResultPayload{
		Status:    toolResultStatusFailed,
		Outcome:   ToolOutcomeUnknown,
		ErrorType: failure.ErrorType,
	}, ToolOutcomeUnknown, true)
	if err != nil {
		return nil, err
	}
	callID := call.ID
	toolName := call.Name
	if activityErr := step.flow.writeActivity(ctx, AgentEvent{
		Kind:     EventKindToolFailed,
		Message:  fmt.Sprintf("%s failed after its retry policy was exhausted.", call.Name),
		CallID:   &callID,
		ToolName: &toolName,
	}); activityErr != nil {
		return nil, activityErr
	}
	next, finishErr := step.flow.finishToolExecution(ctx, call, result)
	if finishErr != nil {
		return nil, finishErr
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, next), nil
}

type durableWaitStep struct {
	dex.StepDefaults
	flow *Flow
}

var _ dex.Step[dex.None] = durableWaitStep{}

func (durableWaitStep) GetStepType() string { return string(stepTypeDurableWait) }

func (durableWaitStep) GetStepOptions() *dex.StepOptions { return messageMutationStepOptions }

func (step durableWaitStep) WaitFor(ctx dex.Context, _ dex.None) (*dex.Wait, error) {
	timer, err := pendingTimerAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	if err := step.flow.updateStatus(ctx, AgentStatusWaitingForTimer); err != nil {
		return nil, err
	}
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusWaiting); err != nil {
		return nil, err
	}
	return dex.AnyOf(
		dex.Timer(time.Duration(timer.DurationSeconds)*time.Second),
		steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
	), nil
}

func (step durableWaitStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	if err := agentInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	call, err := step.flow.currentToolCall(ctx)
	if err != nil {
		return nil, err
	}
	timer, err := pendingTimerAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	steered, err := steeredUserMessagesChannel.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if err := pendingTimerAttribute.Delete(ctx); err != nil {
		return nil, err
	}
	if len(steered) > 0 {
		result, encodeErr := encodeToolResult(toolResultPayload{
			Status: toolResultStatusInterrupted,
			Error:  toolErrorSupersededBySteering,
			Reason: timer.Reason,
		}, ToolOutcomeKnownFailure, true)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if err := step.flow.appendToolResultAndCancelRemaining(
			ctx,
			call,
			result,
			toolErrorSupersededBySteering,
		); err != nil {
			return nil, err
		}
		if err := step.flow.beginSteeredTurn(ctx, steered); err != nil {
			return nil, err
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCompactContext), nil
	}
	result, encodeErr := encodeToolResult(toolResultPayload{
		Status:          toolResultStatusCompleted,
		DurationSeconds: timer.DurationSeconds,
		Reason:          timer.Reason,
	}, ToolOutcomeSucceeded, false)
	if encodeErr != nil {
		return nil, encodeErr
	}
	if err := step.flow.appendToolResult(ctx, call, result); err != nil {
		return nil, err
	}
	return step.flow.continueAfterTool(ctx)
}

type waitForLeaseRefreshStep struct {
	dex.StepDefaults
	flow *Flow
}

var _ dex.Step[dex.None] = waitForLeaseRefreshStep{}

func (waitForLeaseRefreshStep) GetStepType() string { return string(stepTypeWaitLease) }

func (step waitForLeaseRefreshStep) WaitFor(ctx dex.Context, _ dex.None) (*dex.Wait, error) {
	schedule, err := agentLeaseScheduleAttribute.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Agent Lease schedule: %w", err)
	}
	delay := time.Until(schedule.RefreshAt)
	if delay < 0 {
		delay = 0
	}
	return dex.Until(dex.Timer(delay)), nil
}

func (step waitForLeaseRefreshStep) Execute(_ dex.Context, _ dex.None) (*dex.StepDecision, error) {
	return dex.GoTo(refreshLeaseStep{flow: step.flow}, nil), nil
}

type refreshLeaseStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = refreshLeaseStep{}

func (refreshLeaseStep) GetStepType() string { return string(stepTypeRefreshLease) }

func (step refreshLeaseStep) GetStepOptions() *dex.StepOptions {
	if step.flow.lease == nil {
		return &dex.StepOptions{ExecuteRetry: &dex.RetryPolicy{MaximumAttempts: 1}}
	}
	return &dex.StepOptions{
		ExecuteMethodTimeout: step.flow.lease.config.AttemptTimeout,
		ExecuteRetry: &dex.RetryPolicy{
			TotalDuration: step.flow.lease.config.RetryTotalDuration,
		},
	}
}

func (step refreshLeaseStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	if step.flow.lease == nil {
		return nil, errors.New("agent Lease extension is not configured")
	}
	state, err := agentLeaseStateAttribute.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Agent Lease state: %w", err)
	}
	schedule, err := agentLeaseScheduleAttribute.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Agent Lease schedule: %w", err)
	}
	targetGeneration := schedule.Generation + 1
	refreshID := stableLeaseRefreshID(FlowID(ctx.FlowID()), targetGeneration)
	result, err := step.flow.lease.refresher.Refresh(ctx, LeaseRefreshRequest{
		FlowID:           FlowID(ctx.FlowID()),
		State:            state,
		RefreshID:        refreshID,
		TargetGeneration: targetGeneration,
	})
	if err != nil {
		return nil, fmt.Errorf("refresh Agent Lease: %w", err)
	}
	if err := validateLeaseInitialization(&LeaseInitialization{
		State:     result.State,
		RefreshAt: result.RefreshAt,
	}, time.Now()); err != nil {
		return nil, fmt.Errorf("validate refreshed Agent Lease: %w", err)
	}
	if err := agentLeaseStateAttribute.Set(ctx, result.State); err != nil {
		return nil, err
	}
	if err := agentLeaseScheduleAttribute.Set(ctx, leaseSchedule{
		Generation: targetGeneration,
		RefreshAt:  result.RefreshAt,
	}); err != nil {
		return nil, err
	}
	return dex.GoTo(waitForLeaseRefreshStep{flow: step.flow}, nil), nil
}

func stableLeaseRefreshID(flowID FlowID, generation int64) LeaseRefreshID {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", flowID, generation)))
	return LeaseRefreshID(hex.EncodeToString(digest[:]))
}

type modelProgress struct {
	ctx               dex.Context
	activityWriteFunc func(dex.Context, AgentEvent) error
	messageSequence   Sequence
}

func (progress modelProgress) writeActivity(event AgentEvent) error {
	if err := progress.ctx.RecordHeartbeat(modelHeartbeat{
		Phase:     heartbeatPhaseActivityStream,
		EventKind: &event.Kind,
	}); err != nil {
		return err
	}
	event.MessageSequence = &progress.messageSequence
	// Streams are disposable; heartbeat failure still cancels the provider call.
	_ = progress.activityWriteFunc(progress.ctx, event)
	return nil
}

type toolProgress struct {
	ctx  dex.Context
	flow *Flow
	call ToolCall
}

func (progress toolProgress) write(message string) error {
	if err := progress.ctx.RecordHeartbeat(toolHeartbeat{
		Phase:    heartbeatPhaseToolProgress,
		ToolName: progress.call.Name,
	}); err != nil {
		return err
	}
	callID := progress.call.ID
	toolName := progress.call.Name
	// Streams are disposable; heartbeat failure still cancels the tool call.
	_ = progress.flow.writeActivity(progress.ctx, AgentEvent{
		Kind:     EventKindToolProgress,
		Message:  toolProgressMessage(progress.call.Name, message),
		CallID:   &callID,
		ToolName: &toolName,
	})
	return nil
}

func toolProgressMessage(tool ToolName, message string) string {
	if strings.TrimSpace(message) == "" {
		return "Running " + string(tool) + "."
	}
	return condenseActivityMessage(message)
}

func errorTypeName(err error) string {
	value := reflect.TypeOf(err)
	if value == nil {
		return "error"
	}
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Name() == "" {
		return "error"
	}
	return value.Name()
}

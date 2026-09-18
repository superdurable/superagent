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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/superdurable/dex/sdk-go/dex"
)

var (
	agentConfigAttribute          = dex.DefineAttribute[AgentConfig]("AgentConfig")
	agentRuntimeMetadataAttribute = dex.DefineAttribute[JSONObject]("AgentRuntimeMetadata")
	agentStateAttribute           = dex.DefineAttribute[AgentState]("AgentState")
	waitingInputRoundAttribute    = dex.DefineAttribute[WaitingInputRound]("WaitingInputRound")
	contextSummaryAttribute       = dex.DefineAttribute[ContextSummary]("ContextSummary")
	currentMessagesAttribute      = dex.DefineAttributeMap[AgentMessage]("CurrentMessages")
	archivedMessagesAttribute     = dex.DefineAttributeMap[ArchivedMessageChunk]("ArchivedMessages")
	agentPlanAttribute            = dex.DefineAttribute[AgentPlan]("AgentPlan")
	pendingApprovalAttribute      = dex.DefineAttribute[PendingApproval]("PendingApproval")
	pendingToolRecoveryAttribute  = dex.DefineAttribute[PendingToolRecovery]("PendingToolRecovery")
	pendingTimerAttribute         = dex.DefineAttribute[PendingTimer]("PendingTimer")
	pendingUserInputAttribute     = dex.DefineAttribute[PendingUserInput]("PendingUserInput")
	answeredUserInputsChannel     = dex.DefineChannel[AnsweredUserInput]("AnsweredUserInputs")
	queuedUserMessagesChannel     = dex.DefineChannel[PendingUserMessage]("QueuedUserMessages")
	steeredUserMessagesChannel    = dex.DefineChannel[PendingUserMessage]("SteeredUserMessages")
	toolApprovalsChannel          = dex.DefineChannelMap[ToolApproval]("ToolApprovals")
	toolRecoveryDecisionsChannel  = dex.DefineChannelMap[ResolveToolRecoveryRequest]("ToolRecoveryDecisions")
	parallelToolResultsChannel    = dex.DefineChannelMap[parallelToolResult]("ParallelToolResults")
	planExecutionsChannel         = dex.DefineChannelMap[PlanExecutionRequest]("PlanExecutions")
	reasoningSummaryStream        = dex.DefineStream[string]("ReasoningSummary", 10<<20)
	assistantTextStream           = dex.DefineStream[string]("AssistantText", 10<<20)
	agentActivityStream           = dex.DefineStream[AgentEvent]("AgentActivity", 10<<20)
)

// Flow is the durable AI Agent state machine.
type Flow struct {
	modelClient               ModelClient
	tools                     ToolRegistry
	rpcDefinitionsForTestOnly []dex.RPCDef
}

var _ dex.Flow = (*Flow)(nil)

// NewFlow constructs an Agent from its model and trusted tool boundaries.
func NewFlow(modelClient ModelClient, tools ToolRegistry) *Flow {
	if modelClient == nil {
		panic("model client is required")
	}
	if tools == nil {
		panic("tool registry is required")
	}
	return &Flow{modelClient: modelClient, tools: tools}
}

const flowTypeAIAgent = "AIAgentFlow"

// GetFlowType pins the durable Flow identity.
func (*Flow) GetFlowType() string {
	return flowTypeAIAgent
}

// GetSteps registers the state-machine nodes.
func (flow *Flow) GetSteps() []dex.StepDef {
	// Tool-call Step topology:
	//
	// ModelReply.ToolCalls -> AgentState.PendingToolCalls -> routeToolStep
	//   |- built-ins:
	//   |    write_todos -> finish in routeToolStep
	//   |    durable_wait -> checkSteeredStep -> durableWaitStep
	//   |    request_user_input -> awaitUserStep
	//   |- consecutive eligible external calls:
	//   |    parallelToolMovements -> GoToMany(
	//   |        awaitParallelToolResultsStep, executeParallelToolStep x N)
	//   `- other external call:
	//        awaitToolApprovalStep when required -> checkSteeredStep -> executeToolStep
	//
	// Serial failures recover through recoverToolExecutionStep or the manual
	// recovery Steps. Parallel failures normalize per branch before the join.
	return []dex.StepDef{
		dex.DefineStartStep(initStep{flow: flow}),
		dex.DefineStep(awaitUserStep{flow: flow}),
		dex.DefineStep(answeredInputStep{flow: flow}),
		dex.DefineStep(compactContextStep{flow: flow}),
		dex.DefineStep(callModelStep{flow: flow}),
		dex.DefineStep(checkSteeredStep{flow: flow}),
		dex.DefineStep(routeToolStep{flow: flow}),
		dex.DefineStep(awaitToolApprovalStep{flow: flow}),
		dex.DefineStep(executeToolStep{flow: flow}),
		dex.DefineStep(recoverToolExecutionStep{flow: flow}),
		dex.DefineStep(executeParallelToolStep{flow: flow}),
		dex.DefineStep(recoverParallelToolExecutionStep{flow: flow}),
		dex.DefineStep(awaitParallelToolResultsStep{flow: flow}),
		dex.DefineStep(prepareManualToolRecoveryStep{flow: flow}),
		dex.DefineStep(awaitManualToolRecoveryStep{flow: flow}),
		dex.DefineStep(durableWaitStep{flow: flow}),
	}
}

// GetRPCs registers synchronous Agent reads and commands with immutable execution policy.
func (flow *Flow) GetRPCs() []dex.RPCDef {
	definitions := []dex.RPCDef{
		dex.DefineRPC(flow.SendMessage, &dex.RPCOptions{
			Timeout:        defaultCommandTimeout,
			LockAttributes: []dex.AttributeLock{dex.LockAttribute(pendingUserInputAttribute)},
		}),
		dex.DefineRPC(flow.AnswerQuestions, &dex.RPCOptions{
			Timeout:        defaultCommandTimeout,
			LockAttributes: []dex.AttributeLock{dex.LockAttribute(pendingUserInputAttribute)},
		}),
		dex.DefineRPC(flow.SteerMessage, &dex.RPCOptions{
			Timeout:         defaultCommandTimeout,
			LockAttributes:  []dex.AttributeLock{dex.LockAttribute(pendingToolRecoveryAttribute)},
			IsTransactional: true,
			LoadChannels:    []dex.ChannelDef{queuedUserMessagesChannel},
		}),
		dex.DefineRPC(flow.GetSnapshot, &dex.RPCOptions{
			Timeout:           defaultSnapshotTimeout,
			LoadAttributeMaps: []dex.AttributeDef{currentMessagesAttribute},
			LoadChannels: []dex.ChannelDef{
				queuedUserMessagesChannel,
				steeredUserMessagesChannel,
			},
		}),
		dex.DefineRPC(flow.GetArchivedMessages, &dex.RPCOptions{
			Timeout:           defaultCommandTimeout,
			LoadAttributeMaps: []dex.AttributeDef{archivedMessagesAttribute},
		}),
		dex.DefineRPC(flow.DeleteQueuedMessage, &dex.RPCOptions{
			Timeout:         defaultCommandTimeout,
			IsTransactional: true,
			LoadChannels:    []dex.ChannelDef{queuedUserMessagesChannel},
		}),
		dex.DefineRPC(flow.ApproveTool, &dex.RPCOptions{
			Timeout:         defaultCommandTimeout,
			LockAttributes:  []dex.AttributeLock{dex.LockAttribute(pendingApprovalAttribute)},
			IsTransactional: true,
		}),
		dex.DefineRPC(flow.ResolveToolRecovery, &dex.RPCOptions{
			Timeout:        defaultCommandTimeout,
			LockAttributes: []dex.AttributeLock{dex.LockAttribute(pendingToolRecoveryAttribute)},
		}),
		dex.DefineRPC(flow.ExecutePlan, &dex.RPCOptions{
			Timeout:         defaultCommandTimeout,
			LockAttributes:  []dex.AttributeLock{dex.LockAttribute(agentStateAttribute)},
			IsTransactional: true,
		}),
	}
	return append(definitions, flow.rpcDefinitionsForTestOnly...)
}

// GetPersistenceSchema registers every durable value and best-effort stream.
func (*Flow) GetPersistenceSchema() dex.PersistenceSchema {
	return dex.PersistenceSchema{
		Attributes: []dex.AttributeDef{
			agentConfigAttribute,
			agentRuntimeMetadataAttribute,
			agentStateAttribute,
			waitingInputRoundAttribute,
			contextSummaryAttribute,
			currentMessagesAttribute,
			archivedMessagesAttribute,
			agentPlanAttribute,
			pendingApprovalAttribute,
			pendingToolRecoveryAttribute,
			pendingTimerAttribute,
			pendingUserInputAttribute,
		},
		Channels: []dex.ChannelDef{
			answeredUserInputsChannel,
			queuedUserMessagesChannel,
			steeredUserMessagesChannel,
			toolApprovalsChannel,
			toolRecoveryDecisionsChannel,
			parallelToolResultsChannel,
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
func (*Flow) SendMessage(ctx dex.Context, input PendingUserMessage) (*dex.RPCResult[bool], error) {
	if strings.TrimSpace(input.Value.Content) == "" {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if err := validateMessageID(input.MessageID); err != nil {
		return nil, err
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
	return &dex.RPCResult[bool]{Output: true}, nil
}

// AnswerQuestions validates and publishes one exact pending input batch atomically.
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
	if err := answeredUserInputsChannel.Publish(ctx, AnsweredUserInput{
		CallID:        pending.CallID,
		Message:       message,
		QuestionCount: len(pending.Questions),
	}); err != nil {
		return nil, err
	}
	return &dex.RPCResult[bool]{Output: true}, nil
}

// SteerMessage atomically moves a queued message into the Steer queue.
func (*Flow) SteerMessage(ctx dex.Context, input SteerMessageRequest) (*dex.RPCResult[bool], error) {
	if strings.TrimSpace(string(input.MessageID)) == "" {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	messages, err := queuedUserMessagesChannel.PendingMessages(ctx)
	if err != nil {
		return nil, err
	}
	message, found := findPendingUserMessage(messages, input.MessageID)
	if !found {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	if state.Status == AgentStatusWaitingForToolRecovery {
		pending, pendingErr := getPendingToolRecovery(ctx)
		if pendingErr != nil {
			return nil, pendingErr
		}
		if pending == nil {
			return &dex.RPCResult[bool]{Output: false}, nil
		}
		if deleteErr := pendingToolRecoveryAttribute.Delete(ctx); deleteErr != nil {
			return nil, deleteErr
		}
	}
	if err := queuedUserMessagesChannel.Delete(ctx, message.MessageID); err != nil {
		return nil, err
	}
	if err := steeredUserMessagesChannel.Publish(ctx, message.Value); err != nil {
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
		History:     history,
		Description: description,
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
	messages, err := queuedUserMessagesChannel.PendingMessages(ctx)
	if err != nil {
		return nil, err
	}
	message, found := findPendingUserMessage(messages, messageID)
	if !found {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if err := queuedUserMessagesChannel.Delete(ctx, message.MessageID); err != nil {
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
	return &dex.RPCResult[bool]{Output: true}, nil
}

// ResolveToolRecovery publishes one complete decision for the exact pending recovery revision.
func (*Flow) ResolveToolRecovery(
	ctx dex.Context,
	input ResolveToolRecoveryRequest,
) (*dex.RPCResult[bool], error) {
	pending, err := getPendingToolRecovery(ctx)
	if err != nil {
		return nil, err
	}
	if pending == nil || pending.RecoveryID != input.RecoveryID {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if !isValidToolRecoveryResolution(*pending, input) {
		return &dex.RPCResult[bool]{Output: false}, nil
	}
	if err := pendingToolRecoveryAttribute.Delete(ctx); err != nil {
		return nil, err
	}
	if err := toolRecoveryDecisionsChannel.Publish(ctx, string(input.RecoveryID), input); err != nil {
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
	pendingToolRecovery, err := getPendingToolRecovery(ctx)
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
		pendingToolRecovery == nil &&
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
	pendingToolRecovery, err := getPendingToolRecovery(ctx)
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
	waitingInputRound, err := waitingInputRoundAttribute.Get(ctx)
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
		WaitingInputRound:          waitingInputRound,
		Model:                      config.Model,
		SystemPrompt:               config.SystemPrompt,
		FirstRetainedSequence:      state.FirstRetainedSequence,
		LastSequence:               state.LastSequence,
		SummarizedThroughSequence:  state.SummarizedThroughSequence,
		PendingApproval:            pendingApproval,
		PendingToolRecovery:        pendingToolRecovery,
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
	queued []dex.ChannelMessage[PendingUserMessage],
	steered []dex.ChannelMessage[PendingUserMessage],
) AgentSnapshot {
	return AgentSnapshot{
		RunID:   RunID(ctx.RunID()),
		History: HistoryPage{Messages: []SequencedMessage{}},
		Description: AgentDescription{
			Status:                     AgentStatusInitializing,
			WaitingInputRound:          0,
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

func pendingUserMessages(messages []dex.ChannelMessage[PendingUserMessage]) []PendingUserMessage {
	result := make([]PendingUserMessage, 0, len(messages))
	for _, message := range messages {
		result = append(result, message.Value)
	}
	return result
}

func findPendingUserMessage(
	messages []dex.ChannelMessage[PendingUserMessage],
	messageID MessageID,
) (dex.ChannelMessage[PendingUserMessage], bool) {
	for _, message := range messages {
		if message.Value.MessageID == messageID {
			return message, true
		}
	}
	return dex.ChannelMessage[PendingUserMessage]{}, false
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
	policy := definition.RetryExhaustionPolicy.Effective()
	runningType := definition.RunningType.Effective()
	if err := runningType.Validate(); err != nil {
		return err
	}
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
		return policy.Validate()
	}
}

// currentSerialToolStepOptions resolves the current call before scheduling its
// serial execution movement. WithStepOptions carries the result to Dex.
func (flow *Flow) currentSerialToolStepOptions(ctx dex.Context) (*dex.StepOptions, error) {
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
	return flow.serialToolStepOptions(definition), nil
}

// serialToolStepOptions maps one definition to movement-scoped serial policy.
// Exhausted retries route according to that tool's recovery policy.
func (flow *Flow) serialToolStepOptions(definition ToolDefinition) *dex.StepOptions {
	failureStep := dex.ProceedToOnExecuteFailure(
		recoverToolExecutionStep{flow: flow},
		messageMutationStepOptions,
	)
	if definition.RetryExhaustionPolicy.Effective() == ToolRetryExhaustionPolicyManualRecovery {
		failureStep = dex.ProceedToOnExecuteFailure(
			prepareManualToolRecoveryStep{flow: flow},
			manualToolRecoveryStepOptions,
		)
	}
	return &dex.StepOptions{
		ExecuteMethodTimeout:     definition.AttemptTimeout,
		HeartbeatTimeout:         time.Minute,
		ExecuteDurability:        toolExecuteDurability(definition),
		ExecuteLoadAttributeMaps: toolStepOptions.ExecuteLoadAttributeMaps,
		ExecuteRetry: &dex.RetryPolicy{
			// #nosec G115 -- validateToolExecutionPolicy rejects values outside int32.
			MaximumAttempts: int32(definition.MaximumAttempts),
			TotalDuration:   definition.RetryTotalDuration,
		},
		ExecuteFailure: failureStep,
	}
}

// parallelToolStepOptions maps one definition to one branch movement. Branch
// exhaustion always becomes a typed result so the join can resolve the batch.
func (flow *Flow) parallelToolStepOptions(definition ToolDefinition) *dex.StepOptions {
	return &dex.StepOptions{
		ExecuteMethodTimeout: definition.AttemptTimeout,
		HeartbeatTimeout:     time.Minute,
		ExecuteDurability:    toolExecuteDurability(definition),
		ExecuteRetry: &dex.RetryPolicy{
			MaximumAttempts: int32(definition.MaximumAttempts), // #nosec G115 -- validated before scheduling.
			TotalDuration:   definition.RetryTotalDuration,
		},
		ExecuteFailure: dex.ProceedToOnExecuteFailure(
			recoverParallelToolExecutionStep{flow: flow},
			defaultStepOptions,
		),
	}
}

func toolExecuteDurability(definition ToolDefinition) dex.StepDurability {
	if definition.RunningType.Effective() == ToolRunningTypeLongRunning {
		return dex.StepDurabilitySync
	}
	return dex.StepDurabilityDefault
}

func (flow *Flow) parallelToolMovements(
	config AgentConfig,
	state AgentState,
) ([]dex.StepMovement, bool, error) {
	limit := config.EffectiveMaxParallelToolCalls()
	if limit <= 1 {
		return nil, false, nil
	}
	start := state.PendingToolIndex
	records := make([]toolBatchRecord, 0, limit)
	definitions := make([]ToolDefinition, 0, limit)
	for index := start; index < len(state.PendingToolCalls) && len(records) < limit; index++ {
		call := state.PendingToolCalls[index]
		definition, err := flow.invocationToolDefinition(config, state, call.Name)
		if err != nil || definition.RequiresApproval || !definition.SupportsParallelExecution {
			break
		}
		if err := validateToolExecutionPolicy(definition); err != nil {
			return nil, false, fmt.Errorf("tool %q: %w", definition.Name, err)
		}
		records = append(records, toolBatchRecord{
			Index:                 index,
			Call:                  call,
			RetryExhaustionPolicy: definition.RetryExhaustionPolicy.Effective(),
		})
		definitions = append(definitions, definition)
	}
	if len(records) < 2 {
		return nil, false, nil
	}
	batchID := fmt.Sprintf("tool-batch-%d-%d", state.LastSequence, start)
	batch := toolBatchState{BatchID: batchID, FirstIndex: start, Records: records}
	movements := make([]dex.StepMovement, 0, len(records)+1)
	movements = append(movements, dex.MovementOf(
		awaitParallelToolResultsStep{flow: flow},
		awaitParallelToolResultsInput{
			Batch:          batch,
			ResultInstance: batchID,
			ExpectedCount:  len(records),
		},
	))
	for index, record := range records {
		movements = append(movements, dex.MovementOf(
			executeParallelToolStep{flow: flow},
			parallelToolExecutionInput{
				ResultInstance:        batchID,
				Index:                 record.Index,
				Call:                  record.Call,
				RetryExhaustionPolicy: record.RetryExhaustionPolicy,
			},
			dex.WithStepOptions(flow.parallelToolStepOptions(definitions[index])),
		))
	}
	return movements, true, nil
}

func (flow *Flow) toolDefinitions(config AgentConfig) []ToolDefinition {
	definitions := []ToolDefinition{}
	if config.MCPEnabled {
		definitions = append(definitions, flow.tools.Definitions(config.EnabledMCPServers, config.EnabledTools)...)
	}
	definitions = append(definitions, durableWaitDefinition(), requestUserInputDefinition())
	if config.Model == DefaultModel {
		definitions = append(definitions, simulateFailureDefinition())
	}
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

func (flow *Flow) executeTool(
	ctx dex.Context,
	definition ToolDefinition,
	invocation ToolInvocation,
) (ToolExecutionResult, error) {
	if invocation.Name == ToolNameSimulateFailure {
		return ToolExecutionResult{}, simulatedToolFailureError{}
	}
	executionContext, cancel := newToolExecutionContext(ctx, definition.AttemptTimeout)
	defer cancel()
	return flow.tools.Execute(executionContext, invocation)
}

func newToolExecutionContext(
	ctx context.Context,
	attemptTimeout time.Duration,
) (context.Context, context.CancelFunc) {
	if attemptTimeout == 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, attemptTimeout)
}

func (flow *Flow) beginUserTurn(ctx dex.Context, message UserMessage) (Sequence, error) {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return 0, err
	}
	plan, err := getAgentPlan(ctx)
	if err != nil {
		return 0, err
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
		return 0, setErr
	}
	return flow.appendMessage(ctx, AgentMessage{
		Role:                 MessageRoleUser,
		Content:              message.Content,
		ToolCalls:            []ToolCall{},
		ProviderContextItems: []ProviderContextItem{},
	})
}

func (flow *Flow) beginSteeredTurn(ctx dex.Context, messages []PendingUserMessage) error {
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
	if err := deletePendingToolRecovery(ctx); err != nil {
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
		if _, err := flow.beginUserTurn(ctx, message.Value); err != nil {
			return err
		}
	}
	if err := flow.writeInputConsumption(ctx, nil, messages, nil); err != nil {
		return err
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

func (flow *Flow) writeSnapshotRequired(ctx dex.Context) error {
	return flow.writeActivity(ctx, AgentEvent{
		Kind:    EventKindSnapshotRequired,
		Message: "Durable interaction state changed.",
	})
}

func (flow *Flow) writeInputConsumption(
	ctx dex.Context,
	queued []PendingUserMessage,
	steered []PendingUserMessage,
	planExecutionRevision *PlanRevision,
) error {
	if len(queued) == 0 && len(steered) == 0 && planExecutionRevision == nil {
		return nil
	}
	consumption := InputConsumption{
		QueuedMessageIDs:      pendingMessageIDs(queued),
		SteeredMessageIDs:     pendingMessageIDs(steered),
		PlanExecutionRevision: planExecutionRevision,
	}
	messages := make([]string, 0, 3)
	if len(queued) > 0 {
		messages = append(messages, consumedMessagesDescription(len(queued), "queued"))
	}
	if len(steered) > 0 {
		messages = append(messages, consumedMessagesDescription(len(steered), "steered"))
	}
	if planExecutionRevision != nil {
		messages = append(messages, fmt.Sprintf(
			"Consumed plan execution request for revision %d.",
			*planExecutionRevision,
		))
	}
	return flow.writeActivity(ctx, AgentEvent{
		Kind:             EventKindInputConsumed,
		Message:          strings.Join(messages, " "),
		InputConsumption: &consumption,
	})
}

func pendingMessageIDs(messages []PendingUserMessage) []MessageID {
	ids := make([]MessageID, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.MessageID)
	}
	return ids
}

func consumedMessagesDescription(count int, queue string) string {
	noun := "messages"
	if count == 1 {
		noun = "message"
	}
	return fmt.Sprintf("Consumed %d %s user %s.", count, queue, noun)
}

func incrementWaitingInputRound(ctx dex.Context) error {
	round, err := waitingInputRoundAttribute.Get(ctx)
	if err != nil {
		return err
	}
	next, err := nextWaitingInputRound(round)
	if err != nil {
		return err
	}
	return waitingInputRoundAttribute.Set(ctx, next)
}

func nextWaitingInputRound(round WaitingInputRound) (WaitingInputRound, error) {
	if round < 0 {
		return 0, errors.New("waiting input round must not be negative")
	}
	if round >= MaximumWaitingInputRound {
		return 0, fmt.Errorf("waiting input round reached the JavaScript safe integer limit %d", MaximumWaitingInputRound)
	}
	return round + 1, nil
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

func getPendingToolRecovery(ctx dex.Context) (*PendingToolRecovery, error) {
	value, err := pendingToolRecoveryAttribute.Get(ctx)
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

func deletePendingToolRecovery(ctx dex.Context) error {
	value, err := getPendingToolRecovery(ctx)
	if err != nil || value == nil {
		return err
	}
	return pendingToolRecoveryAttribute.Delete(ctx)
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

func validateToolRecoveryResolution(
	pending PendingToolRecovery,
	request ResolveToolRecoveryRequest,
) error {
	if err := request.Resolution.Validate(); err != nil {
		return err
	}
	if request.Resolution == ToolRecoveryResolutionStop {
		if len(request.Decisions) != 0 {
			return errors.New("stop recovery must not include decisions")
		}
		return nil
	}
	if len(request.Decisions) != len(pending.Calls) {
		return errors.New("resume recovery must decide every pending call")
	}
	pendingIDs := make(map[CallID]struct{}, len(pending.Calls))
	for _, call := range pending.Calls {
		pendingIDs[call.CallID] = struct{}{}
	}
	seen := make(map[CallID]struct{}, len(request.Decisions))
	for _, decision := range request.Decisions {
		if err := decision.Action.Validate(); err != nil {
			return err
		}
		if _, found := pendingIDs[decision.CallID]; !found {
			return fmt.Errorf("tool call %q is not pending recovery", decision.CallID)
		}
		if _, duplicate := seen[decision.CallID]; duplicate {
			return fmt.Errorf("tool call %q has duplicate recovery decisions", decision.CallID)
		}
		seen[decision.CallID] = struct{}{}
	}
	return nil
}

func isValidToolRecoveryResolution(pending PendingToolRecovery, request ResolveToolRecoveryRequest) bool {
	return validateToolRecoveryResolution(pending, request) == nil
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
	continueDurableWait       continuation = "durable_wait"

	stepTypeInit                  stepType = "Init"
	stepTypeAwaitUser             stepType = "AwaitUser"
	stepTypeAnsweredInput         stepType = "AnsweredInput"
	stepTypeCompactContext        stepType = "CompactContext"
	stepTypeCallModel             stepType = "CallModel"
	stepTypeCheckSteered          stepType = "CheckSteered"
	stepTypeRouteTool             stepType = "RouteTool"
	stepTypeAwaitApproval         stepType = "AwaitToolApproval"
	stepTypeExecuteTool           stepType = "ExecuteTool"
	stepTypeRecoverTool           stepType = "RecoverToolExecution"
	stepTypeExecuteParallel       stepType = "ExecuteParallelTool"
	stepTypeRecoverParallel       stepType = "RecoverParallelToolExecution"
	stepTypeAwaitParallel         stepType = "AwaitParallelToolResults"
	stepTypePrepareManualRecovery stepType = "PrepareManualToolRecovery"
	stepTypeAwaitManualRecovery   stepType = "AwaitManualToolRecovery"
	stepTypeDurableWait           stepType = "DurableWait"

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

type parallelToolExecutionInput struct {
	ResultInstance        string                    `json:"result_instance"`
	Index                 int                       `json:"index"`
	Call                  ToolCall                  `json:"call"`
	RetryExhaustionPolicy ToolRetryExhaustionPolicy `json:"retry_exhaustion_policy"`
}

type parallelToolResult struct {
	Index                 int                       `json:"index"`
	Call                  ToolCall                  `json:"call"`
	Result                ToolExecutionResult       `json:"result"`
	ErrorType             string                    `json:"error_type,omitempty"`
	RetryExhaustionPolicy ToolRetryExhaustionPolicy `json:"retry_exhaustion_policy"`
}

type toolBatchRecord struct {
	Index                 int                       `json:"index"`
	Call                  ToolCall                  `json:"call"`
	Result                *ToolExecutionResult      `json:"result,omitempty"`
	HasResult             bool                      `json:"has_result"`
	ErrorType             string                    `json:"error_type,omitempty"`
	RetryExhaustionPolicy ToolRetryExhaustionPolicy `json:"retry_exhaustion_policy"`
}

type toolBatchState struct {
	BatchID          string            `json:"batch_id"`
	FirstIndex       int               `json:"first_index"`
	Records          []toolBatchRecord `json:"records"`
	RecoveryRevision int               `json:"recovery_revision"`
}

type awaitParallelToolResultsInput struct {
	Batch          toolBatchState `json:"batch"`
	ResultInstance string         `json:"result_instance"`
	ExpectedCount  int            `json:"expected_count"`
}

var (
	defaultStepOptions = &dex.StepOptions{
		WaitForMethodTimeout: time.Minute,
		ExecuteMethodTimeout: time.Minute,
	}
	messageMutationStepOptions = &dex.StepOptions{
		WaitForMethodTimeout:     time.Minute,
		ExecuteMethodTimeout:     time.Minute,
		ExecuteLoadAttributeMaps: []dex.AttributeDef{currentMessagesAttribute},
		ExecuteLockAttributes: []dex.AttributeLock{
			dex.LockAttribute(pendingUserInputAttribute),
			dex.LockAttribute(pendingApprovalAttribute),
			dex.LockAttribute(pendingToolRecoveryAttribute),
		},
	}
	awaitUserStepOptions = &dex.StepOptions{
		WaitForMethodTimeout: time.Minute,
		ExecuteMethodTimeout: time.Minute,
		ExecuteLoadAttributeMaps: []dex.AttributeDef{
			currentMessagesAttribute,
		},
		ExecuteLockAttributes: []dex.AttributeLock{
			dex.LockAttribute(pendingUserInputAttribute),
			dex.LockAttribute(pendingApprovalAttribute),
			dex.LockAttribute(pendingToolRecoveryAttribute),
		},
	}
	messageContextStepOptions = &dex.StepOptions{
		WaitForMethodTimeout: time.Minute,
		ExecuteMethodTimeout: time.Minute,
		ExecuteLoadAttributeMaps: []dex.AttributeDef{
			currentMessagesAttribute,
			archivedMessagesAttribute,
		},
		ExecuteLockAttributes: []dex.AttributeLock{
			dex.LockAttribute(pendingUserInputAttribute),
			dex.LockAttribute(pendingToolRecoveryAttribute),
		},
	}
	modelStepOptions = &dex.StepOptions{
		ExecuteMethodTimeout:     10 * time.Minute,
		HeartbeatTimeout:         time.Minute,
		ExecuteDurability:        dex.StepDurabilitySync,
		ExecuteLoadAttributeMaps: messageContextStepOptions.ExecuteLoadAttributeMaps,
		ExecuteRetry: &dex.RetryPolicy{
			MaximumAttempts: 3,
			TotalDuration:   30 * time.Minute,
		},
	}
	toolStepOptions = &dex.StepOptions{
		ExecuteMethodTimeout:     2 * time.Hour,
		HeartbeatTimeout:         time.Minute,
		ExecuteLoadAttributeMaps: messageMutationStepOptions.ExecuteLoadAttributeMaps,
		ExecuteRetry: &dex.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	manualToolRecoveryStepOptions = &dex.StepOptions{
		WaitForMethodTimeout:     time.Minute,
		ExecuteMethodTimeout:     time.Minute,
		ExecuteLoadAttributeMaps: messageMutationStepOptions.ExecuteLoadAttributeMaps,
		ExecuteLockAttributes: []dex.AttributeLock{
			dex.LockAttribute(pendingToolRecoveryAttribute),
		},
	}
)

type initStep struct {
	dex.StepDefaultsNoWaitFor[AgentConfig]
	flow *Flow
}

var _ dex.Step[AgentConfig] = initStep{}

func (initStep) GetStepType() string { return string(stepTypeInit) }

func (initStep) GetStepOptions() *dex.StepOptions { return defaultStepOptions }

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
	if err := waitingInputRoundAttribute.Set(ctx, 0); err != nil {
		return nil, err
	}
	return dex.GoTo(awaitUserStep{flow: step.flow}, nil), nil
}

type awaitUserStep struct {
	dex.StepDefaults
	flow *Flow
}

var _ dex.Step[dex.None] = awaitUserStep{}

func (awaitUserStep) GetStepType() string { return string(stepTypeAwaitUser) }

func (awaitUserStep) GetStepOptions() *dex.StepOptions { return awaitUserStepOptions }

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
	if answeredUserInputsChannel.Size(ctx) > 0 {
		return dex.Until(answeredUserInputsChannel.ForOne()), nil
	}
	if pendingInput != nil {
		if err := incrementWaitingInputRound(ctx); err != nil {
			return nil, err
		}
		return dex.Until(answeredUserInputsChannel.ForOne()), nil
	}
	if plan != nil && plan.Status != PlanStatusCompleted {
		planKey := planRevisionKey(plan.Revision)
		if steeredUserMessagesChannel.Size(ctx) == 0 &&
			queuedUserMessagesChannel.Size(ctx) == 0 &&
			planExecutionsChannel.Size(ctx, planKey) == 0 {
			if err := incrementWaitingInputRound(ctx); err != nil {
				return nil, err
			}
		}
		return dex.AnyOf(
			steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
			queuedUserMessagesChannel.ForOne(),
			planExecutionsChannel.ForOne(planKey),
		), nil
	}
	if steeredUserMessagesChannel.Size(ctx) == 0 && queuedUserMessagesChannel.Size(ctx) == 0 {
		if err := incrementWaitingInputRound(ctx); err != nil {
			return nil, err
		}
	}
	return dex.AnyOf(
		steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
		queuedUserMessagesChannel.ForOne(),
	), nil
}

func (step awaitUserStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	answeredInputs, err := answeredUserInputsChannel.GetConditionResults(ctx)
	if err != nil {
		return nil, err
	}
	if len(answeredInputs) > 0 {
		answeredInput := answeredInputs[0]
		sequence, beginErr := step.flow.beginUserTurn(ctx, answeredInput.Message)
		if beginErr != nil {
			return nil, beginErr
		}
		if activityErr := step.flow.writeActivity(ctx, AgentEvent{
			Kind:            EventKindUserInputAnswered,
			Message:         answeredQuestionsDescription(answeredInput.QuestionCount),
			CallID:          &answeredInput.CallID,
			MessageSequence: &sequence,
		}); activityErr != nil {
			return nil, activityErr
		}
		return dex.GoTo(answeredInputStep{flow: step.flow}, nil), nil
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
		if _, beginErr := step.flow.beginUserTurn(ctx, queued[0].Value); beginErr != nil {
			return nil, beginErr
		}
		if consumptionErr := step.flow.writeInputConsumption(ctx, []PendingUserMessage{queued[0]}, nil, nil); consumptionErr != nil {
			return nil, consumptionErr
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
	if len(executions) > 0 {
		revision := executions[0].Revision
		if consumptionErr := step.flow.writeInputConsumption(ctx, nil, nil, &revision); consumptionErr != nil {
			return nil, consumptionErr
		}
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

func answeredQuestionsDescription(questionCount int) string {
	noun := "questions"
	if questionCount == 1 {
		noun = "question"
	}
	return fmt.Sprintf("Answered %d %s.", questionCount, noun)
}

type answeredInputStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = answeredInputStep{}

func (answeredInputStep) GetStepType() string { return string(stepTypeAnsweredInput) }

func (answeredInputStep) GetStepOptions() *dex.StepOptions { return messageContextStepOptions }

func (step answeredInputStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
	cutoff, err := step.flow.pendingCompactionCutoff(ctx)
	if err != nil {
		return nil, err
	}
	if cutoff != nil {
		return dex.GoTo(compactContextStep{flow: step.flow}, *cutoff), nil
	}
	return dex.GoTo(callModelStep{flow: step.flow}, nil), nil
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
		Config:         config,
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
		options, err := step.flow.currentSerialToolStepOptions(ctx)
		if err != nil {
			return nil, err
		}
		return dex.GoTo(
			executeToolStep{flow: step.flow},
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
	call, callErr := step.flow.currentToolCall(ctx)
	if callErr != nil {
		return nil, callErr
	}
	config, configErr := agentConfigAttribute.Get(ctx)
	if configErr != nil {
		return nil, configErr
	}
	state, stateErr := agentStateAttribute.Get(ctx)
	if stateErr != nil {
		return nil, stateErr
	}
	definition, definitionErr := step.flow.invocationToolDefinition(config, state, call.Name)
	if definitionErr != nil {
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
			state.PlanningRequiresWrite = false
			state.PlanningAllowsWrite = true
			if err := agentStateAttribute.Set(ctx, state); err != nil {
				return nil, err
			}
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
	movements, isParallel, err := step.flow.parallelToolMovements(config, state)
	if err != nil {
		return nil, err
	}
	if isParallel {
		if err := step.flow.updateStatus(ctx, AgentStatusExecutingTool); err != nil {
			return nil, err
		}
		return dex.GoToMany(movements...), nil
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
	return dex.GoTo(checkSteeredStep{flow: step.flow}, continueExecuteTool), nil
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
	if err := step.flow.writeSnapshotRequired(ctx); err != nil {
		return nil, err
	}
	return dex.AnyOf(
		steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
		toolApprovalsChannel.ForOne(string(call.ID)),
	), nil
}

func (step awaitToolApprovalStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
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
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueExecuteTool), nil
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

func (flow *Flow) finishToolExecution(
	ctx dex.Context,
	call ToolCall,
	result ToolExecutionResult,
) (continuation, error) {
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return "", err
	}
	resultCopy := result
	return flow.finishToolBatch(ctx, toolBatchState{
		FirstIndex: state.PendingToolIndex,
		Records: []toolBatchRecord{{
			Index:     state.PendingToolIndex,
			Call:      call,
			Result:    &resultCopy,
			HasResult: true,
		}},
	})
}

func (flow *Flow) appendToolBatchResults(ctx dex.Context, batch toolBatchState) error {
	records := append([]toolBatchRecord(nil), batch.Records...)
	slices.SortFunc(records, func(left toolBatchRecord, right toolBatchRecord) int {
		return left.Index - right.Index
	})
	for offset, record := range records {
		if !record.HasResult || record.Result == nil || record.Index != batch.FirstIndex+offset {
			return errors.New("tool batch results are incomplete or out of order")
		}
		if err := record.Result.Outcome.Validate(); err != nil {
			return fmt.Errorf("tool %q outcome: %w", record.Call.Name, err)
		}
		if err := flow.appendToolResult(ctx, record.Call, *record.Result); err != nil {
			return err
		}
		if err := flow.writeToolTerminalActivity(ctx, record.Call, *record.Result); err != nil {
			return err
		}
	}
	return nil
}

func (flow *Flow) writeToolTerminalActivity(
	ctx dex.Context,
	call ToolCall,
	result ToolExecutionResult,
) error {
	callID := call.ID
	toolName := call.Name
	kind := EventKindToolCompleted
	message := fmt.Sprintf("Completed %s.", call.Name)
	if result.Outcome != ToolOutcomeSucceeded {
		kind = EventKindToolFailed
		message = fmt.Sprintf("%s returned a known failure.", call.Name)
		if result.Outcome == ToolOutcomeUnknown {
			message = fmt.Sprintf("%s completed with an unknown outcome.", call.Name)
		}
	}
	return flow.writeActivity(ctx, AgentEvent{
		Kind:     kind,
		Message:  message,
		CallID:   &callID,
		ToolName: &toolName,
	})
}

func (flow *Flow) finishToolBatch(ctx dex.Context, batch toolBatchState) (continuation, error) {
	if err := flow.appendToolBatchResults(ctx, batch); err != nil {
		return "", err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return "", err
	}
	if state.PendingToolIndex != batch.FirstIndex {
		return "", errors.New("tool batch no longer matches the pending cursor")
	}
	state.PendingToolIndex += len(batch.Records)
	if state.PendingToolIndex < len(state.PendingToolCalls) {
		if err := agentStateAttribute.Set(ctx, state); err != nil {
			return "", err
		}
		return continueRouteTool, nil
	}
	state.PendingToolCalls = []ToolCall{}
	state.PendingToolIndex = 0
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return "", err
	}
	return continueCompactContext, nil
}

// executeToolStep has no registered StepOptions. checkSteeredStep resolves the
// current definition and attaches complete serial options to every movement.
type executeToolStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = executeToolStep{}

func (executeToolStep) GetStepType() string { return string(stepTypeExecuteTool) }

func (step executeToolStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
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
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	definition, err := step.flow.invocationToolDefinition(config, state, call.Name)
	if err != nil {
		return nil, err
	}
	runtimeMetadata, err := agentRuntimeMetadataAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		runtimeMetadata = MustJSONObject(`{}`)
	} else if err != nil {
		return nil, err
	}
	progress := toolProgress{ctx: ctx, flow: step.flow, call: call}
	if writeErr := progress.write(fmt.Sprintf("Calling %s (attempt %d).", call.Name, ctx.Attempt())); writeErr != nil {
		return nil, writeErr
	}
	result, err := step.flow.executeTool(ctx, definition, ToolInvocation{
		FlowID:          FlowID(ctx.FlowID()),
		RuntimeMetadata: runtimeMetadata,
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
	if result.Outcome == ToolOutcomeUnknown &&
		definition.RetryExhaustionPolicy.Effective() == ToolRetryExhaustionPolicyManualRecovery {
		return dex.GoTo(prepareManualToolRecoveryStep{flow: step.flow}, nil), nil
	}
	if result.Outcome == ToolOutcomeUnknown {
		return dex.GoTo(recoverToolExecutionStep{flow: step.flow}, nil), nil
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
	errorType := "tool_reported_unknown"
	if failure := ctx.RecoveryError(); failure != nil {
		errorType = failure.ErrorType
	}
	result, err := encodeToolResult(toolResultPayload{
		Status:    toolResultStatusFailed,
		Outcome:   ToolOutcomeUnknown,
		ErrorType: errorType,
	}, ToolOutcomeUnknown, true)
	if err != nil {
		return nil, err
	}
	next, finishErr := step.flow.finishToolExecution(ctx, call, result)
	if finishErr != nil {
		return nil, finishErr
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, next), nil
}

// executeParallelToolStep has no registered StepOptions. Initial fan-out and
// manual retries attach complete branch options to every movement.
type executeParallelToolStep struct {
	dex.StepDefaultsNoWaitFor[parallelToolExecutionInput]
	flow *Flow
}

var _ dex.Step[parallelToolExecutionInput] = executeParallelToolStep{}

func (executeParallelToolStep) GetStepType() string { return string(stepTypeExecuteParallel) }

func (step executeParallelToolStep) Execute(
	ctx dex.Context,
	input parallelToolExecutionInput,
) (*dex.StepDecision, error) {
	config, configErr := agentConfigAttribute.Get(ctx)
	if configErr != nil {
		return nil, configErr
	}
	state, stateErr := agentStateAttribute.Get(ctx)
	if stateErr != nil {
		return nil, stateErr
	}
	definition, definitionErr := step.flow.invocationToolDefinition(config, state, input.Call.Name)
	if definitionErr != nil {
		return nil, definitionErr
	}
	runtimeMetadata, metadataErr := agentRuntimeMetadataAttribute.Get(ctx)
	if isAttributeNotFound(metadataErr) {
		runtimeMetadata = MustJSONObject(`{}`)
	} else if metadataErr != nil {
		return nil, metadataErr
	}
	progress := toolProgress{ctx: ctx, flow: step.flow, call: input.Call}
	if progressErr := progress.write(fmt.Sprintf("Calling %s (attempt %d).", input.Call.Name, ctx.Attempt())); progressErr != nil {
		return nil, progressErr
	}
	result, executeErr := step.flow.executeTool(ctx, definition, ToolInvocation{
		FlowID:          FlowID(ctx.FlowID()),
		RuntimeMetadata: runtimeMetadata,
		Name:            input.Call.Name,
		Arguments:       input.Call.Arguments,
		EnabledServers:  config.EnabledMCPServers,
		WriteProgress:   progress.write,
		CallID:          input.Call.ID,
		Attempt:         ctx.Attempt(),
		FirstAttemptAt:  ctx.FirstAttemptAt(),
	})
	if executeErr != nil {
		return nil, fmt.Errorf("execute tool %q: %w", input.Call.Name, executeErr)
	}
	if err := result.Outcome.Validate(); err != nil {
		return nil, fmt.Errorf("tool %q outcome: %w", input.Call.Name, err)
	}
	if result.Outcome == ToolOutcomeUnknown {
		return dex.GoTo(recoverParallelToolExecutionStep{flow: step.flow}, input), nil
	}
	if err := parallelToolResultsChannel.Publish(ctx, input.ResultInstance, parallelToolResult{
		Index:                 input.Index,
		Call:                  input.Call,
		Result:                result,
		ErrorType:             "",
		RetryExhaustionPolicy: input.RetryExhaustionPolicy.Effective(),
	}); err != nil {
		return nil, err
	}
	return dex.DeadEnd(), nil
}

type recoverParallelToolExecutionStep struct {
	dex.StepDefaultsNoWaitFor[parallelToolExecutionInput]
	flow *Flow
}

var _ dex.Step[parallelToolExecutionInput] = recoverParallelToolExecutionStep{}

func (recoverParallelToolExecutionStep) GetStepType() string {
	return string(stepTypeRecoverParallel)
}

func (recoverParallelToolExecutionStep) GetStepOptions() *dex.StepOptions {
	return defaultStepOptions
}

func (recoverParallelToolExecutionStep) Execute(
	ctx dex.Context,
	input parallelToolExecutionInput,
) (*dex.StepDecision, error) {
	errorType := "tool_reported_unknown"
	if failure := ctx.RecoveryError(); failure != nil {
		errorType = failure.ErrorType
	}
	result, err := unknownToolResult(errorType)
	if err != nil {
		return nil, err
	}
	if err := parallelToolResultsChannel.Publish(ctx, input.ResultInstance, parallelToolResult{
		Index:                 input.Index,
		Call:                  input.Call,
		Result:                result,
		ErrorType:             errorType,
		RetryExhaustionPolicy: input.RetryExhaustionPolicy.Effective(),
	}); err != nil {
		return nil, err
	}
	return dex.DeadEnd(), nil
}

type awaitParallelToolResultsStep struct {
	dex.StepDefaults
	flow *Flow
}

var _ dex.Step[awaitParallelToolResultsInput] = awaitParallelToolResultsStep{}

func (awaitParallelToolResultsStep) GetStepType() string { return string(stepTypeAwaitParallel) }

func (awaitParallelToolResultsStep) GetStepOptions() *dex.StepOptions {
	return messageMutationStepOptions
}

func (awaitParallelToolResultsStep) WaitFor(
	_ dex.Context,
	input awaitParallelToolResultsInput,
) (*dex.Wait, error) {
	return dex.Until(parallelToolResultsChannel.ForN(input.ResultInstance, input.ExpectedCount)), nil
}

func (step awaitParallelToolResultsStep) Execute(
	ctx dex.Context,
	input awaitParallelToolResultsInput,
) (*dex.StepDecision, error) {
	results, err := parallelToolResultsChannel.GetConditionResults(ctx, input.ResultInstance)
	if err != nil {
		return nil, err
	}
	if len(results) != input.ExpectedCount {
		return nil, errors.New("parallel tool wait completed without every result")
	}
	batch := input.Batch
	seen := make(map[int]struct{}, len(results))
	for _, result := range results {
		if _, duplicate := seen[result.Index]; duplicate {
			return nil, fmt.Errorf("parallel tool result index %d is duplicated", result.Index)
		}
		seen[result.Index] = struct{}{}
		found := false
		for index := range batch.Records {
			if batch.Records[index].Index != result.Index || batch.Records[index].Call.ID != result.Call.ID {
				continue
			}
			resultCopy := result.Result
			batch.Records[index].Result = &resultCopy
			batch.Records[index].HasResult = true
			batch.Records[index].ErrorType = result.ErrorType
			batch.Records[index].RetryExhaustionPolicy = result.RetryExhaustionPolicy.Effective()
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("unexpected parallel tool result for call %q", result.Call.ID)
		}
	}
	if batchNeedsManualRecovery(batch) {
		if batch.RecoveryRevision == 0 {
			batch.RecoveryRevision = 1
		}
		return dex.GoTo(awaitManualToolRecoveryStep{flow: step.flow}, batch), nil
	}
	next, err := step.flow.finishToolBatch(ctx, batch)
	if err != nil {
		return nil, err
	}
	return dex.GoTo(checkSteeredStep{flow: step.flow}, next), nil
}

type prepareManualToolRecoveryStep struct {
	dex.StepDefaultsNoWaitFor[dex.None]
	flow *Flow
}

var _ dex.Step[dex.None] = prepareManualToolRecoveryStep{}

func (prepareManualToolRecoveryStep) GetStepType() string {
	return string(stepTypePrepareManualRecovery)
}

func (prepareManualToolRecoveryStep) GetStepOptions() *dex.StepOptions {
	return manualToolRecoveryStepOptions
}

func (step prepareManualToolRecoveryStep) Execute(
	ctx dex.Context,
	_ dex.None,
) (*dex.StepDecision, error) {
	errorType := "tool_reported_unknown"
	if failure := ctx.RecoveryError(); failure != nil {
		errorType = failure.ErrorType
	}
	call, err := step.flow.currentToolCall(ctx)
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	result, err := unknownToolResult(errorType)
	if err != nil {
		return nil, err
	}
	resultCopy := result
	batch := toolBatchState{
		BatchID:          fmt.Sprintf("tool-batch-%d-%d", state.LastSequence, state.PendingToolIndex),
		FirstIndex:       state.PendingToolIndex,
		RecoveryRevision: 1,
		Records: []toolBatchRecord{{
			Index:                 state.PendingToolIndex,
			Call:                  call,
			Result:                &resultCopy,
			HasResult:             true,
			ErrorType:             errorType,
			RetryExhaustionPolicy: ToolRetryExhaustionPolicyManualRecovery,
		}},
	}
	return dex.GoTo(awaitManualToolRecoveryStep{flow: step.flow}, batch), nil
}

type awaitManualToolRecoveryStep struct {
	dex.StepDefaults
	flow *Flow
}

var _ dex.Step[toolBatchState] = awaitManualToolRecoveryStep{}

func (awaitManualToolRecoveryStep) GetStepType() string {
	return string(stepTypeAwaitManualRecovery)
}

func (awaitManualToolRecoveryStep) GetStepOptions() *dex.StepOptions {
	return manualToolRecoveryStepOptions
}

func (step awaitManualToolRecoveryStep) WaitFor(
	ctx dex.Context,
	batch toolBatchState,
) (*dex.Wait, error) {
	pending := pendingToolRecovery(batch)
	if len(pending.Calls) == 0 {
		return nil, errors.New("manual tool recovery has no unresolved calls")
	}
	if err := pendingToolRecoveryAttribute.Set(ctx, pending); err != nil {
		return nil, err
	}
	if err := step.flow.updateStatus(ctx, AgentStatusWaitingForToolRecovery); err != nil {
		return nil, err
	}
	if err := step.flow.writeActivity(ctx, AgentEvent{
		Kind:    EventKindToolRecoveryRequired,
		Message: fmt.Sprintf("Manual recovery is required for %d tool call(s).", len(pending.Calls)),
	}); err != nil {
		return nil, err
	}
	if err := step.flow.writeSnapshotRequired(ctx); err != nil {
		return nil, err
	}
	return dex.AnyOf(
		steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
		toolRecoveryDecisionsChannel.ForOne(string(pending.RecoveryID)),
	), nil
}

func (step awaitManualToolRecoveryStep) Execute(
	ctx dex.Context,
	batch toolBatchState,
) (*dex.StepDecision, error) {
	steered, conditionErr := steeredUserMessagesChannel.GetConditionResults(ctx)
	if conditionErr != nil {
		return nil, conditionErr
	}
	if len(steered) > 0 {
		if err := deletePendingToolRecovery(ctx); err != nil {
			return nil, err
		}
		if err := step.flow.appendToolBatchResults(ctx, batch); err != nil {
			return nil, err
		}
		state, err := agentStateAttribute.Get(ctx)
		if err != nil {
			return nil, err
		}
		state.PendingToolIndex = batch.FirstIndex + len(batch.Records)
		if err := agentStateAttribute.Set(ctx, state); err != nil {
			return nil, err
		}
		if err := step.flow.beginSteeredTurn(ctx, steered); err != nil {
			return nil, err
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, continueCompactContext), nil
	}
	recoveryID := recoveryIDFor(batch)
	requests, decisionErr := toolRecoveryDecisionsChannel.GetConditionResults(ctx, string(recoveryID))
	if decisionErr != nil {
		return nil, decisionErr
	}
	if len(requests) != 1 {
		return nil, errors.New("manual recovery wait completed without one decision")
	}
	request := requests[0]
	if err := validateToolRecoveryResolution(pendingToolRecovery(batch), request); err != nil {
		return nil, err
	}
	if err := step.flow.writeActivity(ctx, AgentEvent{
		Kind:    EventKindToolRecoveryResolved,
		Message: fmt.Sprintf("Resolved manual tool recovery %s.", request.Resolution),
	}); err != nil {
		return nil, err
	}
	if request.Resolution == ToolRecoveryResolutionStop {
		if err := step.flow.stopToolSequenceAfterBatch(ctx, batch); err != nil {
			return nil, err
		}
		return dex.GoTo(awaitUserStep{flow: step.flow}, nil), nil
	}
	decisions := make(map[CallID]ToolRecoveryAction, len(request.Decisions))
	for _, decision := range request.Decisions {
		decisions[decision.CallID] = decision.Action
	}
	retryRecords := make([]toolBatchRecord, 0, len(request.Decisions))
	for index := range batch.Records {
		record := &batch.Records[index]
		if !recordNeedsManualRecovery(*record) {
			continue
		}
		if decisions[record.Call.ID] == ToolRecoveryActionRetry {
			record.HasResult = false
			record.Result = nil
			record.ErrorType = ""
			retryRecords = append(retryRecords, *record)
		} else {
			record.RetryExhaustionPolicy = ToolRetryExhaustionPolicyContinueWithUnknown
		}
	}
	if len(retryRecords) == 0 {
		next, err := step.flow.finishToolBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		return dex.GoTo(checkSteeredStep{flow: step.flow}, next), nil
	}
	batch.RecoveryRevision++
	resultInstance := fmt.Sprintf("%s-retry-%d", batch.BatchID, batch.RecoveryRevision)
	config, err := agentConfigAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return nil, err
	}
	movements := make([]dex.StepMovement, 0, len(retryRecords)+1)
	movements = append(movements, dex.MovementOf(
		awaitParallelToolResultsStep{flow: step.flow},
		awaitParallelToolResultsInput{
			Batch:          batch,
			ResultInstance: resultInstance,
			ExpectedCount:  len(retryRecords),
		},
	))
	for _, record := range retryRecords {
		definition, err := step.flow.invocationToolDefinition(config, state, record.Call.Name)
		if err != nil {
			return nil, err
		}
		movements = append(movements, dex.MovementOf(
			executeParallelToolStep{flow: step.flow},
			parallelToolExecutionInput{
				ResultInstance:        resultInstance,
				Index:                 record.Index,
				Call:                  record.Call,
				RetryExhaustionPolicy: ToolRetryExhaustionPolicyManualRecovery,
			},
			dex.WithStepOptions(step.flow.parallelToolStepOptions(definition)),
		))
	}
	if err := step.flow.updateStatus(ctx, AgentStatusExecutingTool); err != nil {
		return nil, err
	}
	return dex.GoToMany(movements...), nil
}

func unknownToolResult(errorType string) (ToolExecutionResult, error) {
	return encodeToolResult(toolResultPayload{
		Status:    toolResultStatusFailed,
		Outcome:   ToolOutcomeUnknown,
		ErrorType: errorType,
	}, ToolOutcomeUnknown, true)
}

func recoveryIDFor(batch toolBatchState) RecoveryID {
	return RecoveryID(fmt.Sprintf("%s-recovery-%d", batch.BatchID, batch.RecoveryRevision))
}

func recordNeedsManualRecovery(record toolBatchRecord) bool {
	return record.HasResult &&
		record.Result != nil &&
		record.Result.Outcome == ToolOutcomeUnknown &&
		record.RetryExhaustionPolicy.Effective() == ToolRetryExhaustionPolicyManualRecovery
}

func batchNeedsManualRecovery(batch toolBatchState) bool {
	for _, record := range batch.Records {
		if recordNeedsManualRecovery(record) {
			return true
		}
	}
	return false
}

func pendingToolRecovery(batch toolBatchState) PendingToolRecovery {
	pending := PendingToolRecovery{RecoveryID: recoveryIDFor(batch), Calls: []PendingToolRecoveryCall{}}
	for _, record := range batch.Records {
		if !recordNeedsManualRecovery(record) {
			continue
		}
		pending.Calls = append(pending.Calls, PendingToolRecoveryCall{
			CallID:    record.Call.ID,
			ToolName:  record.Call.Name,
			Arguments: record.Call.Arguments,
			ErrorType: record.ErrorType,
		})
	}
	return pending
}

func (flow *Flow) stopToolSequenceAfterBatch(ctx dex.Context, batch toolBatchState) error {
	if err := flow.appendToolBatchResults(ctx, batch); err != nil {
		return err
	}
	state, err := agentStateAttribute.Get(ctx)
	if err != nil {
		return err
	}
	start := batch.FirstIndex + len(batch.Records)
	if start < 0 || start > len(state.PendingToolCalls) {
		return errors.New("manual recovery batch exceeds pending tool calls")
	}
	remaining := append([]ToolCall(nil), state.PendingToolCalls[start:]...)
	state.PendingToolCalls = []ToolCall{}
	state.PendingToolIndex = 0
	if err := agentStateAttribute.Set(ctx, state); err != nil {
		return err
	}
	for _, call := range remaining {
		result, err := encodeToolResult(toolResultPayload{
			Status: toolResultStatusInterrupted,
			Error:  toolErrorStoppedByRecovery,
		}, ToolOutcomeKnownFailure, true)
		if err != nil {
			return err
		}
		if err := flow.appendToolResult(ctx, call, result); err != nil {
			return err
		}
		if err := flow.writeToolTerminalActivity(ctx, call, result); err != nil {
			return err
		}
	}
	return nil
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
	if err := step.flow.writeSnapshotRequired(ctx); err != nil {
		return nil, err
	}
	return dex.AnyOf(
		dex.Timer(time.Duration(timer.DurationSeconds)*time.Second),
		steeredUserMessagesChannel.AtLeastAtMost(1, maximumSteeringMessageCount),
	), nil
}

func (step durableWaitStep) Execute(ctx dex.Context, _ dex.None) (*dex.StepDecision, error) {
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

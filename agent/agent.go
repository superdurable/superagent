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

// Package agent exposes the embeddable SuperAgent Flow and its typed application client.
//
// Applications provide model and tool implementations, register the returned Flow with
// Dex, and use Client for durable commands, Snapshots, history, and best-effort events.
// Durable resource descriptors and the Agent state-machine implementation remain private.
package agent

import (
	"github.com/superdurable/dex/sdk-go/dex"
	agentinternal "github.com/superdurable/superagent/internal/agent"
)

const (
	// DefaultModel uses the deterministic local provider.
	DefaultModel = agentinternal.DefaultModel
	// DefaultContextTokens bounds reconstructed model context.
	DefaultContextTokens = agentinternal.DefaultContextTokens
	// DefaultMessageRetention bounds retained summarized messages.
	DefaultMessageRetention = agentinternal.DefaultMessageRetention
	// MaximumUserMessageContentBytes bounds one user-message body.
	MaximumUserMessageContentBytes = agentinternal.MaximumUserMessageContentBytes
	// MaximumRuntimeMetadataBytes bounds trusted metadata persisted for one Agent.
	MaximumRuntimeMetadataBytes = agentinternal.MaximumRuntimeMetadataBytes
	// MaximumRecentEventLimit bounds one best-effort Stream recovery read.
	MaximumRecentEventLimit = agentinternal.MaximumRecentEventLimit
	// DefaultSystemPrompt is used when callers omit a custom prompt.
	DefaultSystemPrompt = agentinternal.DefaultSystemPrompt

	// ToolNameWriteTodos atomically replaces the durable plan.
	ToolNameWriteTodos = agentinternal.ToolNameWriteTodos
	// ToolNameDurableWait creates a Dex Timer wait.
	ToolNameDurableWait = agentinternal.ToolNameDurableWait
	// ToolNameRequestUserInput creates a durable user question.
	ToolNameRequestUserInput = agentinternal.ToolNameRequestUserInput
)

const (
	EventStreamReasoning = agentinternal.EventStreamReasoning
	EventStreamAssistant = agentinternal.EventStreamAssistant
	EventStreamActivity  = agentinternal.EventStreamActivity

	StreamEventKindReasoning = agentinternal.StreamEventKindReasoning
	StreamEventKindAssistant = agentinternal.StreamEventKindAssistant
	StreamEventKindActivity  = agentinternal.StreamEventKindActivity
)

const (
	AgentStatusInitializing           = agentinternal.AgentStatusInitializing
	AgentStatusWaitingForMessage      = agentinternal.AgentStatusWaitingForMessage
	AgentStatusCompactingContext      = agentinternal.AgentStatusCompactingContext
	AgentStatusCallingModel           = agentinternal.AgentStatusCallingModel
	AgentStatusRoutingTool            = agentinternal.AgentStatusRoutingTool
	AgentStatusWaitingForToolApproval = agentinternal.AgentStatusWaitingForToolApproval
	AgentStatusExecutingTool          = agentinternal.AgentStatusExecutingTool
	AgentStatusWaitingForTimer        = agentinternal.AgentStatusWaitingForTimer
	AgentStatusApplyingSteering       = agentinternal.AgentStatusApplyingSteering

	AgentInteractionStatusSubmitted = agentinternal.AgentInteractionStatusSubmitted
	AgentInteractionStatusWaiting   = agentinternal.AgentInteractionStatusWaiting
)

const (
	FlowStatusRunning        = agentinternal.FlowStatusRunning
	FlowStatusCompleted      = agentinternal.FlowStatusCompleted
	FlowStatusFailed         = agentinternal.FlowStatusFailed
	FlowStatusTerminated     = agentinternal.FlowStatusTerminated
	FlowStatusCanceled       = agentinternal.FlowStatusCanceled
	FlowStatusContinuedAsNew = agentinternal.FlowStatusContinuedAsNew

	FlowErrorTypeStepDecision    = agentinternal.FlowErrorTypeStepDecision
	FlowErrorTypeClientAPI       = agentinternal.FlowErrorTypeClientAPI
	FlowErrorTypeWorkerMethod    = agentinternal.FlowErrorTypeWorkerMethod
	FlowErrorTypeInvalidUserCode = agentinternal.FlowErrorTypeInvalidUserCode
	FlowErrorTypeInternal        = agentinternal.FlowErrorTypeInternal
	FlowErrorTypeTimeout         = agentinternal.FlowErrorTypeTimeout
)

const (
	InteractionModeChat      = agentinternal.InteractionModeChat
	InteractionModePlanning  = agentinternal.InteractionModePlanning
	InteractionModeExecuting = agentinternal.InteractionModeExecuting

	PlanStatusDraft     = agentinternal.PlanStatusDraft
	PlanStatusActive    = agentinternal.PlanStatusActive
	PlanStatusCompleted = agentinternal.PlanStatusCompleted

	TaskStatusPending    = agentinternal.TaskStatusPending
	TaskStatusInProgress = agentinternal.TaskStatusInProgress
	TaskStatusCompleted  = agentinternal.TaskStatusCompleted
)

const (
	MessageRoleSystem    = agentinternal.MessageRoleSystem
	MessageRoleUser      = agentinternal.MessageRoleUser
	MessageRoleAssistant = agentinternal.MessageRoleAssistant
	MessageRoleTool      = agentinternal.MessageRoleTool

	ProviderMock      = agentinternal.ProviderMock
	ProviderOpenAI    = agentinternal.ProviderOpenAI
	ProviderAnthropic = agentinternal.ProviderAnthropic
	ProviderGemini    = agentinternal.ProviderGemini
	ProviderGroq      = agentinternal.ProviderGroq

	ToolOutcomeSucceeded    = agentinternal.ToolOutcomeSucceeded
	ToolOutcomeKnownFailure = agentinternal.ToolOutcomeKnownFailure
	ToolOutcomeUnknown      = agentinternal.ToolOutcomeUnknown
)

const (
	EventKindPlanStarted        = agentinternal.EventKindPlanStarted
	EventKindPlanUpdated        = agentinternal.EventKindPlanUpdated
	EventKindPlanTaskUpdated    = agentinternal.EventKindPlanTaskUpdated
	EventKindSteeringApplied    = agentinternal.EventKindSteeringApplied
	EventKindCompactionFailed   = agentinternal.EventKindCompactionFailed
	EventKindCompacted          = agentinternal.EventKindCompacted
	EventKindModelStarted       = agentinternal.EventKindModelStarted
	EventKindModelFailed        = agentinternal.EventKindModelFailed
	EventKindModelCompleted     = agentinternal.EventKindModelCompleted
	EventKindModelToolCall      = agentinternal.EventKindModelToolCall
	EventKindUserInputRequested = agentinternal.EventKindUserInputRequested
	EventKindToolProgress       = agentinternal.EventKindToolProgress
	EventKindToolFailed         = agentinternal.EventKindToolFailed
	EventKindToolCompleted      = agentinternal.EventKindToolCompleted

	CommandSendMessage     = agentinternal.CommandSendMessage
	CommandAnswerQuestions = agentinternal.CommandAnswerQuestions
	CommandSteer           = agentinternal.CommandSteer
	CommandApproveTool     = agentinternal.CommandApproveTool
	CommandExecutePlan     = agentinternal.CommandExecutePlan
)

// Public types intentionally alias the implementation's stable application boundary.
// The aliases let embedding applications implement the interfaces and consume Client
// results without a second conversion layer or a duplicated Agent loop.
type (
	Flow   = agentinternal.Flow
	Client = agentinternal.Client

	FlowID      = agentinternal.FlowID
	RunID       = agentinternal.RunID
	CallID      = agentinternal.CallID
	MessageID   = agentinternal.MessageID
	Sequence    = agentinternal.Sequence
	ResumeToken = agentinternal.ResumeToken

	EventStream     = agentinternal.EventStream
	StreamEventKind = agentinternal.StreamEventKind
	PlanRevision    = agentinternal.PlanRevision
	PlanTaskIndex   = agentinternal.PlanTaskIndex
	Model           = agentinternal.Model
	ToolName        = agentinternal.ToolName

	AgentStatus            = agentinternal.AgentStatus
	AgentInteractionStatus = agentinternal.AgentInteractionStatus
	FlowStatus             = agentinternal.FlowStatus
	FlowErrorType          = agentinternal.FlowErrorType
	InteractionMode        = agentinternal.InteractionMode
	PlanStatus             = agentinternal.PlanStatus
	TaskStatus             = agentinternal.TaskStatus
	MessageRole            = agentinternal.MessageRole
	EventKind              = agentinternal.EventKind
	Provider               = agentinternal.Provider
	ToolOutcome            = agentinternal.ToolOutcome
	Command                = agentinternal.Command

	EnumValidationError           = agentinternal.EnumValidationError
	CommandRejectedError          = agentinternal.CommandRejectedError
	PendingMessageNotFoundError   = agentinternal.PendingMessageNotFoundError
	ArchivedMessagesNotFoundError = agentinternal.ArchivedMessagesNotFoundError
	JSONObject                    = agentinternal.JSONObject
	JSONValue                     = agentinternal.JSONValue
	AgentConfig                   = agentinternal.AgentConfig
	StartRequest                  = agentinternal.StartRequest
	ToolCall                      = agentinternal.ToolCall
	ProviderContextItem           = agentinternal.ProviderContextItem
	AgentMessage                  = agentinternal.AgentMessage
	SequencedMessage              = agentinternal.SequencedMessage
	HistoryPage                   = agentinternal.HistoryPage
	PendingUserMessage            = agentinternal.PendingUserMessage
	AgentDescription              = agentinternal.AgentDescription
	AgentSnapshot                 = agentinternal.AgentSnapshot
	UserMessage                   = agentinternal.UserMessage
	SteerMessageRequest           = agentinternal.SteerMessageRequest
	PlanTask                      = agentinternal.PlanTask
	AgentPlan                     = agentinternal.AgentPlan
	PlanExecutionRequest          = agentinternal.PlanExecutionRequest
	ToolApprovalRequest           = agentinternal.ToolApprovalRequest
	PendingApproval               = agentinternal.PendingApproval
	PendingTimer                  = agentinternal.PendingTimer
	PendingUserInput              = agentinternal.PendingUserInput
	AnswerQuestionsRequest        = agentinternal.AnswerQuestionsRequest
	UserInputAnswer               = agentinternal.UserInputAnswer
	UserInputQuestion             = agentinternal.UserInputQuestion
	UserInputQuestionID           = agentinternal.UserInputQuestionID
	UserInputOption               = agentinternal.UserInputOption
	AgentEvent                    = agentinternal.AgentEvent
	StreamEvent                   = agentinternal.StreamEvent
	ModelReply                    = agentinternal.ModelReply
	ToolDefinition                = agentinternal.ToolDefinition
	ToolExecutionResult           = agentinternal.ToolExecutionResult
	TextWriter                    = agentinternal.TextWriter
	ActivityWriter                = agentinternal.ActivityWriter
	ModelRequest                  = agentinternal.ModelRequest
	SummarizeRequest              = agentinternal.SummarizeRequest
	ModelClient                   = agentinternal.ModelClient
	ToolInvocation                = agentinternal.ToolInvocation
	ToolRegistry                  = agentinternal.ToolRegistry
	RegisteredTool                = agentinternal.RegisteredTool
)

// NewFlow constructs an Agent from its model and trusted tool boundaries.
func NewFlow(modelClient ModelClient, tools ToolRegistry) *Flow {
	return agentinternal.NewFlow(modelClient, tools)
}

// NewClient constructs an Agent application client over one Dex client and Flow definition.
func NewClient(sdkClient *dex.Client, flow *Flow) *Client {
	return agentinternal.NewClient(sdkClient, flow)
}

// NewAgentConfig returns deterministic local defaults.
func NewAgentConfig() AgentConfig {
	return agentinternal.NewAgentConfig()
}

// ParseJSONObject validates one complete JSON object.
func ParseJSONObject(encoded string) (JSONObject, error) {
	return agentinternal.ParseJSONObject(encoded)
}

// MustJSONObject returns a validated static JSON object.
func MustJSONObject(encoded string) JSONObject {
	return agentinternal.MustJSONObject(encoded)
}

// ParseJSONValue validates one complete JSON value.
func ParseJSONValue(encoded string) (JSONValue, error) {
	return agentinternal.ParseJSONValue(encoded)
}

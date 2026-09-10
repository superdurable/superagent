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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	archiveMessageChunkSize = 10
	currentMessageLimit     = 2 * archiveMessageChunkSize
	maximumMessageIDBytes   = 256
	maximumUserMessageBytes = 128 << 10
	maximumAppContextBytes  = 16 << 10
	// DefaultForwardHistoryLimit is used when MessagesAfter receives a zero limit.
	DefaultForwardHistoryLimit = 100
	// MaximumForwardHistoryLimit bounds one canonical forward-history read.
	MaximumForwardHistoryLimit = 200

	// DefaultModel uses the deterministic local provider.
	DefaultModel Model = "mock/dex"
	// DefaultContextTokens bounds reconstructed model context.
	DefaultContextTokens = 32_000
	// DefaultMessageRetention bounds retained summarized messages.
	DefaultMessageRetention = 1_000
	// DefaultSystemPrompt is used when callers omit a custom prompt.
	DefaultSystemPrompt = "You are a helpful durable AI agent. Use tools when they help, explain important actions, and never claim a tool succeeded unless its result says so."
)

var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]*$`)

// FlowID uniquely identifies one durable Agent conversation.
type FlowID string

// RunID uniquely identifies one execution run of a Flow.
type RunID string

// RequestID is a caller-assigned idempotency identity for one lifecycle command.
type RequestID string

// CallID uniquely identifies one model-requested tool call.
type CallID string

// MessageID is a caller-assigned idempotency identity for one user message.
type MessageID string

// Sequence orders durable application-history messages.
type Sequence int64

// ResumeToken resumes a best-effort event subscription.
type ResumeToken string

// EventStream selects one best-effort live Stream.
type EventStream string

const (
	EventStreamReasoning EventStream = "reasoning"
	EventStreamAssistant EventStream = "assistant"
	EventStreamActivity  EventStream = "activity"
)

// Validate rejects unknown event Streams.
func (stream EventStream) Validate() error {
	switch stream {
	case EventStreamReasoning, EventStreamAssistant, EventStreamActivity:
		return nil
	default:
		return newEnumValidationError("EventStream", string(stream))
	}
}

// UnmarshalJSON decodes and validates an event Stream.
func (stream *EventStream) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, stream, EventStream.Validate)
}

// StreamEventKind discriminates the three live event payloads.
type StreamEventKind string

const (
	StreamEventKindReasoning StreamEventKind = "reasoning_summary"
	StreamEventKindAssistant StreamEventKind = "assistant_text"
	StreamEventKindActivity  StreamEventKind = "activity"
)

// Validate rejects unknown live event kinds.
func (kind StreamEventKind) Validate() error {
	switch kind {
	case StreamEventKindReasoning, StreamEventKindAssistant, StreamEventKindActivity:
		return nil
	default:
		return newEnumValidationError("StreamEventKind", string(kind))
	}
}

// UnmarshalJSON decodes and validates a live event kind.
func (kind *StreamEventKind) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, kind, StreamEventKind.Validate)
}

// PlanRevision identifies one immutable plan revision.
type PlanRevision int64

// PlanTaskIndex identifies one task position inside a Plan revision.
type PlanTaskIndex int

// Model identifies a provider-qualified model.
type Model string

// ToolName identifies one provider-visible tool.
type ToolName string

// AgentStatus describes the active durable state-machine boundary.
type AgentStatus string

const (
	AgentStatusInitializing           AgentStatus = "initializing"
	AgentStatusWaitingForMessage      AgentStatus = "waiting_for_message"
	AgentStatusCompactingContext      AgentStatus = "compacting_context"
	AgentStatusCallingModel           AgentStatus = "calling_model"
	AgentStatusRoutingTool            AgentStatus = "routing_tool"
	AgentStatusWaitingForToolApproval AgentStatus = "waiting_for_tool_approval"
	AgentStatusExecutingTool          AgentStatus = "executing_tool"
	AgentStatusWaitingForTimer        AgentStatus = "waiting_for_timer"
	AgentStatusApplyingSteering       AgentStatus = "applying_steering"
)

// Validate rejects unknown Agent statuses.
func (status AgentStatus) Validate() error {
	switch status {
	case AgentStatusInitializing,
		AgentStatusWaitingForMessage,
		AgentStatusCompactingContext,
		AgentStatusCallingModel,
		AgentStatusRoutingTool,
		AgentStatusWaitingForToolApproval,
		AgentStatusExecutingTool,
		AgentStatusWaitingForTimer,
		AgentStatusApplyingSteering:
		return nil
	default:
		return newEnumValidationError("AgentStatus", string(status))
	}
}

// UnmarshalJSON decodes and validates an Agent status.
func (status *AgentStatus) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, status, AgentStatus.Validate)
}

// AgentInteractionStatus coordinates durable browser reconciliation.
type AgentInteractionStatus string

const (
	AgentInteractionStatusSubmitted AgentInteractionStatus = "submitted"
	AgentInteractionStatusWaiting   AgentInteractionStatus = "waiting"
)

// Validate rejects unknown interaction statuses.
func (status AgentInteractionStatus) Validate() error {
	switch status {
	case AgentInteractionStatusSubmitted, AgentInteractionStatusWaiting:
		return nil
	default:
		return newEnumValidationError("AgentInteractionStatus", string(status))
	}
}

// UnmarshalJSON decodes and validates an interaction status.
func (status *AgentInteractionStatus) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, status, AgentInteractionStatus.Validate)
}

// FlowStatus describes the Dex lifecycle state exposed with an Agent Snapshot.
type FlowStatus string

const (
	FlowStatusRunning        FlowStatus = "running"
	FlowStatusCompleted      FlowStatus = "completed"
	FlowStatusFailed         FlowStatus = "failed"
	FlowStatusTerminated     FlowStatus = "terminated"
	FlowStatusCanceled       FlowStatus = "canceled"
	FlowStatusContinuedAsNew FlowStatus = "continued_as_new"
)

// Validate rejects unknown Flow lifecycle states.
func (status FlowStatus) Validate() error {
	switch status {
	case FlowStatusRunning,
		FlowStatusCompleted,
		FlowStatusFailed,
		FlowStatusTerminated,
		FlowStatusCanceled,
		FlowStatusContinuedAsNew:
		return nil
	default:
		return newEnumValidationError("FlowStatus", string(status))
	}
}

// UnmarshalJSON decodes and validates a Flow lifecycle state.
func (status *FlowStatus) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, status, FlowStatus.Validate)
}

// FlowErrorType classifies a terminal Dex Flow failure.
type FlowErrorType string

const (
	FlowErrorTypeStepDecision    FlowErrorType = "step_decision"
	FlowErrorTypeClientAPI       FlowErrorType = "client_api"
	FlowErrorTypeWorkerMethod    FlowErrorType = "worker_method"
	FlowErrorTypeInvalidUserCode FlowErrorType = "invalid_user_code"
	FlowErrorTypeInternal        FlowErrorType = "internal"
	FlowErrorTypeTimeout         FlowErrorType = "timeout"
)

// Validate rejects unknown Flow failure categories.
func (errorType FlowErrorType) Validate() error {
	switch errorType {
	case FlowErrorTypeStepDecision,
		FlowErrorTypeClientAPI,
		FlowErrorTypeWorkerMethod,
		FlowErrorTypeInvalidUserCode,
		FlowErrorTypeInternal,
		FlowErrorTypeTimeout:
		return nil
	default:
		return newEnumValidationError("FlowErrorType", string(errorType))
	}
}

// UnmarshalJSON decodes and validates a Flow failure category.
func (errorType *FlowErrorType) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, errorType, FlowErrorType.Validate)
}

// InteractionMode controls which tools one model turn may use.
type InteractionMode string

const (
	InteractionModeChat      InteractionMode = "chat"
	InteractionModePlanning  InteractionMode = "planning"
	InteractionModeExecuting InteractionMode = "executing"
)

// Validate rejects unknown interaction modes.
func (mode InteractionMode) Validate() error {
	switch mode {
	case InteractionModeChat, InteractionModePlanning, InteractionModeExecuting:
		return nil
	default:
		return newEnumValidationError("InteractionMode", string(mode))
	}
}

// UnmarshalJSON decodes and validates an interaction mode.
func (mode *InteractionMode) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, mode, InteractionMode.Validate)
}

// PlanStatus describes one durable plan's lifecycle.
type PlanStatus string

const (
	PlanStatusDraft     PlanStatus = "draft"
	PlanStatusActive    PlanStatus = "active"
	PlanStatusCompleted PlanStatus = "completed"
)

// Validate rejects unknown plan statuses.
func (status PlanStatus) Validate() error {
	switch status {
	case PlanStatusDraft, PlanStatusActive, PlanStatusCompleted:
		return nil
	default:
		return newEnumValidationError("PlanStatus", string(status))
	}
}

// UnmarshalJSON decodes and validates a plan status.
func (status *PlanStatus) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, status, PlanStatus.Validate)
}

// TaskStatus describes one durable plan task.
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
)

// Validate rejects unknown task statuses.
func (status TaskStatus) Validate() error {
	switch status {
	case TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted:
		return nil
	default:
		return newEnumValidationError("TaskStatus", string(status))
	}
}

// UnmarshalJSON decodes and validates a task status.
func (status *TaskStatus) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, status, TaskStatus.Validate)
}

// MessageRole identifies a provider-neutral message kind.
type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)

// Validate rejects unknown message roles.
func (role MessageRole) Validate() error {
	switch role {
	case MessageRoleSystem, MessageRoleUser, MessageRoleAssistant, MessageRoleTool:
		return nil
	default:
		return newEnumValidationError("MessageRole", string(role))
	}
}

// UnmarshalJSON decodes and validates a message role.
func (role *MessageRole) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, role, MessageRole.Validate)
}

// EventKind identifies one structured activity event.
type EventKind string

const (
	EventKindPlanStarted        EventKind = "plan_started"
	EventKindPlanUpdated        EventKind = "plan_updated"
	EventKindPlanTaskUpdated    EventKind = "plan_task_updated"
	EventKindSteeringApplied    EventKind = "steering_applied"
	EventKindCompactionFailed   EventKind = "compaction_failed"
	EventKindCompacted          EventKind = "compacted"
	EventKindModelStarted       EventKind = "model_started"
	EventKindModelFailed        EventKind = "model_failed"
	EventKindModelCompleted     EventKind = "model_completed"
	EventKindModelToolCall      EventKind = "model_tool_call"
	EventKindUserInputRequested EventKind = "user_input_requested"
	EventKindToolProgress       EventKind = "tool_progress"
	EventKindToolFailed         EventKind = "tool_failed"
	EventKindToolCompleted      EventKind = "tool_completed"
)

// Validate rejects unknown event kinds.
func (kind EventKind) Validate() error {
	switch kind {
	case EventKindPlanStarted,
		EventKindPlanUpdated,
		EventKindPlanTaskUpdated,
		EventKindSteeringApplied,
		EventKindCompactionFailed,
		EventKindCompacted,
		EventKindModelStarted,
		EventKindModelFailed,
		EventKindModelCompleted,
		EventKindModelToolCall,
		EventKindUserInputRequested,
		EventKindToolProgress,
		EventKindToolFailed,
		EventKindToolCompleted:
		return nil
	default:
		return newEnumValidationError("EventKind", string(kind))
	}
}

// UnmarshalJSON decodes and validates an event kind.
func (kind *EventKind) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, kind, EventKind.Validate)
}

// Provider identifies a supported model API.
type Provider string

const (
	ProviderMock      Provider = "mock"
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderGemini    Provider = "gemini"
	ProviderGroq      Provider = "groq"
)

// Validate rejects unknown providers.
func (provider Provider) Validate() error {
	switch provider {
	case ProviderMock, ProviderOpenAI, ProviderAnthropic, ProviderGemini, ProviderGroq:
		return nil
	default:
		return newEnumValidationError("Provider", string(provider))
	}
}

// UnmarshalJSON decodes and validates a provider.
func (provider *Provider) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, provider, Provider.Validate)
}

// ToolOutcome records whether an external effect's result is known.
type ToolOutcome string

const (
	ToolOutcomeSucceeded    ToolOutcome = "succeeded"
	ToolOutcomeKnownFailure ToolOutcome = "known_failure"
	ToolOutcomeUnknown      ToolOutcome = "unknown"
)

// Validate rejects unknown tool outcomes.
func (outcome ToolOutcome) Validate() error {
	switch outcome {
	case ToolOutcomeSucceeded, ToolOutcomeKnownFailure, ToolOutcomeUnknown:
		return nil
	default:
		return newEnumValidationError("ToolOutcome", string(outcome))
	}
}

// UnmarshalJSON decodes and validates a tool outcome.
func (outcome *ToolOutcome) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, outcome, ToolOutcome.Validate)
}

// EnumValidationError identifies one unknown external enum value.
type EnumValidationError struct {
	Type  string
	Value string
}

var _ error = (*EnumValidationError)(nil)

// Error describes the invalid enum without changing its value.
func (err *EnumValidationError) Error() string {
	return fmt.Sprintf("invalid %s value %q", err.Type, err.Value)
}

// JSONObject holds one validated JSON object without dynamic domain maps.
type JSONObject string

// ParseJSONObject validates one complete JSON object.
func ParseJSONObject(encoded string) (JSONObject, error) {
	trimmed := bytes.TrimSpace([]byte(encoded))
	if len(trimmed) == 0 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return "", errors.New("value must be one valid JSON object")
	}
	return JSONObject(trimmed), nil
}

// MustJSONObject returns a validated static JSON object.
func MustJSONObject(encoded string) JSONObject {
	object, err := ParseJSONObject(encoded)
	if err != nil {
		panic(err)
	}
	return object
}

// String returns the encoded object.
func (object JSONObject) String() string {
	return string(object)
}

// MarshalJSON writes the object as JSON, not a quoted string.
func (object JSONObject) MarshalJSON() ([]byte, error) {
	validated, err := ParseJSONObject(string(object))
	if err != nil {
		return nil, err
	}
	return []byte(validated), nil
}

// UnmarshalJSON validates and stores one object.
func (object *JSONObject) UnmarshalJSON(data []byte) error {
	validated, err := ParseJSONObject(string(data))
	if err != nil {
		return err
	}
	*object = validated
	return nil
}

// JSONValue holds one validated provider-owned JSON value.
type JSONValue string

// ParseJSONValue validates one complete JSON value.
func ParseJSONValue(encoded string) (JSONValue, error) {
	trimmed := bytes.TrimSpace([]byte(encoded))
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return "", errors.New("value must be valid JSON")
	}
	return JSONValue(trimmed), nil
}

// String returns the encoded value.
func (value JSONValue) String() string {
	return string(value)
}

// MarshalJSON writes the stored JSON value.
func (value JSONValue) MarshalJSON() ([]byte, error) {
	validated, err := ParseJSONValue(string(value))
	if err != nil {
		return nil, err
	}
	return []byte(validated), nil
}

// UnmarshalJSON validates and stores one JSON value.
func (value *JSONValue) UnmarshalJSON(data []byte) error {
	validated, err := ParseJSONValue(string(data))
	if err != nil {
		return err
	}
	*value = validated
	return nil
}

// AgentConfig is the durable configuration for one Agent Flow.
type AgentConfig struct {
	// Model defaults to mock/dex and must include a supported provider prefix.
	Model Model `json:"model"`
	// SystemPrompt defaults to DefaultSystemPrompt and must not be blank.
	SystemPrompt string `json:"system_prompt"`
	// CompactionModel optionally overrides Model for summaries.
	CompactionModel *Model `json:"compaction_model,omitempty"`
	// MaxContextTokens defaults to 32000 and must be positive.
	MaxContextTokens int `json:"max_context_tokens"`
	// CompactionTriggerFraction defaults to 0.85 and must be below 1.
	CompactionTriggerFraction float64 `json:"compaction_trigger_fraction"`
	// CompactionKeepFraction defaults to 0.10 and must be below the trigger.
	CompactionKeepFraction float64 `json:"compaction_keep_fraction"`
	// MessageRetentionLimit defaults to 1000 and must be a multiple of ten.
	MessageRetentionLimit int `json:"message_retention_limit"`
	// MCPEnabled defaults true and controls trusted MCP visibility.
	MCPEnabled bool `json:"mcp_enabled"`
	// EnabledMCPServers restricts MCP servers; empty selects all configured servers.
	EnabledMCPServers []string `json:"enabled_mcp_servers"`
	// EnabledTools restricts tools; empty selects all tools on enabled servers.
	EnabledTools []ToolName `json:"enabled_tools"`
}

// EnsureStartRequest idempotently establishes one Agent identity and optional first message.
type EnsureStartRequest struct {
	RequestID          RequestID    `json:"request_id"`
	Config             AgentConfig  `json:"config"`
	ApplicationContext string       `json:"application_context"`
	InitialMessage     *UserMessage `json:"initial_message,omitempty"`
}

func (request EnsureStartRequest) fingerprint() (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode EnsureStarted request fingerprint: %w", err)
	}
	return encodedFingerprint(encoded), nil
}

// StartReceipt identifies the established durable Agent and optional first message acceptance.
type StartReceipt struct {
	RequestID      RequestID       `json:"request_id"`
	FlowID         FlowID          `json:"flow_id"`
	RunID          RunID           `json:"run_id"`
	AcceptedAt     time.Time       `json:"accepted_at"`
	IsReplay       bool            `json:"is_replay"`
	InitialMessage *MessageReceipt `json:"initial_message,omitempty"`
}

// NewAgentConfig returns deterministic local defaults.
func NewAgentConfig() AgentConfig {
	return AgentConfig{
		Model:                     DefaultModel,
		SystemPrompt:              DefaultSystemPrompt,
		MaxContextTokens:          DefaultContextTokens,
		CompactionTriggerFraction: 0.85,
		CompactionKeepFraction:    0.10,
		MessageRetentionLimit:     DefaultMessageRetention,
		MCPEnabled:                true,
		EnabledMCPServers:         []string{},
		EnabledTools:              []ToolName{},
	}
}

// Validate rejects configurations that cannot execute safely.
func (config AgentConfig) Validate() error {
	if _, err := config.Model.Provider(); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if config.CompactionModel != nil {
		if _, err := config.CompactionModel.Provider(); err != nil {
			return fmt.Errorf("compaction_model: %w", err)
		}
	}
	switch {
	case strings.TrimSpace(config.SystemPrompt) == "":
		return errors.New("system_prompt must not be empty")
	case config.MaxContextTokens <= 0:
		return errors.New("max_context_tokens must be positive")
	case !isFinite(config.CompactionKeepFraction), !isFinite(config.CompactionTriggerFraction):
		return errors.New("compaction fractions must be finite")
	case config.CompactionKeepFraction <= 0,
		config.CompactionKeepFraction >= config.CompactionTriggerFraction,
		config.CompactionTriggerFraction >= 1:
		return errors.New("compaction fractions must satisfy 0 < keep < trigger < 1")
	case config.MessageRetentionLimit < currentMessageLimit || config.MessageRetentionLimit%archiveMessageChunkSize != 0:
		return fmt.Errorf("message_retention_limit must be at least %d and a multiple of %d", currentMessageLimit, archiveMessageChunkSize)
	default:
		return nil
	}
}

// Provider returns Model's validated provider prefix.
func (model Model) Provider() (Provider, error) {
	prefix, name, found := strings.Cut(string(model), "/")
	provider := Provider(prefix)
	if !found || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("model %q must be provider-qualified", model)
	}
	if err := provider.Validate(); err != nil {
		return "", err
	}
	return provider, nil
}

// ProviderModel returns Model without its provider prefix.
func (model Model) ProviderModel() (string, error) {
	_, name, found := strings.Cut(string(model), "/")
	if !found || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("model %q must be provider-qualified", model)
	}
	if _, err := model.Provider(); err != nil {
		return "", err
	}
	return name, nil
}

// ToolCall is one model-requested function call.
type ToolCall struct {
	ID        CallID     `json:"id"`
	Name      ToolName   `json:"name"`
	Arguments JSONObject `json:"arguments"`
}

// ProviderContextItem stores opaque provider-specific replay state.
type ProviderContextItem struct {
	Provider Provider  `json:"provider"`
	Item     JSONValue `json:"item"`
}

// AgentMessage is one durable conversation item.
type AgentMessage struct {
	MessageID            MessageID             `json:"message_id"`
	Role                 MessageRole           `json:"role"`
	Content              string                `json:"content"`
	ToolCalls            []ToolCall            `json:"tool_calls"`
	ToolCallID           *CallID               `json:"tool_call_id,omitempty"`
	ToolName             *ToolName             `json:"tool_name,omitempty"`
	ProviderContextItems []ProviderContextItem `json:"provider_context_items"`
	CreatedAt            time.Time             `json:"created_at"`
}

// AgentState records state-machine and message-sequence progress.
type AgentState struct {
	NextSequence                 Sequence        `json:"next_sequence"`
	FirstRetainedSequence        Sequence        `json:"first_retained_sequence"`
	CurrentFirstSequence         Sequence        `json:"current_first_sequence"`
	LastSequence                 Sequence        `json:"last_sequence"`
	SummarizedThroughSequence    Sequence        `json:"summarized_through_sequence"`
	CompactionGeneration         int64           `json:"compaction_generation"`
	Status                       AgentStatus     `json:"status"`
	PendingToolCalls             []ToolCall      `json:"pending_tool_calls"`
	PendingToolIndex             int             `json:"pending_tool_index"`
	PlanRevision                 PlanRevision    `json:"plan_revision"`
	InteractionMode              InteractionMode `json:"interaction_mode"`
	PlanningRequiresWrite        bool            `json:"planning_requires_write"`
	PlanningAllowsWrite          bool            `json:"planning_allows_write"`
	PendingPlanExecutionRevision *PlanRevision   `json:"pending_plan_execution_revision,omitempty"`
}

// SequencedMessage pairs one application message with its durable ordering key.
type SequencedMessage struct {
	Sequence Sequence     `json:"sequence"`
	Message  AgentMessage `json:"message"`
}

// HistoryPage is one ascending page of retained application history.
type HistoryPage struct {
	Messages           []SequencedMessage `json:"messages"`
	NextBeforeSequence *Sequence          `json:"next_before_sequence,omitempty"`
}

// ForwardHistoryPage is one bounded ascending page after an exclusive sequence cursor.
// FirstRetainedSequence lets callers detect a cursor that predates retained history.
type ForwardHistoryPage struct {
	Messages              []SequencedMessage `json:"messages"`
	NextAfterSequence     *Sequence          `json:"next_after_sequence,omitempty"`
	FirstRetainedSequence Sequence           `json:"first_retained_sequence"`
	LastSequence          Sequence           `json:"last_sequence"`
	IsTruncated           bool               `json:"is_truncated"`
}

// ArchivedMessageChunk stores one immutable ten-message history page.
type ArchivedMessageChunk struct {
	Messages []SequencedMessage `json:"messages"`
}

// PendingUserMessage exposes application identity while keeping Dex queue identity private.
type PendingUserMessage struct {
	MessageID MessageID   `json:"message_id"`
	Value     UserMessage `json:"value"`
}

// AgentDescription is the durable application state needed to render a conversation.
type AgentDescription struct {
	Status                     AgentStatus            `json:"status"`
	InteractionStatus          AgentInteractionStatus `json:"interaction_status"`
	Model                      Model                  `json:"model"`
	SystemPrompt               string                 `json:"system_prompt"`
	FirstRetainedSequence      Sequence               `json:"first_retained_sequence"`
	LastSequence               Sequence               `json:"last_sequence"`
	SummarizedThroughSequence  Sequence               `json:"summarized_through_sequence"`
	PendingApproval            *PendingApproval       `json:"pending_approval,omitempty"`
	PendingTimer               *PendingTimer          `json:"pending_timer,omitempty"`
	PendingUserInput           *PendingUserInput      `json:"pending_user_input,omitempty"`
	Plan                       *AgentPlan             `json:"plan,omitempty"`
	IsPlanExecutionRequested   bool                   `json:"is_plan_execution_requested"`
	PendingQueuedMessageCount  int                    `json:"pending_queued_message_count"`
	PendingSteeredMessageCount int                    `json:"pending_steered_message_count"`
	AvailableMCPServers        []string               `json:"available_mcp_servers"`
	AvailableTools             []ToolName             `json:"available_tools"`
}

// AgentSnapshot is one atomic durable application view.
type AgentSnapshot struct {
	RunID        RunID                `json:"run_id"`
	FlowStatus   FlowStatus           `json:"flow_status"`
	ErrorType    *FlowErrorType       `json:"error_type,omitempty"`
	ErrorMessage *string              `json:"error_message,omitempty"`
	History      HistoryPage          `json:"history"`
	Description  *AgentDescription    `json:"description,omitempty"`
	Queued       []PendingUserMessage `json:"queued"`
	Steered      []PendingUserMessage `json:"steered"`
}

// NewAgentState returns the initial durable state.
func NewAgentState() AgentState {
	return AgentState{
		NextSequence:          1,
		FirstRetainedSequence: 1,
		CurrentFirstSequence:  1,
		Status:                AgentStatusWaitingForMessage,
		PendingToolCalls:      []ToolCall{},
		InteractionMode:       InteractionModeChat,
	}
}

// ContextSummary is the cumulative compacted conversation.
type ContextSummary struct {
	Generation                int64    `json:"generation"`
	SummarizedThroughSequence Sequence `json:"summarized_through_sequence"`
	Content                   string   `json:"content"`
}

// UserMessage is a queued or steered user request.
type UserMessage struct {
	MessageID           MessageID `json:"message_id"`
	Content             string    `json:"content"`
	PlanMode            bool      `json:"plan_mode"`
	AnsweredInputCallID *CallID   `json:"answered_input_call_id,omitempty"`
	AcceptedAt          time.Time `json:"accepted_at"`
}

// SendMessageRequest idempotently submits one caller-identified user message.
type SendMessageRequest struct {
	RequestID RequestID   `json:"request_id"`
	Message   UserMessage `json:"message"`
}

func (request SendMessageRequest) fingerprint() (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode SendMessage request fingerprint: %w", err)
	}
	return encodedFingerprint(encoded), nil
}

// MessageReceipt proves the first durable acceptance of one caller message identity.
type MessageReceipt struct {
	RequestID  RequestID `json:"request_id"`
	MessageID  MessageID `json:"message_id"`
	AcceptedAt time.Time `json:"accepted_at"`
	IsReplay   bool      `json:"is_replay"`
}

// CommandReceipt proves the first durable acceptance of one mutation request.
type CommandReceipt struct {
	RequestID  RequestID `json:"request_id"`
	AcceptedAt time.Time `json:"accepted_at"`
	IsReplay   bool      `json:"is_replay"`
}

// CancellationReceipt proves one durable cancellation request and terminal outcome.
type CancellationReceipt struct {
	RequestID  RequestID  `json:"request_id"`
	AcceptedAt time.Time  `json:"accepted_at"`
	FlowStatus FlowStatus `json:"flow_status"`
	IsReplay   bool       `json:"is_replay"`
}

type acceptedUserMessage struct {
	MessageID   MessageID `json:"message_id"`
	Fingerprint string    `json:"fingerprint"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

type durableCommandOutcome string

const (
	durableCommandAccepted        durableCommandOutcome = "accepted"
	durableCommandRejected        durableCommandOutcome = "rejected"
	durableCommandNotFound        durableCommandOutcome = "not_found"
	durableCommandMessageConflict durableCommandOutcome = "message_idempotency_conflict"
)

type durableCommandRecord struct {
	RequestID      RequestID             `json:"request_id"`
	Command        Command               `json:"command"`
	Fingerprint    string                `json:"fingerprint"`
	RecordedAt     time.Time             `json:"recorded_at"`
	Outcome        durableCommandOutcome `json:"outcome"`
	IsEffectReplay bool                  `json:"is_effect_replay"`
}

type durableCommandDisposition string

const (
	durableCommandCommitted           durableCommandDisposition = "committed"
	durableCommandReplayed            durableCommandDisposition = "replayed"
	durableCommandIdempotencyConflict durableCommandDisposition = "idempotency_conflict"
)

type durableCommandResult struct {
	Disposition durableCommandDisposition `json:"disposition"`
	Record      durableCommandRecord      `json:"record"`
}

type confirmStartRequest struct {
	RequestID   RequestID `json:"request_id"`
	Fingerprint string    `json:"fingerprint"`
}

// SteerMessageRequest atomically moves one queued message into steering.
type SteerMessageRequest struct {
	RequestID RequestID `json:"request_id"`
	MessageID MessageID `json:"message_id"`
}

func (request SteerMessageRequest) fingerprint() (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode SteerMessage request fingerprint: %w", err)
	}
	return encodedFingerprint(encoded), nil
}

// DeleteQueuedMessageRequest removes one exact queued application message.
type DeleteQueuedMessageRequest struct {
	RequestID RequestID `json:"request_id"`
	MessageID MessageID `json:"message_id"`
}

func (request DeleteQueuedMessageRequest) fingerprint() (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode DeleteQueuedMessage request fingerprint: %w", err)
	}
	return encodedFingerprint(encoded), nil
}

// PlanTask is one ordered task in the durable plan.
type PlanTask struct {
	Content string     `json:"content"`
	Status  TaskStatus `json:"status"`
}

// AgentPlan is the revisioned durable plan.
type AgentPlan struct {
	Revision PlanRevision `json:"revision"`
	Status   PlanStatus   `json:"status"`
	Tasks    []PlanTask   `json:"tasks"`
}

// PlanExecutionRequest selects one exact plan revision.
type PlanExecutionRequest struct {
	RequestID RequestID    `json:"request_id"`
	Revision  PlanRevision `json:"revision"`
}

func (request PlanExecutionRequest) fingerprint() (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode ExecutePlan request fingerprint: %w", err)
	}
	return encodedFingerprint(encoded), nil
}

// ToolApproval is one durable approval decision.
type ToolApproval struct {
	Approved bool `json:"approved"`
}

// ToolApprovalRequest applies a decision to one exact tool call.
type ToolApprovalRequest struct {
	RequestID RequestID `json:"request_id"`
	CallID    CallID    `json:"call_id"`
	Approved  bool      `json:"approved"`
}

func (request ToolApprovalRequest) fingerprint() (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode ApproveTool request fingerprint: %w", err)
	}
	return encodedFingerprint(encoded), nil
}

// PendingApproval describes the call awaiting user approval.
type PendingApproval struct {
	CallID    CallID     `json:"call_id"`
	ToolName  ToolName   `json:"tool_name"`
	Arguments JSONObject `json:"arguments"`
}

// PendingTimer describes a durable model-requested wait.
type PendingTimer struct {
	CallID          CallID `json:"call_id"`
	DurationSeconds int64  `json:"duration_seconds"`
	Reason          string `json:"reason"`
}

// UserInputQuestionID identifies one question within a pending input batch.
type UserInputQuestionID string

// UserInputOption describes one suggested answer.
type UserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// UserInputQuestion describes one question within a pending input batch.
type UserInputQuestion struct {
	ID       UserInputQuestionID `json:"id"`
	Header   string              `json:"header"`
	Question string              `json:"question"`
	Options  []UserInputOption   `json:"options"`
}

// PendingUserInput describes one durable batch awaiting answers.
type PendingUserInput struct {
	CallID    CallID              `json:"call_id"`
	Questions []UserInputQuestion `json:"questions"`
}

// AnswerQuestionsRequest identifies and answers one pending batch.
type AnswerQuestionsRequest struct {
	CallID  CallID            `json:"call_id"`
	Answers []UserInputAnswer `json:"answers"`
}

// UserInputAnswer answers one question in a pending input batch.
type UserInputAnswer struct {
	QuestionID UserInputQuestionID `json:"question_id"`
	Answer     string              `json:"answer"`
}

// AgentEvent is emitted to the best-effort activity Stream.
type AgentEvent struct {
	Kind             EventKind      `json:"kind"`
	Message          string         `json:"message"`
	CallID           *CallID        `json:"call_id,omitempty"`
	ToolName         *ToolName      `json:"tool_name,omitempty"`
	MessageSequence  *Sequence      `json:"message_sequence,omitempty"`
	PlanBaseRevision *PlanRevision  `json:"plan_base_revision,omitempty"`
	PlanRevision     *PlanRevision  `json:"plan_revision,omitempty"`
	PlanTaskIndex    *PlanTaskIndex `json:"plan_task_index,omitempty"`
	PlanTaskStatus   *TaskStatus    `json:"plan_task_status,omitempty"`
}

// StreamEvent is one typed best-effort Stream message.
// Text is populated for assistant and reasoning events; Activity is populated for activity events.
type StreamEvent struct {
	Kind        StreamEventKind
	Text        string
	Activity    AgentEvent
	ResumeToken ResumeToken
	CreatedAt   time.Time
	Source      string
}

// Command identifies one Agent command RPC for typed rejection errors.
type Command string

const (
	CommandStart           Command = "start"
	CommandSendMessage     Command = "send_message"
	CommandAnswerQuestions Command = "answer_questions"
	CommandSteer           Command = "steer_message"
	CommandDelete          Command = "delete_queued_message"
	CommandApproveTool     Command = "approve_tool"
	CommandExecutePlan     Command = "execute_plan"
	CommandCancel          Command = "cancel"
)

// CommandRejectedError reports a valid command that does not match current durable state.
type CommandRejectedError struct {
	Command Command
}

var _ error = (*CommandRejectedError)(nil)

// PendingMessageNotFoundError reports a queue ID that is no longer pending.
type PendingMessageNotFoundError struct {
	MessageID MessageID
}

// MessageIdempotencyConflictError reports reuse of a MessageID with different content.
type MessageIdempotencyConflictError struct {
	MessageID MessageID
}

// CommandIdempotencyConflictError reports request-ID reuse with a different command payload.
type CommandIdempotencyConflictError struct {
	Command   Command
	RequestID RequestID
}

var _ error = (*CommandIdempotencyConflictError)(nil)

// Error describes the conflicting caller request without exposing its payload.
func (err *CommandIdempotencyConflictError) Error() string {
	return fmt.Sprintf("request ID %q for command %q was already used with a different payload", err.RequestID, err.Command)
}

var _ error = (*MessageIdempotencyConflictError)(nil)

// Error describes the conflicting caller identity without exposing message content.
func (err *MessageIdempotencyConflictError) Error() string {
	return fmt.Sprintf("message ID %q was already accepted with a different payload", err.MessageID)
}

// StartIdentityConflictError reports reuse of a FlowID with different start identity.
type StartIdentityConflictError struct {
	FlowID FlowID
}

var _ error = (*StartIdentityConflictError)(nil)

// Error describes the conflicting durable Agent identity.
func (err *StartIdentityConflictError) Error() string {
	return fmt.Sprintf("agent Flow %q already exists with a different start identity", err.FlowID)
}

// AgentAlreadyTerminalError reports a cancellation that did not cause an existing terminal Flow.
type AgentAlreadyTerminalError struct {
	FlowID FlowID
	Status FlowStatus
}

var _ error = (*AgentAlreadyTerminalError)(nil)

// Error describes the terminal state that made cancellation impossible.
func (err *AgentAlreadyTerminalError) Error() string {
	return fmt.Sprintf("agent Flow %q is already terminal with status %q", err.FlowID, err.Status)
}

var _ error = (*PendingMessageNotFoundError)(nil)

// Error describes the stale queue identity.
func (err *PendingMessageNotFoundError) Error() string {
	return fmt.Sprintf("queued message %q is no longer pending", err.MessageID)
}

// ArchivedMessagesNotFoundError reports a history chunk lost to retention or a stale view.
type ArchivedMessagesNotFoundError struct {
	BeforeSequence Sequence
}

var _ error = (*ArchivedMessagesNotFoundError)(nil)

// Error describes the missing history boundary.
func (err *ArchivedMessagesNotFoundError) Error() string {
	return fmt.Sprintf("archived messages before %d are no longer retained", err.BeforeSequence)
}

// HistoryMessageNotFoundError reports a retained sequence that moved or expired during a paged read.
type HistoryMessageNotFoundError struct {
	Sequence Sequence
}

var _ error = (*HistoryMessageNotFoundError)(nil)

// Error describes the missing canonical sequence.
func (err *HistoryMessageNotFoundError) Error() string {
	return fmt.Sprintf("canonical message %d is no longer retained", err.Sequence)
}

// Error describes the rejected command without exposing durable state internals.
func (err *CommandRejectedError) Error() string {
	return fmt.Sprintf("agent command %q was rejected by current durable state", err.Command)
}

func validateNewUserMessage(message UserMessage) error {
	if err := validateMessageID(message.MessageID); err != nil {
		return err
	}
	if !message.AcceptedAt.IsZero() {
		return errors.New("accepted_at is assigned by SuperAgent and must be zero")
	}
	if !utf8.ValidString(message.Content) {
		return errors.New("content must be valid UTF-8")
	}
	if strings.ContainsRune(message.Content, '\x00') {
		return errors.New("content must not contain NUL")
	}
	if strings.TrimSpace(message.Content) == "" {
		return errors.New("content must not be empty")
	}
	if len(message.Content) > maximumUserMessageBytes {
		return fmt.Errorf("content exceeds %d bytes", maximumUserMessageBytes)
	}
	return nil
}

func validateMessageID(messageID MessageID) error {
	value := string(messageID)
	if value == "" || len(value) > maximumMessageIDBytes || !utf8.ValidString(value) || !messageIDPattern.MatchString(value) {
		return fmt.Errorf("message ID must be 1-%d bytes and contain only protocol-safe identifier characters", maximumMessageIDBytes)
	}
	return nil
}

func validateApplicationContext(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("application context must be valid UTF-8")
	}
	if strings.ContainsRune(value, '\x00') {
		return errors.New("application context must not contain NUL")
	}
	if len(value) > maximumAppContextBytes {
		return fmt.Errorf("application context exceeds %d bytes", maximumAppContextBytes)
	}
	return nil
}

func acceptedMessageInstance(messageID MessageID) string {
	digest := sha256.Sum256([]byte(messageID))
	return hex.EncodeToString(digest[:])
}

func durableCommandInstance(command Command, requestID RequestID) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s", command, requestID)))
	return hex.EncodeToString(digest[:])
}

func encodedFingerprint(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateRequestID(requestID RequestID) error {
	value := string(requestID)
	if value == "" || len(value) > maximumMessageIDBytes || !utf8.ValidString(value) || !messageIDPattern.MatchString(value) {
		return fmt.Errorf("request ID must be 1-%d bytes and contain only protocol-safe identifier characters", maximumMessageIDBytes)
	}
	return nil
}

func userMessageFingerprint(message UserMessage) (string, error) {
	encoded, err := json.Marshal(struct {
		Content  string `json:"content"`
		PlanMode bool   `json:"plan_mode"`
	}{
		Content:  message.Content,
		PlanMode: message.PlanMode,
	})
	if err != nil {
		return "", fmt.Errorf("encode user message fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func generatedMessageID(flowID FlowID, runID RunID, sequence Sequence) MessageID {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", flowID, runID, sequence)))
	return MessageID("agent:" + hex.EncodeToString(digest[:]))
}

// ModelReply is one complete provider response.
type ModelReply struct {
	Content              string                `json:"content"`
	ToolCalls            []ToolCall            `json:"tool_calls"`
	ProviderContextItems []ProviderContextItem `json:"provider_context_items"`
}

// ToolDefinition is one provider-neutral function schema and execution policy.
type ToolDefinition struct {
	Name               ToolName
	Description        string
	InputSchema        JSONObject
	RequiresApproval   bool
	AttemptTimeout     time.Duration
	MaximumAttempts    int
	RetryTotalDuration time.Duration
}

// ToolExecutionResult stores one provider-neutral tool result.
type ToolExecutionResult struct {
	Content string      `json:"content"`
	Outcome ToolOutcome `json:"outcome"`
	IsError bool        `json:"is_error"`
}

// TextWriter receives one provider text delta.
type TextWriter func(string) error

// ActivityWriter receives one structured lifecycle event.
type ActivityWriter func(AgentEvent) error

// ModelRequest contains one stateless provider invocation.
type ModelRequest struct {
	Config         AgentConfig
	Messages       []AgentMessage
	Tools          []ToolDefinition
	WriteAssistant TextWriter
	WriteReasoning TextWriter
	WriteActivity  ActivityWriter
	ForcedTool     ToolName
	FlowID         FlowID
}

// SummarizeRequest contains one cumulative compaction invocation.
type SummarizeRequest struct {
	Config          AgentConfig
	PreviousSummary string
	Messages        []AgentMessage
	FlowID          FlowID
}

// ModelClient is the provider-neutral model boundary.
type ModelClient interface {
	Complete(context.Context, ModelRequest) (ModelReply, error)
	Summarize(context.Context, SummarizeRequest) (string, error)
	CountTokens(Model, []AgentMessage) int
}

// ToolInvocation contains one trusted-registry execution request.
type ToolInvocation struct {
	FlowID             FlowID
	ApplicationContext string
	Name               ToolName
	Arguments          JSONObject
	EnabledServers     []string
	WriteProgress      TextWriter
	CallID             CallID
}

// ToolRegistry exposes trusted, discovered MCP capabilities.
type ToolRegistry interface {
	ServerNames() []string
	RegisteredTools() []RegisteredTool
	Definitions([]string, []ToolName) []ToolDefinition
	Execute(context.Context, ToolInvocation) (ToolExecutionResult, error)
}

// RegisteredTool maps a model-visible name to one MCP server.
type RegisteredTool struct {
	ServerName string
	RemoteName string
	Definition ToolDefinition
}

func validateTask(task PlanTask, index int) error {
	if strings.TrimSpace(task.Content) == "" {
		return fmt.Errorf("todos[%d].content must be a non-empty string", index)
	}
	if err := task.Status.Validate(); err != nil {
		return fmt.Errorf("todos[%d].status: %w", index, err)
	}
	return nil
}

func decodeEnum[T ~string](data []byte, destination *T, validate func(T) error) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	decoded := T(value)
	if err := validate(decoded); err != nil {
		return err
	}
	*destination = decoded
	return nil
}

func newEnumValidationError(typeName string, value string) error {
	return &EnumValidationError{Type: typeName, Value: value}
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

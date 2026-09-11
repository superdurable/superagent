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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/superdurable/dex/sdk-go/dex"
)

const (
	defaultCommandTimeout = 20 * time.Second
	defaultEventPoll      = 20 * time.Second
	snapshotActiveProbe   = 100 * time.Millisecond
)

// Client is the typed application boundary around Dex Agent operations.
// It deliberately exposes no raw persistence descriptors.
type Client struct {
	sdk            *dex.Client
	flow           *Flow
	commandTimeout time.Duration
	eventPoll      time.Duration
}

// NewClient constructs an Agent application client over one Dex client and Flow definition.
func NewClient(sdkClient *dex.Client, flow *Flow) *Client {
	if sdkClient == nil {
		panic("Dex client is required")
	}
	if flow == nil {
		panic("Agent Flow is required")
	}
	return &Client{
		sdk:            sdkClient,
		flow:           flow,
		commandTimeout: defaultCommandTimeout,
		eventPoll:      defaultEventPoll,
	}
}

// Start creates one non-reusable durable Agent Flow.
func (client *Client) Start(ctx context.Context, flowID FlowID, request StartRequest) (RunID, error) {
	if err := validateFlowID(flowID); err != nil {
		return "", err
	}
	if err := client.flow.validateConfig(request.Config); err != nil {
		return "", fmt.Errorf("validate Agent config: %w", err)
	}
	if err := validateRuntimeMetadata(request.RuntimeMetadata); err != nil {
		return "", err
	}
	metadata := request.RuntimeMetadata
	if metadata == "" {
		metadata = MustJSONObject(`{}`)
	}
	initialMetadata, err := dex.InitialAttribute(agentRuntimeMetadataAttribute, metadata)
	if err != nil {
		return "", fmt.Errorf("encode Agent runtime metadata: %w", err)
	}
	runID, err := client.sdk.StartFlow(ctx, client.flow, string(flowID), request.Config, dex.StartFlowOptions{
		IDReusePolicy: dex.IDReuseDisallow,
		Attributes:    []dex.InitialAttributeDef{initialMetadata},
	})
	if err != nil {
		return "", err
	}
	if err := client.WaitForInteractionStatus(ctx, flowID, AgentInteractionStatusWaiting); err != nil {
		return "", fmt.Errorf("wait for initial Agent interaction status: %w", err)
	}
	return RunID(runID), nil
}

// SendMessage invokes the durable SendMessage command.
func (client *Client) SendMessage(ctx context.Context, flowID FlowID, message UserMessage) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if err := validateNewUserMessage(message); err != nil {
		return err
	}
	accepted, err := invokeLockedCommand(ctx, client.commandTimeout, func(ctx context.Context, accepted *bool) error {
		return client.sdk.InvokeRPC(ctx, string(flowID), client.flow.SendMessage, message, accepted, dex.InvokeOptions{
			Timeout:        client.commandTimeout,
			LockAttributes: []dex.AttributeLock{dex.LockAttribute(pendingUserInputAttribute)},
		})
	})
	if err != nil {
		return err
	}
	return ensureAccepted(accepted, CommandSendMessage)
}

// AnswerQuestions invokes the durable command for one exact pending input batch.
func (client *Client) AnswerQuestions(
	ctx context.Context,
	flowID FlowID,
	request AnswerQuestionsRequest,
) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if err := validateAnswerQuestionsRequest(request); err != nil {
		return err
	}
	accepted, err := invokeLockedCommand(ctx, client.commandTimeout, func(ctx context.Context, accepted *bool) error {
		return client.sdk.InvokeRPC(ctx, string(flowID), client.flow.AnswerQuestions, request, accepted, dex.InvokeOptions{
			Timeout:        client.commandTimeout,
			LockAttributes: []dex.AttributeLock{dex.LockAttribute(pendingUserInputAttribute)},
		})
	})
	if err != nil {
		return err
	}
	return ensureAccepted(accepted, CommandAnswerQuestions)
}

func invokeLockedCommand(
	ctx context.Context,
	timeout time.Duration,
	invoke func(context.Context, *bool) error,
) (bool, error) {
	const initialRetryDelay = 5 * time.Millisecond
	const maximumRetryDelay = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	retryDelay := initialRetryDelay
	for {
		var accepted bool
		err := invoke(ctx, &accepted)
		if err == nil {
			return accepted, nil
		}
		var conflict *dex.RPCLockConflictError
		if !errors.As(err, &conflict) {
			return false, err
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
		retryDelay = min(retryDelay*2, maximumRetryDelay)
	}
}

// SteerMessage invokes the durable SteerMessage command.
func (client *Client) SteerMessage(ctx context.Context, flowID FlowID, request SteerMessageRequest) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if err := validateMessageID(request.MessageID); err != nil {
		return err
	}
	var accepted bool
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.SteerMessage, request, &accepted, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LoadChannels:    []dex.ChannelDef{queuedUserMessagesChannel},
	}); err != nil {
		return err
	}
	if !accepted {
		return &PendingMessageNotFoundError{MessageID: request.MessageID}
	}
	return nil
}

// Snapshot reads one atomic durable application view.
func (client *Client) Snapshot(
	ctx context.Context,
	flowID FlowID,
) (AgentSnapshot, error) {
	if err := validateFlowID(flowID); err != nil {
		return AgentSnapshot{}, err
	}
	var snapshot AgentSnapshot
	err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.Snapshot, nil, &snapshot, dex.InvokeOptions{
		Timeout:           client.commandTimeout,
		LoadAttributeMaps: []dex.AttributeDef{currentMessagesAttribute},
		LoadChannels: []dex.ChannelDef{
			queuedUserMessagesChannel,
			steeredUserMessagesChannel,
		},
	})
	if err == nil {
		return client.resolveSnapshotLifecycle(ctx, flowID, snapshot)
	}
	var inactive *dex.FlowNotActiveError
	if !errors.As(err, &inactive) {
		return AgentSnapshot{}, err
	}
	terminal, terminalErr := client.terminalSnapshot(ctx, flowID, "")
	if terminalErr != nil {
		return AgentSnapshot{}, errors.Join(err, terminalErr)
	}
	return terminal, nil
}

func (client *Client) resolveSnapshotLifecycle(
	ctx context.Context,
	flowID FlowID,
	snapshot AgentSnapshot,
) (AgentSnapshot, error) {
	if snapshot.Description == nil {
		return snapshot, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, snapshotActiveProbe)
	defer cancel()
	var matched AgentInteractionStatus
	err := client.sdk.WaitForAttributeMatch(
		probeCtx,
		string(flowID),
		agentInteractionStatusAttribute,
		dex.AttributeMatchEqual(snapshot.Description.InteractionStatus),
		&matched,
	)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		return snapshot, nil
	}
	if ctx.Err() != nil {
		return AgentSnapshot{}, ctx.Err()
	}
	var inactive *dex.FlowNotActiveError
	if errors.As(err, &inactive) {
		return client.terminalSnapshot(ctx, flowID, snapshot.RunID)
	}
	// Keep active Snapshots available during lifecycle probe outages.
	return snapshot, nil
}

// ArchivedMessages reads exactly one immutable history chunk before a sequence boundary.
func (client *Client) ArchivedMessages(ctx context.Context, flowID FlowID, before Sequence) (HistoryPage, error) {
	if err := validateFlowID(flowID); err != nil {
		return HistoryPage{}, err
	}
	first, isValid := archivedMessageChunkFirst(before)
	if !isValid {
		return HistoryPage{}, fmt.Errorf("before sequence must identify a %d-message boundary", archiveMessageChunkSize)
	}
	var result archivedMessagesRPCOutput
	err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ArchivedMessages, before, &result, dex.InvokeOptions{
		Timeout: client.commandTimeout,
		LoadAttributeMapInstances: []dex.AttributeMapLoad{
			archivedMessagesAttribute.Load(sequenceKey(first)),
		},
	})
	if err != nil {
		return HistoryPage{}, err
	}
	if !result.Found {
		return HistoryPage{}, &ArchivedMessagesNotFoundError{BeforeSequence: before}
	}
	return result.Page, nil
}

// WaitForInteractionStatus blocks until the durable synchronization status matches expected.
func (client *Client) WaitForInteractionStatus(
	ctx context.Context,
	flowID FlowID,
	expected AgentInteractionStatus,
) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if err := expected.Validate(); err != nil {
		return err
	}
	var matched AgentInteractionStatus
	return client.sdk.WaitForAttributeMatch(
		ctx,
		string(flowID),
		agentInteractionStatusAttribute,
		dex.AttributeMatchEqual(expected),
		&matched,
	)
}

func (client *Client) terminalSnapshot(
	ctx context.Context,
	flowID FlowID,
	runID RunID,
) (AgentSnapshot, error) {
	result, err := client.sdk.WaitForFlow(ctx, string(flowID), dex.WaitForFlowOptions{})
	if err != nil {
		return AgentSnapshot{}, fmt.Errorf("read terminal Flow result: %w", err)
	}
	status, err := flowStatusFromDex(result.Status)
	if err != nil {
		return AgentSnapshot{}, err
	}
	if status == FlowStatusRunning {
		return AgentSnapshot{}, errors.New("inactive Agent resolved to a non-terminal Flow")
	}
	if runID == "" {
		runID, err = client.currentRunID(ctx, flowID)
		if err != nil {
			return AgentSnapshot{}, err
		}
	}
	errorType, err := flowErrorTypeFromDex(result.ErrorType)
	if err != nil {
		return AgentSnapshot{}, err
	}
	var errorMessage *string
	if result.ErrorMessage != "" {
		message := result.ErrorMessage
		errorMessage = &message
	}
	return AgentSnapshot{
		RunID:        runID,
		FlowStatus:   status,
		ErrorType:    errorType,
		ErrorMessage: errorMessage,
		History:      HistoryPage{Messages: []SequencedMessage{}},
		Queued:       []PendingUserMessage{},
		Steered:      []PendingUserMessage{},
	}, nil
}

func (client *Client) currentRunID(ctx context.Context, flowID FlowID) (RunID, error) {
	current, err := client.latestAgentRun(ctx, flowID)
	if err != nil {
		return "", err
	}
	if current == nil {
		return "", fmt.Errorf("agent Flow %q has no searchable run", flowID)
	}
	return RunID(current.RunID), nil
}

func (client *Client) latestAgentRun(ctx context.Context, flowID FlowID) (*dex.SearchFlowEntry, error) {
	query := "WorkflowId=" + visibilityString(string(flowID))
	page, err := client.sdk.SearchFlows(ctx, query, 100, "")
	if err != nil {
		return nil, fmt.Errorf("find Agent Flow run: %w", err)
	}
	var current *dex.SearchFlowEntry
	for index := range page.Flows {
		candidate := &page.Flows[index]
		if candidate.FlowID != string(flowID) || candidate.FlowType != flowTypeAIAgent {
			continue
		}
		if current == nil || candidate.StartedAt.After(current.StartedAt) {
			current = candidate
		}
	}
	if current == nil {
		return nil, nil
	}
	return current, nil
}

func visibilityString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func flowStatusFromDex(status dex.FlowStatus) (FlowStatus, error) {
	switch status {
	case dex.FlowRunning:
		return FlowStatusRunning, nil
	case dex.FlowCompleted:
		return FlowStatusCompleted, nil
	case dex.FlowFailed:
		return FlowStatusFailed, nil
	case dex.FlowTerminated:
		return FlowStatusTerminated, nil
	case dex.FlowCanceled:
		return FlowStatusCanceled, nil
	case dex.FlowContinuedAsNew:
		return FlowStatusContinuedAsNew, nil
	case dex.FlowServerSideTimeoutInternalOnly:
		return "", errors.New("dex returned its internal-only Flow timeout status")
	default:
		return "", fmt.Errorf("unknown Dex Flow status %d", status)
	}
}

func flowErrorTypeFromDex(errorType dex.FlowErrorType) (*FlowErrorType, error) {
	var mapped FlowErrorType
	switch errorType {
	case 0:
		return nil, nil
	case dex.FlowErrorStepDecision:
		mapped = FlowErrorTypeStepDecision
	case dex.FlowErrorClientAPI:
		mapped = FlowErrorTypeClientAPI
	case dex.FlowErrorWorkerMethod:
		mapped = FlowErrorTypeWorkerMethod
	case dex.FlowErrorInvalidUserCode:
		mapped = FlowErrorTypeInvalidUserCode
	case dex.FlowErrorInternal:
		mapped = FlowErrorTypeInternal
	case dex.FlowErrorTimeout:
		mapped = FlowErrorTypeTimeout
	default:
		return nil, fmt.Errorf("unknown Dex Flow error type %d", errorType)
	}
	return &mapped, nil
}

// DeleteQueuedMessage removes one exact pending user message.
func (client *Client) DeleteQueuedMessage(ctx context.Context, flowID FlowID, messageID MessageID) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if err := validateMessageID(messageID); err != nil {
		return err
	}
	return client.sdk.DeleteChannelMessage(
		ctx,
		string(flowID),
		queuedUserMessagesChannel,
		string(messageID),
	)
}

// ApproveTool invokes the durable ApproveTool command.
func (client *Client) ApproveTool(ctx context.Context, flowID FlowID, request ToolApprovalRequest) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if strings.TrimSpace(string(request.CallID)) == "" {
		return errors.New("call ID must not be empty")
	}
	var accepted bool
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ApproveTool, request, &accepted, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes:  []dex.AttributeLock{dex.LockAttribute(pendingApprovalAttribute)},
	}); err != nil {
		return err
	}
	return ensureAccepted(accepted, CommandApproveTool)
}

// ExecutePlan invokes the durable ExecutePlan command.
func (client *Client) ExecutePlan(ctx context.Context, flowID FlowID, request PlanExecutionRequest) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if request.Revision <= 0 {
		return errors.New("plan revision must be positive")
	}
	var accepted bool
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ExecutePlan, request, &accepted, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes:  []dex.AttributeLock{dex.LockAttribute(agentStateAttribute)},
	}); err != nil {
		return err
	}
	return ensureAccepted(accepted, CommandExecutePlan)
}

// ReadEvent long-polls exactly one typed best-effort Stream.
func (client *Client) ReadEvent(
	ctx context.Context,
	flowID FlowID,
	stream EventStream,
	resumeToken ResumeToken,
) (StreamEvent, error) {
	if strings.TrimSpace(string(flowID)) == "" {
		return StreamEvent{}, errors.New("flow ID must not be empty")
	}
	if err := stream.Validate(); err != nil {
		return StreamEvent{}, err
	}
	pollCtx, cancel := context.WithTimeout(ctx, client.eventPoll)
	defer cancel()
	switch stream {
	case EventStreamReasoning:
		var value string
		message, err := client.sdk.ReadStream(
			pollCtx,
			string(flowID),
			reasoningSummaryStream,
			string(resumeToken),
			&value,
		)
		if err != nil {
			return StreamEvent{}, err
		}
		return textStreamEvent(StreamEventKindReasoning, message, value), nil
	case EventStreamAssistant:
		var value string
		message, err := client.sdk.ReadStream(
			pollCtx,
			string(flowID),
			assistantTextStream,
			string(resumeToken),
			&value,
		)
		if err != nil {
			return StreamEvent{}, err
		}
		return textStreamEvent(StreamEventKindAssistant, message, value), nil
	case EventStreamActivity:
		var value AgentEvent
		message, err := client.sdk.ReadStream(
			pollCtx,
			string(flowID),
			agentActivityStream,
			string(resumeToken),
			&value,
		)
		if err != nil {
			return StreamEvent{}, err
		}
		return StreamEvent{
			Kind:        StreamEventKindActivity,
			Activity:    value,
			ResumeToken: ResumeToken(message.ResumeToken),
			CreatedAt:   message.CreatedTime,
			Source:      message.Source,
		}, nil
	default:
		return StreamEvent{}, fmt.Errorf("unsupported event Stream %q", stream)
	}
}

func validateFlowID(flowID FlowID) error {
	if strings.TrimSpace(string(flowID)) == "" {
		return errors.New("flow ID must not be empty")
	}
	return nil
}

func ensureAccepted(accepted bool, command Command) error {
	if !accepted {
		return &CommandRejectedError{Command: command}
	}
	return nil
}

func textStreamEvent(kind StreamEventKind, message dex.StreamMessage, value string) StreamEvent {
	return StreamEvent{
		Kind:        kind,
		Text:        value,
		ResumeToken: ResumeToken(message.ResumeToken),
		CreatedAt:   message.CreatedTime,
		Source:      message.Source,
	}
}

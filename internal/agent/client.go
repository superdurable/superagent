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
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
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

// EnsureStarted idempotently establishes one durable Agent and optional first message.
func (client *Client) EnsureStarted(
	ctx context.Context,
	flowID FlowID,
	request EnsureStartRequest,
) (StartReceipt, error) {
	if err := validateFlowID(flowID); err != nil {
		return StartReceipt{}, err
	}
	if err := validateRequestID(request.RequestID); err != nil {
		return StartReceipt{}, err
	}
	if err := client.flow.validateConfig(request.Config); err != nil {
		return StartReceipt{}, fmt.Errorf("validate Agent config: %w", err)
	}
	if err := validateApplicationContext(request.ApplicationContext); err != nil {
		return StartReceipt{}, err
	}
	if request.InitialMessage != nil {
		if err := validateNewUserMessage(*request.InitialMessage); err != nil {
			return StartReceipt{}, fmt.Errorf("validate initial message: %w", err)
		}
	}
	fingerprint, fingerprintErr := request.fingerprint()
	if fingerprintErr != nil {
		return StartReceipt{}, fingerprintErr
	}
	initialContext, contextErr := dex.InitialAttribute(applicationContextAttribute, request.ApplicationContext)
	if contextErr != nil {
		return StartReceipt{}, fmt.Errorf("encode application context: %w", contextErr)
	}
	requestID := flowStartRequestID(flowID, request.RequestID)
	runID, startErr := client.sdk.StartFlow(ctx, client.flow, string(flowID), request.Config, dex.StartFlowOptions{
		IDReusePolicy: dex.IDReuseDisallow,
		Attributes:    []dex.InitialAttributeDef{initialContext},
		AlreadyStarted: &dex.AlreadyStartedOptions{
			IgnoreError: true,
		},
		RequestID: &requestID,
	})
	if startErr != nil {
		return StartReceipt{}, startErr
	}
	if waitErr := client.waitForInitialization(ctx, flowID); waitErr != nil {
		return StartReceipt{}, fmt.Errorf("wait for Agent initialization: %w", waitErr)
	}
	if identityErr := client.verifyStartIdentity(ctx, flowID, request); identityErr != nil {
		return StartReceipt{}, identityErr
	}
	confirmation, err := client.confirmStart(ctx, flowID, request.RequestID, fingerprint)
	if err != nil {
		return StartReceipt{}, err
	}
	receipt := StartReceipt{
		RequestID:  confirmation.RequestID,
		FlowID:     flowID,
		RunID:      RunID(runID),
		AcceptedAt: confirmation.AcceptedAt,
		IsReplay:   confirmation.IsReplay,
	}
	if request.InitialMessage != nil {
		messageReceipt, sendErr := client.SendMessage(ctx, flowID, SendMessageRequest{
			RequestID: initialMessageRequestID(request.RequestID),
			Message:   *request.InitialMessage,
		})
		if sendErr != nil {
			return StartReceipt{}, fmt.Errorf("accept initial message: %w", sendErr)
		}
		receipt.InitialMessage = &messageReceipt
	}
	return receipt, nil
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
func (client *Client) Start(ctx context.Context, flowID FlowID, config AgentConfig) (RunID, error) {
	if strings.TrimSpace(string(flowID)) == "" {
		return "", errors.New("flow ID must not be empty")
	}
	if err := client.flow.validateConfig(config); err != nil {
		return "", fmt.Errorf("validate Agent config: %w", err)
	}
	initialContext, err := dex.InitialAttribute(applicationContextAttribute, "")
	if err != nil {
		return "", fmt.Errorf("encode empty application context: %w", err)
	}
	requestID := "start:" + string(flowID)
	runID, err := client.sdk.StartFlow(ctx, client.flow, string(flowID), config, dex.StartFlowOptions{
		IDReusePolicy: dex.IDReuseDisallow,
		Attributes:    []dex.InitialAttributeDef{initialContext},
		RequestID:     &requestID,
	})
	if err != nil {
		return "", err
	}
	if err := client.WaitForInteractionStatus(ctx, flowID, AgentInteractionStatusWaiting); err != nil {
		return "", fmt.Errorf("wait for initial Agent interaction status: %w", err)
	}
	return RunID(runID), nil
}

// SendMessage idempotently invokes the durable SendMessage command.
func (client *Client) SendMessage(
	ctx context.Context,
	flowID FlowID,
	request SendMessageRequest,
) (MessageReceipt, error) {
	if err := validateFlowID(flowID); err != nil {
		return MessageReceipt{}, err
	}
	if err := validateRequestID(request.RequestID); err != nil {
		return MessageReceipt{}, err
	}
	if err := validateNewUserMessage(request.Message); err != nil {
		return MessageReceipt{}, err
	}
	commandInstance := durableCommandInstance(CommandSendMessage, request.RequestID)
	messageInstance := acceptedMessageInstance(request.Message.MessageID)
	var result durableCommandResult
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.SendMessage, request, &result, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes: []dex.AttributeLock{
			dex.LockAttributeMap(durableCommandsAttribute, commandInstance),
			dex.LockAttributeMap(acceptedUserMessagesAttribute, messageInstance),
		},
		LoadAttributeMapInstances: []dex.AttributeMapLoad{
			durableCommandsAttribute.Load(commandInstance),
			acceptedUserMessagesAttribute.Load(messageInstance),
		},
	}); err != nil {
		return MessageReceipt{}, err
	}
	commandReceipt, err := commandReceipt(result, CommandSendMessage, request.RequestID, request.Message.MessageID)
	if err != nil {
		return MessageReceipt{}, err
	}
	return MessageReceipt{
		RequestID:  commandReceipt.RequestID,
		MessageID:  request.Message.MessageID,
		AcceptedAt: commandReceipt.AcceptedAt,
		IsReplay:   commandReceipt.IsReplay,
	}, nil
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
	if strings.TrimSpace(string(request.CallID)) == "" {
		return errors.New("call ID must not be empty")
	}
	if len(request.Answers) == 0 || len(request.Answers) > maximumUserInputQuestions {
		return fmt.Errorf("answers must contain 1-%d values", maximumUserInputQuestions)
	}
	seen := make(map[UserInputQuestionID]struct{}, len(request.Answers))
	for _, answer := range request.Answers {
		if strings.TrimSpace(string(answer.QuestionID)) == "" || strings.TrimSpace(answer.Answer) == "" {
			return errors.New("answers require question ID and answer")
		}
		if _, found := seen[answer.QuestionID]; found {
			return fmt.Errorf("question %q was answered more than once", answer.QuestionID)
		}
		seen[answer.QuestionID] = struct{}{}
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

// SteerMessage idempotently invokes the durable SteerMessage command.
func (client *Client) SteerMessage(
	ctx context.Context,
	flowID FlowID,
	request SteerMessageRequest,
) (CommandReceipt, error) {
	if err := validateFlowID(flowID); err != nil {
		return CommandReceipt{}, err
	}
	if err := validateRequestID(request.RequestID); err != nil {
		return CommandReceipt{}, err
	}
	if err := validateMessageID(request.MessageID); err != nil {
		return CommandReceipt{}, err
	}
	instance := durableCommandInstance(CommandSteer, request.RequestID)
	var result durableCommandResult
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.SteerMessage, request, &result, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes:  []dex.AttributeLock{dex.LockAttributeMap(durableCommandsAttribute, instance)},
		LoadAttributeMapInstances: []dex.AttributeMapLoad{
			durableCommandsAttribute.Load(instance),
		},
		LoadChannels: []dex.ChannelDef{queuedUserMessagesChannel},
	}); err != nil {
		return CommandReceipt{}, pendingMessageMutationError(err, request.MessageID)
	}
	return commandReceipt(result, CommandSteer, request.RequestID, request.MessageID)
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
	if before <= Sequence(archiveMessageChunkSize) || (before-1)%Sequence(archiveMessageChunkSize) != 0 {
		return HistoryPage{}, fmt.Errorf("before sequence must identify a %d-message boundary", archiveMessageChunkSize)
	}
	first := before - Sequence(archiveMessageChunkSize)
	var chunk ArchivedMessageChunk
	found, err := client.sdk.GetAttributeMapInstance(
		ctx,
		string(flowID),
		archivedMessagesAttribute,
		sequenceKey(first),
		&chunk,
	)
	if err != nil {
		return HistoryPage{}, err
	}
	if !found {
		return HistoryPage{}, &ArchivedMessagesNotFoundError{BeforeSequence: before}
	}
	var state AgentState
	found, err = client.sdk.GetAttribute(ctx, string(flowID), agentStateAttribute, &state)
	if err != nil {
		return HistoryPage{}, err
	}
	if !found {
		return HistoryPage{}, errors.New("agent state is not initialized")
	}
	var next *Sequence
	if first > state.FirstRetainedSequence {
		value := first
		next = &value
	}
	return HistoryPage{Messages: chunk.Messages, NextBeforeSequence: next}, nil
}

// MessagesAfter reads retained canonical history after an exclusive cursor.
func (client *Client) MessagesAfter(
	ctx context.Context,
	flowID FlowID,
	after Sequence,
	limit int,
) (ForwardHistoryPage, error) {
	if err := validateFlowID(flowID); err != nil {
		return ForwardHistoryPage{}, err
	}
	if after < 0 {
		return ForwardHistoryPage{}, errors.New("after sequence must not be negative")
	}
	if limit == 0 {
		limit = DefaultForwardHistoryLimit
	}
	if limit < 1 || limit > MaximumForwardHistoryLimit {
		return ForwardHistoryPage{}, fmt.Errorf("limit must be between 1 and %d", MaximumForwardHistoryLimit)
	}
	var state AgentState
	found, err := client.sdk.GetAttribute(ctx, string(flowID), agentStateAttribute, &state)
	if err != nil {
		return ForwardHistoryPage{}, err
	}
	if !found {
		return ForwardHistoryPage{}, errors.New("agent state is not initialized")
	}
	page := ForwardHistoryPage{
		Messages:              []SequencedMessage{},
		FirstRetainedSequence: state.FirstRetainedSequence,
		LastSequence:          state.LastSequence,
	}
	if after >= state.LastSequence {
		return page, nil
	}
	start := max(after+1, state.FirstRetainedSequence)
	end := state.LastSequence
	if available := state.LastSequence - start + 1; available > Sequence(limit) {
		end = start + Sequence(limit) - 1
	}
	archiveCache := make(map[Sequence]ArchivedMessageChunk)
	page.Messages = make([]SequencedMessage, 0, int(end-start+1))
	for sequence := start; sequence <= end; sequence++ {
		message, readErr := client.readCanonicalMessage(ctx, flowID, state, sequence, archiveCache)
		if readErr != nil {
			return ForwardHistoryPage{}, readErr
		}
		page.Messages = append(page.Messages, SequencedMessage{Sequence: sequence, Message: message})
	}
	if end < state.LastSequence {
		next := end
		page.NextAfterSequence = &next
		page.IsTruncated = true
	}
	return page, nil
}

func (client *Client) readCanonicalMessage(
	ctx context.Context,
	flowID FlowID,
	state AgentState,
	sequence Sequence,
	archiveCache map[Sequence]ArchivedMessageChunk,
) (AgentMessage, error) {
	if sequence >= state.CurrentFirstSequence {
		var message AgentMessage
		found, err := client.sdk.GetAttributeMapInstance(
			ctx,
			string(flowID),
			currentMessagesAttribute,
			sequenceKey(sequence),
			&message,
		)
		if err != nil {
			return AgentMessage{}, err
		}
		if found {
			return message, nil
		}
		// The current window may have moved to an archive after the state read.
	}
	first := ((sequence - 1) / Sequence(archiveMessageChunkSize) * Sequence(archiveMessageChunkSize)) + 1
	chunk, found := archiveCache[first]
	if !found {
		var err error
		found, err = client.sdk.GetAttributeMapInstance(
			ctx,
			string(flowID),
			archivedMessagesAttribute,
			sequenceKey(first),
			&chunk,
		)
		if err != nil {
			return AgentMessage{}, err
		}
		if !found {
			return AgentMessage{}, &HistoryMessageNotFoundError{Sequence: sequence}
		}
		archiveCache[first] = chunk
	}
	index := sequence - first
	if index < 0 || index >= Sequence(len(chunk.Messages)) || chunk.Messages[index].Sequence != sequence {
		return AgentMessage{}, &HistoryMessageNotFoundError{Sequence: sequence}
	}
	return chunk.Messages[index].Message, nil
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

// Cancel durably records a caller request, cancels the active Flow, and observes terminal cancellation.
func (client *Client) Cancel(
	ctx context.Context,
	flowID FlowID,
	requestID RequestID,
) (CancellationReceipt, error) {
	if err := validateFlowID(flowID); err != nil {
		return CancellationReceipt{}, err
	}
	if err := validateRequestID(requestID); err != nil {
		return CancellationReceipt{}, err
	}
	instance := durableCommandInstance(CommandCancel, requestID)
	var result durableCommandResult
	err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.AcceptCancellation, requestID, &result, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes:  []dex.AttributeLock{dex.LockAttributeMap(durableCommandsAttribute, instance)},
		LoadAttributeMapInstances: []dex.AttributeMapLoad{
			durableCommandsAttribute.Load(instance),
		},
	})
	if err != nil {
		var inactive *dex.FlowNotActiveError
		if !errors.As(err, &inactive) {
			return CancellationReceipt{}, err
		}
		return client.reconcileInactiveCancellation(ctx, flowID, requestID)
	}
	receipt, err := commandReceipt(result, CommandCancel, requestID, "")
	if err != nil {
		return CancellationReceipt{}, err
	}
	if err := client.sdk.StopFlow(ctx, string(flowID), dex.StopOptions{
		Type:   dex.CancelFlow,
		Reason: "canceled through SuperAgent",
	}); err != nil {
		var inactive *dex.FlowNotActiveError
		if !errors.As(err, &inactive) {
			return CancellationReceipt{}, err
		}
	}
	return client.waitForCancellation(ctx, flowID, receipt)
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

// DeleteQueuedMessage idempotently removes one exact pending user message.
func (client *Client) DeleteQueuedMessage(
	ctx context.Context,
	flowID FlowID,
	request DeleteQueuedMessageRequest,
) (CommandReceipt, error) {
	if err := validateFlowID(flowID); err != nil {
		return CommandReceipt{}, err
	}
	if err := validateRequestID(request.RequestID); err != nil {
		return CommandReceipt{}, err
	}
	if err := validateMessageID(request.MessageID); err != nil {
		return CommandReceipt{}, err
	}
	instance := durableCommandInstance(CommandDelete, request.RequestID)
	var result durableCommandResult
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.DeleteQueuedMessage, request, &result, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes:  []dex.AttributeLock{dex.LockAttributeMap(durableCommandsAttribute, instance)},
		LoadAttributeMapInstances: []dex.AttributeMapLoad{
			durableCommandsAttribute.Load(instance),
		},
		LoadChannels: []dex.ChannelDef{queuedUserMessagesChannel},
	}); err != nil {
		return CommandReceipt{}, pendingMessageMutationError(err, request.MessageID)
	}
	return commandReceipt(result, CommandDelete, request.RequestID, request.MessageID)
}

// ApproveTool idempotently invokes the durable ApproveTool command.
func (client *Client) ApproveTool(ctx context.Context, flowID FlowID, request ToolApprovalRequest) (CommandReceipt, error) {
	if err := validateFlowID(flowID); err != nil {
		return CommandReceipt{}, err
	}
	if err := validateRequestID(request.RequestID); err != nil {
		return CommandReceipt{}, err
	}
	instance := durableCommandInstance(CommandApproveTool, request.RequestID)
	var result durableCommandResult
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ApproveTool, request, &result, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes:  []dex.AttributeLock{dex.LockAttributeMap(durableCommandsAttribute, instance)},
		LoadAttributeMapInstances: []dex.AttributeMapLoad{
			durableCommandsAttribute.Load(instance),
		},
	}); err != nil {
		return CommandReceipt{}, err
	}
	return commandReceipt(result, CommandApproveTool, request.RequestID, "")
}

// ExecutePlan idempotently invokes the durable ExecutePlan command.
func (client *Client) ExecutePlan(ctx context.Context, flowID FlowID, request PlanExecutionRequest) (CommandReceipt, error) {
	if err := validateFlowID(flowID); err != nil {
		return CommandReceipt{}, err
	}
	if err := validateRequestID(request.RequestID); err != nil {
		return CommandReceipt{}, err
	}
	instance := durableCommandInstance(CommandExecutePlan, request.RequestID)
	var result durableCommandResult
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ExecutePlan, request, &result, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes:  []dex.AttributeLock{dex.LockAttributeMap(durableCommandsAttribute, instance)},
		LoadAttributeMapInstances: []dex.AttributeMapLoad{
			durableCommandsAttribute.Load(instance),
		},
	}); err != nil {
		return CommandReceipt{}, err
	}
	return commandReceipt(result, CommandExecutePlan, request.RequestID, "")
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

func (client *Client) waitForInitialization(ctx context.Context, flowID FlowID) error {
	var initialized bool
	return client.sdk.WaitForAttributeMatch(
		ctx,
		string(flowID),
		agentInitializedAttribute,
		dex.AttributeMatchEqual(true),
		&initialized,
	)
}

func (client *Client) verifyStartIdentity(
	ctx context.Context,
	flowID FlowID,
	request EnsureStartRequest,
) error {
	var persistedConfig AgentConfig
	found, err := client.sdk.GetAttribute(ctx, string(flowID), agentConfigAttribute, &persistedConfig)
	if err != nil {
		return fmt.Errorf("read persisted Agent config: %w", err)
	}
	if !found {
		return errors.New("agent config is not initialized")
	}
	var persistedContext string
	found, err = client.sdk.GetAttribute(ctx, string(flowID), applicationContextAttribute, &persistedContext)
	if err != nil {
		return fmt.Errorf("read persisted application context: %w", err)
	}
	if !found {
		return &StartIdentityConflictError{FlowID: flowID}
	}
	if !sameAgentConfig(persistedConfig, request.Config) || persistedContext != request.ApplicationContext {
		return &StartIdentityConflictError{FlowID: flowID}
	}
	return nil
}

func (client *Client) confirmStart(
	ctx context.Context,
	flowID FlowID,
	requestID RequestID,
	fingerprint string,
) (CommandReceipt, error) {
	instance := durableCommandInstance(CommandStart, requestID)
	var result durableCommandResult
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ConfirmStart, confirmStartRequest{
		RequestID:   requestID,
		Fingerprint: fingerprint,
	}, &result, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
		LockAttributes:  []dex.AttributeLock{dex.LockAttributeMap(durableCommandsAttribute, instance)},
		LoadAttributeMapInstances: []dex.AttributeMapLoad{
			durableCommandsAttribute.Load(instance),
		},
	}); err != nil {
		return CommandReceipt{}, err
	}
	return commandReceipt(result, CommandStart, requestID, "")
}

func sameAgentConfig(left AgentConfig, right AgentConfig) bool {
	return left.Model == right.Model &&
		optionalModelEqual(left.CompactionModel, right.CompactionModel) &&
		left.SystemPrompt == right.SystemPrompt &&
		left.MaxContextTokens == right.MaxContextTokens &&
		left.CompactionTriggerFraction == right.CompactionTriggerFraction &&
		left.CompactionKeepFraction == right.CompactionKeepFraction &&
		left.MessageRetentionLimit == right.MessageRetentionLimit &&
		left.MCPEnabled == right.MCPEnabled &&
		slices.Equal(left.EnabledMCPServers, right.EnabledMCPServers) &&
		slices.Equal(left.EnabledTools, right.EnabledTools)
}

func optionalModelEqual(left *Model, right *Model) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func flowStartRequestID(flowID FlowID, requestID RequestID) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s", flowID, requestID)))
	return fmt.Sprintf("agent-start:%x", digest)
}

func initialMessageRequestID(requestID RequestID) RequestID {
	digest := sha256.Sum256([]byte(requestID))
	return RequestID(fmt.Sprintf("agent-initial:%x", digest))
}

func commandReceipt(
	result durableCommandResult,
	command Command,
	requestID RequestID,
	messageID MessageID,
) (CommandReceipt, error) {
	if result.Disposition == durableCommandIdempotencyConflict {
		return CommandReceipt{}, &CommandIdempotencyConflictError{Command: command, RequestID: requestID}
	}
	if result.Disposition != durableCommandCommitted && result.Disposition != durableCommandReplayed {
		return CommandReceipt{}, fmt.Errorf("agent returned unknown %s disposition %q", command, result.Disposition)
	}
	if result.Record.RequestID != requestID || result.Record.Command != command {
		return CommandReceipt{}, fmt.Errorf("agent returned an invalid %s receipt identity", command)
	}
	switch result.Record.Outcome {
	case durableCommandAccepted:
		if result.Record.RecordedAt.IsZero() {
			return CommandReceipt{}, fmt.Errorf("agent returned an invalid %s acceptance timestamp", command)
		}
		return CommandReceipt{
			RequestID:  requestID,
			AcceptedAt: result.Record.RecordedAt,
			IsReplay:   result.Disposition == durableCommandReplayed || result.Record.IsEffectReplay,
		}, nil
	case durableCommandMessageConflict:
		return CommandReceipt{}, &MessageIdempotencyConflictError{MessageID: messageID}
	case durableCommandNotFound:
		return CommandReceipt{}, &PendingMessageNotFoundError{MessageID: messageID}
	case durableCommandRejected:
		return CommandReceipt{}, &CommandRejectedError{Command: command}
	default:
		return CommandReceipt{}, fmt.Errorf("agent returned unknown %s outcome %q", command, result.Record.Outcome)
	}
}

func (client *Client) reconcileInactiveCancellation(
	ctx context.Context,
	flowID FlowID,
	requestID RequestID,
) (CancellationReceipt, error) {
	record, found, err := client.readDurableCommand(ctx, flowID, CommandCancel, requestID)
	if err != nil {
		return CancellationReceipt{}, err
	}
	if !found {
		return client.alreadyTerminalCancellation(ctx, flowID)
	}
	if record.Outcome != durableCommandAccepted {
		return CancellationReceipt{}, &CommandRejectedError{Command: CommandCancel}
	}
	return client.waitForCancellation(ctx, flowID, CommandReceipt{
		RequestID:  requestID,
		AcceptedAt: record.RecordedAt,
		IsReplay:   true,
	})
}

func (client *Client) readDurableCommand(
	ctx context.Context,
	flowID FlowID,
	command Command,
	requestID RequestID,
) (durableCommandRecord, bool, error) {
	var record durableCommandRecord
	found, err := client.sdk.GetAttributeMapInstance(
		ctx,
		string(flowID),
		durableCommandsAttribute,
		durableCommandInstance(command, requestID),
		&record,
	)
	if err != nil || !found {
		return durableCommandRecord{}, found, err
	}
	if record.RequestID != requestID || record.Command != command {
		return durableCommandRecord{}, false, &CommandIdempotencyConflictError{Command: command, RequestID: requestID}
	}
	return record, true, nil
}

func (client *Client) waitForCancellation(
	ctx context.Context,
	flowID FlowID,
	receipt CommandReceipt,
) (CancellationReceipt, error) {
	result, err := client.sdk.WaitForFlow(ctx, string(flowID), dex.WaitForFlowOptions{})
	if err != nil {
		return CancellationReceipt{}, err
	}
	status, err := flowStatusFromDex(result.Status)
	if err != nil {
		return CancellationReceipt{}, err
	}
	if status != FlowStatusCanceled {
		return CancellationReceipt{}, &AgentAlreadyTerminalError{FlowID: flowID, Status: status}
	}
	return CancellationReceipt{
		RequestID:  receipt.RequestID,
		AcceptedAt: receipt.AcceptedAt,
		FlowStatus: status,
		IsReplay:   receipt.IsReplay,
	}, nil
}

func (client *Client) alreadyTerminalCancellation(
	ctx context.Context,
	flowID FlowID,
) (CancellationReceipt, error) {
	result, err := client.sdk.WaitForFlow(ctx, string(flowID), dex.WaitForFlowOptions{})
	if err != nil {
		return CancellationReceipt{}, err
	}
	status, err := flowStatusFromDex(result.Status)
	if err != nil {
		return CancellationReceipt{}, err
	}
	return CancellationReceipt{}, &AgentAlreadyTerminalError{FlowID: flowID, Status: status}
}

func pendingMessageMutationError(err error, messageID MessageID) error {
	var notFound *dex.ChannelMessageNotFoundError
	if errors.As(err, &notFound) {
		return &PendingMessageNotFoundError{MessageID: messageID}
	}
	return err
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

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

	"github.com/google/uuid"
	"github.com/superdurable/dex/sdk-go/dex"
)

const (
	defaultCommandTimeout  = 20 * time.Second
	defaultEventPoll       = 20 * time.Second
	defaultSnapshotTimeout = 5 * time.Second
	// MaximumRecentEventLimit matches Dex's default maximum Stream list page size.
	MaximumRecentEventLimit = 1_000
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
	runID, err := client.sdk.StartFlow(
		ctx,
		client.flow,
		string(flowID),
		request.Config,
		newAgentStartFlowOptions(initialMetadata),
	)
	if err != nil {
		return "", err
	}
	if _, err := client.WaitForWaitingInputRound(ctx, flowID, 0); err != nil {
		return "", fmt.Errorf("wait for initial Agent input round: %w", err)
	}
	return RunID(runID), nil
}

func newAgentStartFlowOptions(initialMetadata dex.InitialAttributeDef) dex.StartFlowOptions {
	durability := dex.StepDurabilityAsync
	return dex.StartFlowOptions{
		IDReusePolicy: dex.IDReuseDisallow,
		Attributes:    []dex.InitialAttributeDef{initialMetadata},
		ConfigOverride: &dex.FlowConfig{
			StepDurability: &durability,
		},
	}
}

// SendMessage invokes the durable SendMessage command.
func (client *Client) SendMessage(ctx context.Context, flowID FlowID, message UserMessage) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if err := validateNewUserMessage(message); err != nil {
		return err
	}
	pending := PendingUserMessage{MessageID: MessageID(uuid.NewString()), Value: message}
	accepted, err := invokeLockedCommand(ctx, client.commandTimeout, func(ctx context.Context, accepted *bool) error {
		return client.sdk.InvokeRPC(ctx, string(flowID), client.flow.SendMessage, pending, accepted)
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
		return client.sdk.InvokeRPC(ctx, string(flowID), client.flow.AnswerQuestions, request, accepted)
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
	accepted, err := invokeLockedCommand(ctx, client.commandTimeout, func(ctx context.Context, accepted *bool) error {
		return client.sdk.InvokeRPC(ctx, string(flowID), client.flow.SteerMessage, request, accepted)
	})
	if err != nil {
		return err
	}
	if !accepted {
		return &PendingMessageNotFoundError{MessageID: request.MessageID}
	}
	return nil
}

// GetSnapshot reads one atomic durable application view.
func (client *Client) GetSnapshot(
	ctx context.Context,
	flowID FlowID,
) (AgentSnapshot, error) {
	if err := validateFlowID(flowID); err != nil {
		return AgentSnapshot{}, err
	}
	var snapshot AgentSnapshot
	err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.GetSnapshot, nil, &snapshot)
	return snapshot, err
}

// GetArchivedMessages reads exactly one immutable history chunk before a sequence boundary.
func (client *Client) GetArchivedMessages(ctx context.Context, flowID FlowID, before Sequence) (HistoryPage, error) {
	if err := validateFlowID(flowID); err != nil {
		return HistoryPage{}, err
	}
	if _, isValid := archivedMessageChunkFirst(before); !isValid {
		return HistoryPage{}, fmt.Errorf("before sequence must identify a %d-message boundary", archiveMessageChunkSize)
	}
	var result archivedMessagesRPCOutput
	err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.GetArchivedMessages, before, &result)
	if err != nil {
		return HistoryPage{}, err
	}
	if !result.Found {
		return HistoryPage{}, &ArchivedMessagesNotFoundError{BeforeSequence: before}
	}
	return result.Page, nil
}

// GetArchivedMessageRange reads up to limit archived messages before a sequence boundary.
func (client *Client) GetArchivedMessageRange(
	ctx context.Context,
	flowID FlowID,
	before Sequence,
	limit int,
) (HistoryPage, error) {
	if limit < archiveMessageChunkSize || limit > 200 || limit%archiveMessageChunkSize != 0 {
		return HistoryPage{}, fmt.Errorf("limit must be between %d and 200 in increments of %d", archiveMessageChunkSize, archiveMessageChunkSize)
	}
	messages := make([]SequencedMessage, 0, limit)
	nextBefore := &before
	for len(messages) < limit && nextBefore != nil {
		page, err := client.GetArchivedMessages(ctx, flowID, *nextBefore)
		if err != nil {
			return HistoryPage{}, err
		}
		messages = append(page.Messages, messages...)
		nextBefore = page.NextBeforeSequence
	}
	return HistoryPage{Messages: messages, NextBeforeSequence: nextBefore}, nil
}

// WaitForWaitingInputRound blocks until the durable watermark advances.
func (client *Client) WaitForWaitingInputRound(
	ctx context.Context,
	flowID FlowID,
	after WaitingInputRound,
) (WaitingInputRound, error) {
	if err := validateFlowID(flowID); err != nil {
		return 0, err
	}
	if after < 0 || after > MaximumWaitingInputRound {
		return 0, fmt.Errorf("after waiting input round must be between 0 and %d", MaximumWaitingInputRound)
	}
	var matched WaitingInputRound
	err := client.sdk.WaitForAttributeMatch(
		ctx,
		string(flowID),
		waitingInputRoundAttribute,
		dex.AttributeMatchGreaterThan(after),
		&matched,
		dex.WaitForAttributeOptions{},
	)
	return matched, err
}

// DeleteQueuedMessage removes one exact pending user message.
func (client *Client) DeleteQueuedMessage(ctx context.Context, flowID FlowID, messageID MessageID) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if err := validateMessageID(messageID); err != nil {
		return err
	}
	var deleted bool
	if err := client.sdk.InvokeRPC(
		ctx,
		string(flowID),
		client.flow.DeleteQueuedMessage,
		messageID,
		&deleted,
	); err != nil {
		return err
	}
	if !deleted {
		return &PendingMessageNotFoundError{MessageID: messageID}
	}
	return nil
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
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ApproveTool, request, &accepted); err != nil {
		return err
	}
	return ensureAccepted(accepted, CommandApproveTool)
}

// ResolveToolRecovery invokes the durable command for one exact recovery revision.
func (client *Client) ResolveToolRecovery(
	ctx context.Context,
	flowID FlowID,
	request ResolveToolRecoveryRequest,
) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if strings.TrimSpace(string(request.RecoveryID)) == "" {
		return errors.New("recovery ID must not be empty")
	}
	if err := request.Resolution.Validate(); err != nil {
		return err
	}
	for _, decision := range request.Decisions {
		if strings.TrimSpace(string(decision.CallID)) == "" {
			return errors.New("recovery decision call ID must not be empty")
		}
		if err := decision.Action.Validate(); err != nil {
			return err
		}
	}
	accepted, err := invokeLockedCommand(ctx, client.commandTimeout, func(ctx context.Context, accepted *bool) error {
		return client.sdk.InvokeRPC(
			ctx,
			string(flowID),
			client.flow.ResolveToolRecovery,
			request,
			accepted,
		)
	})
	if err != nil {
		return err
	}
	return ensureAccepted(accepted, CommandResolveToolRecovery)
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
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ExecutePlan, request, &accepted); err != nil {
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

// ListRecentEvents reads one bounded chronological page from a best-effort Stream.
func (client *Client) ListRecentEvents(
	ctx context.Context,
	flowID FlowID,
	stream EventStream,
	limit int,
) ([]StreamEvent, error) {
	if err := validateFlowID(flowID); err != nil {
		return nil, err
	}
	if err := stream.Validate(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaximumRecentEventLimit {
		return nil, fmt.Errorf("recent event limit must be between 1 and %d", MaximumRecentEventLimit)
	}
	switch stream {
	case EventStreamReasoning:
		var page dex.StreamMessagesPage[string]
		if err := client.sdk.ListStreamMessages(
			ctx, string(flowID), reasoningSummaryStream, int32(limit), "", &page,
		); err != nil {
			return nil, err
		}
		return recentTextStreamEvents(StreamEventKindReasoning, page.Messages), nil
	case EventStreamAssistant:
		var page dex.StreamMessagesPage[string]
		if err := client.sdk.ListStreamMessages(
			ctx, string(flowID), assistantTextStream, int32(limit), "", &page,
		); err != nil {
			return nil, err
		}
		return recentTextStreamEvents(StreamEventKindAssistant, page.Messages), nil
	case EventStreamActivity:
		var page dex.StreamMessagesPage[AgentEvent]
		if err := client.sdk.ListStreamMessages(
			ctx, string(flowID), agentActivityStream, int32(limit), "", &page,
		); err != nil {
			return nil, err
		}
		events := make([]StreamEvent, 0, len(page.Messages))
		for index := len(page.Messages) - 1; index >= 0; index-- {
			message := page.Messages[index]
			events = append(events, StreamEvent{
				Kind:        StreamEventKindActivity,
				Activity:    message.Value,
				ResumeToken: ResumeToken(message.ResumeToken),
				CreatedAt:   message.CreatedTime,
				Source:      message.Source,
			})
		}
		return events, nil
	default:
		return nil, fmt.Errorf("unsupported event Stream %q", stream)
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

func recentTextStreamEvents(
	kind StreamEventKind,
	messages []dex.ListedStreamMessage[string],
) []StreamEvent {
	events := make([]StreamEvent, 0, len(messages))
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		events = append(events, StreamEvent{
			Kind:        kind,
			Text:        message.Value,
			ResumeToken: ResumeToken(message.ResumeToken),
			CreatedAt:   message.CreatedTime,
			Source:      message.Source,
		})
	}
	return events
}

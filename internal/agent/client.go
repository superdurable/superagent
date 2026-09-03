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
	defaultSnapshotLimit  = 50
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
func (client *Client) Start(ctx context.Context, flowID FlowID, config AgentConfig) (RunID, error) {
	if strings.TrimSpace(string(flowID)) == "" {
		return "", errors.New("flow ID must not be empty")
	}
	if err := client.flow.validateConfig(config); err != nil {
		return "", fmt.Errorf("validate Agent config: %w", err)
	}
	requestID := "start:" + string(flowID)
	runID, err := client.sdk.StartFlow(ctx, client.flow, string(flowID), config, dex.StartFlowOptions{
		IDReusePolicy: dex.IDReuseDisallow,
		RequestID:     &requestID,
	})
	if err != nil {
		return "", err
	}
	return RunID(runID), nil
}

// SendMessage invokes the durable SendMessage command.
func (client *Client) SendMessage(ctx context.Context, flowID FlowID, message UserMessage) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	var accepted bool
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.SendMessage, message, &accepted, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
	}); err != nil {
		return err
	}
	return ensureAccepted(accepted, CommandSendMessage)
}

// SteerMessage invokes the durable SteerMessage command.
func (client *Client) SteerMessage(ctx context.Context, flowID FlowID, request SteerMessageRequest) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if strings.TrimSpace(string(request.MessageID)) == "" {
		return errors.New("message ID must not be empty")
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
	request SnapshotRequest,
) (AgentSnapshot, error) {
	if err := validateFlowID(flowID); err != nil {
		return AgentSnapshot{}, err
	}
	if request.Limit == 0 {
		request.Limit = defaultSnapshotLimit
	}
	if request.Limit < minimumSnapshotPageSize || request.Limit > maximumSnapshotPageSize {
		return AgentSnapshot{}, fmt.Errorf("snapshot limit must be between %d and %d", minimumSnapshotPageSize, maximumSnapshotPageSize)
	}
	if request.BeforeSequence != nil && *request.BeforeSequence < 1 {
		return AgentSnapshot{}, errors.New("before sequence must be positive")
	}
	var snapshot AgentSnapshot
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.Snapshot, request, &snapshot, dex.InvokeOptions{
		Timeout:           client.commandTimeout,
		LoadAttributeMaps: []dex.AttributeDef{agentMessagesAttribute},
		LoadChannels: []dex.ChannelDef{
			queuedUserMessagesChannel,
			steeredUserMessagesChannel,
		},
	}); err != nil {
		return AgentSnapshot{}, err
	}
	return snapshot, nil
}

// DeleteQueuedMessage removes one exact pending user message.
func (client *Client) DeleteQueuedMessage(ctx context.Context, flowID FlowID, messageID MessageID) error {
	if err := validateFlowID(flowID); err != nil {
		return err
	}
	if strings.TrimSpace(string(messageID)) == "" {
		return errors.New("message ID must not be empty")
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
	var accepted bool
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ApproveTool, request, &accepted, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
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
	var accepted bool
	if err := client.sdk.InvokeRPC(ctx, string(flowID), client.flow.ExecutePlan, request, &accepted, dex.InvokeOptions{
		Timeout:         client.commandTimeout,
		IsTransactional: true,
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

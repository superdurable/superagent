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

	"github.com/superdurable/dex/sdk-go/dex"
)

const (
	defaultForwardHistoryLimitForTestOnly = 100
	maximumForwardHistoryLimitForTestOnly = 200
)

// ForwardHistoryPageForTestOnly supports retained-history integration assertions.
type ForwardHistoryPageForTestOnly struct {
	Messages              []SequencedMessage
	NextAfterSequence     *Sequence
	FirstRetainedSequence Sequence
	LastSequence          Sequence
	IsTruncated           bool
}

type getMessagesAfterInputForTestOnly struct {
	After Sequence
	Limit int
}

// FlowStateForTestOnly exposes exact durable state to released-server integration assertions.
type FlowStateForTestOnly struct {
	State           AgentState
	HasState        bool
	Plan            *AgentPlan
	PendingApproval *PendingApproval
	PendingInput    *PendingUserInput
	PendingTimer    *PendingTimer
	Queued          []dex.ChannelMessage[UserMessage]
}

// GetFlowStateForTestOnly returns exact durable state without mutating the Flow.
func (*Flow) GetFlowStateForTestOnly(
	ctx dex.Context,
	_ dex.None,
) (*dex.RPCResult[FlowStateForTestOnly], error) {
	result := FlowStateForTestOnly{}
	state, err := agentStateAttribute.Get(ctx)
	if err == nil {
		result.State = state
		result.HasState = true
	} else if !isAttributeNotFound(err) {
		return nil, err
	}
	result.Plan, err = getAgentPlan(ctx)
	if err != nil {
		return nil, err
	}
	result.PendingApproval, err = getPendingApproval(ctx)
	if err != nil {
		return nil, err
	}
	result.PendingInput, err = getPendingUserInput(ctx)
	if err != nil {
		return nil, err
	}
	result.PendingTimer, err = getPendingTimer(ctx)
	if err != nil {
		return nil, err
	}
	result.Queued, err = queuedUserMessagesChannel.PendingMessages(ctx)
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[FlowStateForTestOnly]{Output: result}, nil
}

// GetFlowStateForTestOnly reads exact durable state for integration assertions.
func (client *Client) GetFlowStateForTestOnly(
	ctx context.Context,
	flowID FlowID,
) (FlowStateForTestOnly, error) {
	if err := validateFlowID(flowID); err != nil {
		return FlowStateForTestOnly{}, err
	}
	var result FlowStateForTestOnly
	err := client.sdk.InvokeRPC(
		ctx,
		string(flowID),
		client.flow.GetFlowStateForTestOnly,
		nil,
		&result,
		dex.InvokeOptions{
			Timeout:      client.commandTimeout,
			LoadChannels: []dex.ChannelDef{queuedUserMessagesChannel},
		},
	)
	return result, err
}

// GetPlanExecutionMessagesForTestOnly returns pending requests for one Plan revision.
func (*Flow) GetPlanExecutionMessagesForTestOnly(
	ctx dex.Context,
	revision PlanRevision,
) (*dex.RPCResult[[]dex.ChannelMessage[PlanExecutionRequest]], error) {
	messages, err := planExecutionsChannel.PendingMessages(ctx, planRevisionKey(revision))
	if err != nil {
		return nil, err
	}
	return &dex.RPCResult[[]dex.ChannelMessage[PlanExecutionRequest]]{Output: messages}, nil
}

// GetPlanExecutionMessagesForTestOnly reads pending requests for one Plan revision.
func (client *Client) GetPlanExecutionMessagesForTestOnly(
	ctx context.Context,
	flowID FlowID,
	revision PlanRevision,
) ([]dex.ChannelMessage[PlanExecutionRequest], error) {
	if err := validateFlowID(flowID); err != nil {
		return nil, err
	}
	var messages []dex.ChannelMessage[PlanExecutionRequest]
	err := client.sdk.InvokeRPC(
		ctx,
		string(flowID),
		client.flow.GetPlanExecutionMessagesForTestOnly,
		revision,
		&messages,
		dex.InvokeOptions{
			Timeout: client.commandTimeout,
			LoadChannelMapInstances: []dex.ChannelMapLoad{
				planExecutionsChannel.LoadMessages(planRevisionKey(revision)),
			},
		},
	)
	return messages, err
}

// GetMessagesAfterForTestOnly returns retained canonical history after an exclusive cursor.
func (*Flow) GetMessagesAfterForTestOnly(
	ctx dex.Context,
	input getMessagesAfterInputForTestOnly,
) (*dex.RPCResult[ForwardHistoryPageForTestOnly], error) {
	if input.After < 0 {
		return nil, errors.New("after sequence must not be negative")
	}
	if input.Limit < 1 || input.Limit > maximumForwardHistoryLimitForTestOnly {
		return nil, fmt.Errorf(
			"limit must be between 1 and %d",
			maximumForwardHistoryLimitForTestOnly,
		)
	}
	state, err := agentStateAttribute.Get(ctx)
	if isAttributeNotFound(err) {
		return nil, errors.New("agent state is not initialized")
	}
	if err != nil {
		return nil, err
	}
	page := ForwardHistoryPageForTestOnly{
		Messages:              []SequencedMessage{},
		FirstRetainedSequence: state.FirstRetainedSequence,
		LastSequence:          state.LastSequence,
	}
	if input.After >= state.LastSequence {
		return &dex.RPCResult[ForwardHistoryPageForTestOnly]{Output: page}, nil
	}
	start := max(input.After+1, state.FirstRetainedSequence)
	end := state.LastSequence
	if available := state.LastSequence - start + 1; available > Sequence(input.Limit) {
		end = start + Sequence(input.Limit) - 1
	}
	archiveCache := make(map[Sequence]ArchivedMessageChunk)
	page.Messages = make([]SequencedMessage, 0, int(end-start+1))
	for sequence := start; sequence <= end; sequence++ {
		message, readErr := readCanonicalMessageForTestOnly(ctx, state, sequence, archiveCache)
		if readErr != nil {
			return nil, readErr
		}
		page.Messages = append(page.Messages, SequencedMessage{Sequence: sequence, Message: message})
	}
	if end < state.LastSequence {
		next := end
		page.NextAfterSequence = &next
		page.IsTruncated = true
	}
	return &dex.RPCResult[ForwardHistoryPageForTestOnly]{Output: page}, nil
}

// GetMessagesAfterForTestOnly probes retained canonical history after an exclusive cursor.
func (client *Client) GetMessagesAfterForTestOnly(
	ctx context.Context,
	flowID FlowID,
	after Sequence,
	limit int,
) (ForwardHistoryPageForTestOnly, error) {
	if err := validateFlowID(flowID); err != nil {
		return ForwardHistoryPageForTestOnly{}, err
	}
	if after < 0 {
		return ForwardHistoryPageForTestOnly{}, errors.New("after sequence must not be negative")
	}
	if limit == 0 {
		limit = defaultForwardHistoryLimitForTestOnly
	}
	if limit < 1 || limit > maximumForwardHistoryLimitForTestOnly {
		return ForwardHistoryPageForTestOnly{}, fmt.Errorf(
			"limit must be between 1 and %d",
			maximumForwardHistoryLimitForTestOnly,
		)
	}
	var page ForwardHistoryPageForTestOnly
	err := client.sdk.InvokeRPC(
		ctx,
		string(flowID),
		client.flow.GetMessagesAfterForTestOnly,
		getMessagesAfterInputForTestOnly{After: after, Limit: limit},
		&page,
		dex.InvokeOptions{
			Timeout: client.commandTimeout,
			LoadAttributeMaps: []dex.AttributeDef{
				currentMessagesAttribute,
				archivedMessagesAttribute,
			},
		},
	)
	return page, err
}

func readCanonicalMessageForTestOnly(
	ctx dex.Context,
	state AgentState,
	sequence Sequence,
	archiveCache map[Sequence]ArchivedMessageChunk,
) (AgentMessage, error) {
	if sequence >= state.CurrentFirstSequence {
		var message AgentMessage
		message, err := currentMessagesAttribute.Get(ctx, sequenceKey(sequence))
		if err == nil {
			return message, nil
		}
		if !isAttributeNotFound(err) {
			return AgentMessage{}, err
		}
	}
	first := ((sequence - 1) / Sequence(archiveMessageChunkSize) * Sequence(archiveMessageChunkSize)) + 1
	chunk, found := archiveCache[first]
	if !found {
		var err error
		chunk, err = archivedMessagesAttribute.Get(ctx, sequenceKey(first))
		if isAttributeNotFound(err) {
			return AgentMessage{}, fmt.Errorf("canonical message %d is no longer retained", sequence)
		}
		if err != nil {
			return AgentMessage{}, err
		}
		archiveCache[first] = chunk
	}
	index := sequence - first
	if index < 0 || index >= Sequence(len(chunk.Messages)) || chunk.Messages[index].Sequence != sequence {
		return AgentMessage{}, fmt.Errorf("canonical message %d is no longer retained", sequence)
	}
	return chunk.Messages[index].Message, nil
}

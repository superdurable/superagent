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

// MessagesAfterForTestOnly probes retained canonical history after an exclusive cursor.
func (client *Client) MessagesAfterForTestOnly(
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
	var state AgentState
	found, err := client.sdk.GetAttribute(ctx, string(flowID), agentStateAttribute, &state)
	if err != nil {
		return ForwardHistoryPageForTestOnly{}, err
	}
	if !found {
		return ForwardHistoryPageForTestOnly{}, errors.New("agent state is not initialized")
	}
	page := ForwardHistoryPageForTestOnly{
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
		message, readErr := client.readCanonicalMessageForTestOnly(ctx, flowID, state, sequence, archiveCache)
		if readErr != nil {
			return ForwardHistoryPageForTestOnly{}, readErr
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

func (client *Client) readCanonicalMessageForTestOnly(
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
			return AgentMessage{}, fmt.Errorf("canonical message %d is no longer retained", sequence)
		}
		archiveCache[first] = chunk
	}
	index := sequence - first
	if index < 0 || index >= Sequence(len(chunk.Messages)) || chunk.Messages[index].Sequence != sequence {
		return AgentMessage{}, fmt.Errorf("canonical message %d is no longer retained", sequence)
	}
	return chunk.Messages[index].Message, nil
}

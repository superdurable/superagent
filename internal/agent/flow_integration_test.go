//go:build integration

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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/superdurable/dex/blob-cache-go/blobcache"
	"github.com/superdurable/dex/sdk-go/dex"
)

const integrationToolName ToolName = "integration_tool"

const integrationWaitTimeout = 30 * time.Second

func TestAgentFlowDurabilityIntegration(t *testing.T) {
	modelClient := integrationModel{}
	toolRegistry := newIntegrationToolRegistry()
	environment := newAgentIntegrationEnvironment(t, modelClient, toolRegistry)
	flowID := FlowID("agent-integration-" + randomLocalID(t))
	config := NewAgentConfig()
	config.MaxContextTokens = 80
	config.CompactionTriggerFraction = 0.60
	config.CompactionKeepFraction = 0.20
	config.MessageRetentionLimit = 20
	runtimeMetadata := MustJSONObject(`{"resource_id":"resource-1","tenant":"integration"}`)

	runID, err := environment.agent.Start(t.Context(), flowID, StartRequest{
		Config:          config,
		RuntimeMetadata: runtimeMetadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runID == "" {
		t.Fatal("run ID is empty")
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	initialSnapshot := readSnapshot(t, environment, flowID)
	if initialSnapshot.RunID != runID {
		t.Fatalf("Snapshot run ID = %q, want %q", initialSnapshot.RunID, runID)
	}
	if initialSnapshot.Description == nil ||
		initialSnapshot.Description.Status != AgentStatusWaitingForMessage ||
		len(initialSnapshot.History.Messages) != 0 ||
		len(initialSnapshot.Queued) != 0 ||
		len(initialSnapshot.Steered) != 0 {
		t.Fatalf("initial Snapshot = %#v", initialSnapshot)
	}

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	state := waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && state.LastSequence >= 2
	})
	assertApplicationMessage(t, environment, flowID, state.LastSequence-1, MessageRoleUser, "hello")
	assertApplicationMessage(t, environment, flowID, state.LastSequence, MessageRoleAssistant, "integration response: hello")
	latestSnapshot := readSnapshot(t, environment, flowID)
	if len(latestSnapshot.History.Messages) != 2 ||
		latestSnapshot.History.Messages[0].Sequence != state.LastSequence-1 ||
		latestSnapshot.History.Messages[0].Message.Role != MessageRoleUser ||
		latestSnapshot.History.Messages[1].Sequence != state.LastSequence ||
		latestSnapshot.History.Messages[1].Message.Role != MessageRoleAssistant ||
		latestSnapshot.History.NextBeforeSequence != nil {
		t.Fatalf("latest Snapshot history = %#v", latestSnapshot.History)
	}
	assertTextStream(t, environment.agent, flowID, EventStreamAssistant, "integration response: hello")
	assertTextStream(t, environment.agent, flowID, EventStreamReasoning, "deterministic integration summary")
	assertModelActivity(t, environment.agent, flowID, state.LastSequence)

	environment.replaceWorker(t, flowID)
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
		t.Fatal(err)
	}
	approval := waitForPendingApproval(t, environment, flowID)
	if approval.ToolName != integrationToolName {
		t.Fatalf("pending tool = %q", approval.ToolName)
	}
	environment.replaceWorker(t, flowID)
	recoveredApproval := waitForPendingApproval(t, environment, flowID)
	if recoveredApproval.CallID != approval.CallID || recoveredApproval.Arguments != approval.Arguments {
		t.Fatalf("approval changed across Worker replacement: got %#v, want %#v", recoveredApproval, approval)
	}
	if err := environment.agent.ApproveTool(t.Context(), flowID, ToolApprovalRequest{
		CallID: approval.CallID, Approved: true,
	}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && len(state.PendingToolCalls) == 0
	})
	duplicateApprovalErr := environment.agent.ApproveTool(t.Context(), flowID, ToolApprovalRequest{
		CallID: approval.CallID, Approved: true,
	})
	var duplicateApproval *CommandRejectedError
	if !errors.As(duplicateApprovalErr, &duplicateApproval) || duplicateApproval.Command != CommandApproveTool {
		t.Fatalf("duplicate approval error = %T %v", duplicateApprovalErr, duplicateApprovalErr)
	}
	toolRegistry.assertCallsUseID(t, approval.CallID)
	toolRegistry.assertInvocation(t, flowID, approval.CallID, runtimeMetadata)

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
		t.Fatal(err)
	}
	rejectedApproval := waitForPendingApproval(t, environment, flowID)
	staleApprovalErr := environment.agent.ApproveTool(t.Context(), flowID, ToolApprovalRequest{
		CallID: approval.CallID, Approved: false,
	})
	var staleApproval *CommandRejectedError
	if !errors.As(staleApprovalErr, &staleApproval) || staleApproval.Command != CommandApproveTool {
		t.Fatalf("stale approval error = %T %v", staleApprovalErr, staleApprovalErr)
	}
	if err := environment.agent.ApproveTool(t.Context(), flowID, ToolApprovalRequest{
		CallID: rejectedApproval.CallID, Approved: false,
	}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && len(state.PendingToolCalls) == 0
	})
	toolRegistry.assertExecutionCount(t, 1)
	if snapshot := readSnapshot(t, environment, flowID); !historyContainsText(
		snapshot.History.Messages,
		string(toolErrorRejectedByUser),
	) {
		t.Fatalf("rejected tool result is missing from history: %#v", snapshot.History.Messages)
	}

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content: "ship safely", PlanMode: true,
	}); err != nil {
		t.Fatal(err)
	}
	plan := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	stalePlanErr := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{Revision: plan.Revision + 1})
	var stalePlan *CommandRejectedError
	if !errors.As(stalePlanErr, &stalePlan) || stalePlan.Command != CommandExecutePlan {
		t.Fatalf("stale plan execution error = %T %v", stalePlanErr, stalePlanErr)
	}
	if err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{Revision: plan.Revision}); err != nil {
		t.Fatal(err)
	}
	waitForAgentPlan(t, environment, flowID, PlanStatusCompleted)
	completedPlanErr := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{Revision: plan.Revision})
	var completedPlan *CommandRejectedError
	if !errors.As(completedPlanErr, &completedPlan) || completedPlan.Command != CommandExecutePlan {
		t.Fatalf("completed plan execution error = %T %v", completedPlanErr, completedPlanErr)
	}

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/ask deployment region"}); err != nil {
		t.Fatal(err)
	}
	pendingInput := waitForPendingUserInput(t, environment, flowID)
	environment.replaceWorker(t, flowID)
	if err := environment.agent.AnswerQuestions(t.Context(), flowID, answerRequest(pendingInput, "us-west")); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingUserInput(t, environment, flowID)

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/wait"}); err != nil {
		t.Fatal(err)
	}
	waitForPendingTimer(t, environment, flowID)
	environment.replaceWorker(t, flowID)
	stateBeforeQueue := readAgentState(t, environment, flowID)
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "queued message"}); err != nil {
		t.Fatal(err)
	}
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "delete me"}); err != nil {
		t.Fatal(err)
	}
	queued := waitForQueuedMessages(t, environment, flowID, 2)
	if stateAfterQueue := readAgentState(t, environment, flowID); stateAfterQueue.LastSequence != stateBeforeQueue.LastSequence {
		t.Fatalf("queued message entered history: sequence advanced from %d to %d", stateBeforeQueue.LastSequence, stateAfterQueue.LastSequence)
	}
	firstQueueSnapshot := readSnapshot(t, environment, flowID)
	secondQueueSnapshot := readSnapshot(t, environment, flowID)
	if len(firstQueueSnapshot.Queued) != 2 || len(secondQueueSnapshot.Queued) != 2 {
		t.Fatalf("Snapshot queue lengths = %d/%d, want 2/2", len(firstQueueSnapshot.Queued), len(secondQueueSnapshot.Queued))
	}
	for index := range firstQueueSnapshot.Queued {
		if firstQueueSnapshot.Queued[index] != secondQueueSnapshot.Queued[index] {
			t.Fatalf("Snapshot queue changed at %d: %#v != %#v", index, firstQueueSnapshot.Queued[index], secondQueueSnapshot.Queued[index])
		}
	}
	if err := environment.agent.DeleteQueuedMessage(t.Context(), flowID, MessageID(queued[1].MessageID)); err != nil {
		t.Fatal(err)
	}
	waitForQueuedMessages(t, environment, flowID, 1)
	deleteErr := environment.agent.DeleteQueuedMessage(t.Context(), flowID, MessageID(queued[1].MessageID))
	var channelMessageNotFound *dex.ChannelMessageNotFoundError
	if !errors.As(deleteErr, &channelMessageNotFound) {
		t.Fatalf("repeated queue delete error = %T %v", deleteErr, deleteErr)
	}
	if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
		MessageID: MessageID(queued[0].MessageID),
	}); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingTimer(t, environment, flowID)
	waitForQueuedMessages(t, environment, flowID, 0)
	state = waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && state.LastSequence > stateBeforeQueue.LastSequence
	})
	if !historyContains(t, environment, flowID, state, MessageRoleUser, "queued message") {
		t.Fatal("steered message did not enter application history")
	}
	steerErr := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
		MessageID: MessageID(queued[0].MessageID),
	})
	var pendingMessageNotFound *PendingMessageNotFoundError
	if !errors.As(steerErr, &pendingMessageNotFound) {
		t.Fatalf("repeated queue steer error = %T %v", steerErr, steerErr)
	}
	state = waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.CompactionGeneration > 0
	})
	retained := state.LastSequence - state.FirstRetainedSequence + 1
	if retained > Sequence(config.MessageRetentionLimit) {
		t.Fatalf("retained messages = %d, limit = %d", retained, config.MessageRetentionLimit)
	}
	if state.FirstRetainedSequence > 1 && state.SummarizedThroughSequence < state.FirstRetainedSequence-1 {
		t.Fatalf("messages deleted before summary: state = %#v", state)
	}
}

func TestAgentInteractionStatusIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-interaction-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.WaitForInteractionStatus(t.Context(), flowID, AgentInteractionStatusWaiting); err != nil {
		t.Fatal(err)
	}

	submitted := make(chan error, 1)
	go func() {
		submitted <- environment.agent.WaitForInteractionStatus(t.Context(), flowID, AgentInteractionStatusSubmitted)
	}()
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "status cycle"}); err != nil {
		t.Fatal(err)
	}
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
	if err := environment.agent.WaitForInteractionStatus(t.Context(), flowID, AgentInteractionStatusWaiting); err != nil {
		t.Fatal(err)
	}
	snapshot := readSnapshot(t, environment, flowID)
	if snapshot.Description == nil || snapshot.Description.InteractionStatus != AgentInteractionStatusWaiting ||
		!historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: status cycle") {
		t.Fatalf("reconciled Snapshot = %#v", snapshot)
	}
}

func TestAgentMessageArchiveIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-archive-" + randomLocalID(t))
	config := NewAgentConfig()
	config.MaxContextTokens = 1_000_000
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: config}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})

	for index := 1; index <= 15; index++ {
		content := fmt.Sprintf("archive %02d", index)
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: content}); err != nil {
			t.Fatal(err)
		}
		lastSequence := Sequence(index * 2)
		waitForAgentState(t, environment, flowID, func(state AgentState) bool {
			return state.Status == AgentStatusWaitingForMessage && state.LastSequence >= lastSequence
		})
		if index == 10 {
			assertArchiveWindow(t, environment, flowID, 11, 20)
		}
		if index == 14 {
			assertArchiveWindow(t, environment, flowID, 11, 28)
		}
	}
	assertArchiveWindow(t, environment, flowID, 21, 30)

	first, err := environment.agent.ArchivedMessages(t.Context(), flowID, 11)
	if err != nil {
		t.Fatal(err)
	}
	second, err := environment.agent.ArchivedMessages(t.Context(), flowID, 21)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 10 || first.Messages[0].Sequence != 1 || first.Messages[9].Sequence != 10 ||
		first.NextBeforeSequence != nil {
		t.Fatalf("first archive = %#v", first)
	}
	if len(second.Messages) != 10 || second.Messages[0].Sequence != 11 || second.Messages[9].Sequence != 20 ||
		second.NextBeforeSequence == nil || *second.NextBeforeSequence != 11 {
		t.Fatalf("second archive = %#v", second)
	}
	forward, err := environment.agent.MessagesAfter(t.Context(), flowID, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Messages) != 7 || forward.Messages[0].Sequence != 1 || forward.Messages[6].Sequence != 7 ||
		forward.NextAfterSequence == nil || *forward.NextAfterSequence != 7 || !forward.IsTruncated ||
		forward.FirstRetainedSequence != 1 || forward.LastSequence != 30 {
		t.Fatalf("first forward page = %#v", forward)
	}
	forward, err = environment.agent.MessagesAfter(t.Context(), flowID, *forward.NextAfterSequence, MaximumForwardHistoryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Messages) != 23 || forward.Messages[0].Sequence != 8 || forward.Messages[22].Sequence != 30 ||
		forward.NextAfterSequence != nil || forward.IsTruncated {
		t.Fatalf("second forward page = %#v", forward)
	}
	empty, err := environment.agent.MessagesAfter(t.Context(), flowID, 30, 0)
	if err != nil || len(empty.Messages) != 0 || empty.IsTruncated || empty.NextAfterSequence != nil {
		t.Fatalf("empty forward page = %#v, %v", empty, err)
	}
}

func assertArchiveWindow(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	first Sequence,
	last Sequence,
) {
	t.Helper()
	snapshot := readSnapshot(t, environment, flowID)
	if len(snapshot.History.Messages) != int(last-first+1) ||
		snapshot.History.Messages[0].Sequence != first ||
		snapshot.History.Messages[len(snapshot.History.Messages)-1].Sequence != last {
		t.Fatalf("current history at %d = %#v", last, snapshot.History)
	}
	if snapshot.History.NextBeforeSequence == nil || *snapshot.History.NextBeforeSequence != first {
		t.Fatalf("archive boundary at %d = %#v", last, snapshot.History.NextBeforeSequence)
	}
}

func TestAgentUserInputIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-input-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/ask-many What date should I use?"}); err != nil {
		t.Fatal(err)
	}
	snapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.PendingUserInput != nil
	})
	if len(snapshot.Description.PendingUserInput.Questions) != 1 ||
		snapshot.Description.PendingUserInput.Questions[0].Question != "What date should I use?" {
		t.Fatalf("pending questions = %#v", snapshot.Description.PendingUserInput.Questions)
	}
	requestIndex := -1
	for index := len(snapshot.History.Messages) - 1; index >= 0; index-- {
		if len(snapshot.History.Messages[index].Message.ToolCalls) == 2 {
			requestIndex = index
			break
		}
	}
	if requestIndex < 0 || requestIndex+2 >= len(snapshot.History.Messages) {
		t.Fatalf("multi-call history = %#v", snapshot.History.Messages)
	}
	request := snapshot.History.Messages[requestIndex]
	firstResult := snapshot.History.Messages[requestIndex+1]
	secondResult := snapshot.History.Messages[requestIndex+2]
	if firstResult.Message.ToolCallID == nil || secondResult.Message.ToolCallID == nil ||
		*firstResult.Message.ToolCallID != request.Message.ToolCalls[0].ID ||
		*secondResult.Message.ToolCallID != request.Message.ToolCalls[1].ID ||
		!strings.Contains(secondResult.Message.Content, string(toolErrorSupersededByUserInput)) {
		t.Fatalf("multi-call results = %#v / %#v", firstResult, secondResult)
	}
	environment.replaceWorker(t, flowID)
	rejectedMessage := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "September 12"})
	var sendRejected *CommandRejectedError
	if !errors.As(rejectedMessage, &sendRejected) || sendRejected.Command != CommandSendMessage {
		t.Fatalf("message while question pending = %T %v", rejectedMessage, rejectedMessage)
	}
	if err := environment.agent.AnswerQuestions(
		t.Context(),
		flowID,
		answerRequest(*snapshot.Description.PendingUserInput, "September 12"),
	); err != nil {
		t.Fatal(err)
	}
	closed := readSnapshot(t, environment, flowID)
	if closed.Description == nil || closed.Description.PendingUserInput != nil {
		t.Fatalf("question remained after accepted answer: %#v", closed.Description)
	}
	waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.PendingUserInput == nil &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: **Details**: September 12")
	})
	staleAnswer := environment.agent.AnswerQuestions(
		t.Context(),
		flowID,
		answerRequest(*snapshot.Description.PendingUserInput, "September 13"),
	)
	var answerRejected *CommandRejectedError
	if !errors.As(staleAnswer, &answerRejected) || answerRejected.Command != CommandAnswerQuestions {
		t.Fatalf("stale answer error = %T %v", staleAnswer, staleAnswer)
	}

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content: "/choose Where should I deploy? | Staging | Production",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot = waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.PendingUserInput != nil &&
			len(snapshot.Description.PendingUserInput.Questions) == 1 &&
			len(snapshot.Description.PendingUserInput.Questions[0].Options) == 2
	})
	input := snapshot.Description.PendingUserInput
	question := input.Questions[0]
	if question.Question != "Where should I deploy?" ||
		question.Options[0].Label != "Staging" || question.Options[1].Label != "Production" {
		t.Fatalf("pending input = %#v", input)
	}
	if err := environment.agent.AnswerQuestions(t.Context(), flowID, answerRequest(*input, "Production")); err != nil {
		t.Fatal(err)
	}
	waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.PendingUserInput == nil &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: **Details**: Production")
	})

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/questions"}); err != nil {
		t.Fatal(err)
	}
	multi := waitForPendingUserInput(t, environment, flowID)
	if len(multi.Questions) != 3 {
		t.Fatalf("question count = %d, want 3", len(multi.Questions))
	}
	missing := AnswerQuestionsRequest{CallID: multi.CallID, Answers: []UserInputAnswer{
		{QuestionID: "region", Answer: "West"},
		{QuestionID: "pace", Answer: "Careful"},
	}}
	assertAnswerRejected(t, environment.agent.AnswerQuestions(t.Context(), flowID, missing))
	unknown := AnswerQuestionsRequest{CallID: multi.CallID, Answers: []UserInputAnswer{
		{QuestionID: "region", Answer: "West"},
		{QuestionID: "pace", Answer: "Careful"},
		{QuestionID: "unknown", Answer: "Detailed"},
	}}
	assertAnswerRejected(t, environment.agent.AnswerQuestions(t.Context(), flowID, unknown))
	if current := readSnapshot(t, environment, flowID); current.Description == nil ||
		current.Description.PendingUserInput == nil || current.Description.PendingUserInput.CallID != multi.CallID {
		t.Fatalf("invalid answer changed pending batch: %#v", current.Description)
	}

	answer := AnswerQuestionsRequest{CallID: multi.CallID, Answers: []UserInputAnswer{
		{QuestionID: "format", Answer: "Detailed"},
		{QuestionID: "region", Answer: "West"},
		{QuestionID: "pace", Answer: "Careful"},
	}}
	environment.replaceWorker(t, flowID)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- environment.agent.AnswerQuestions(t.Context(), flowID, answer)
		}()
	}
	accepted := 0
	rejected := 0
	for range 2 {
		err := <-results
		if err == nil {
			accepted++
			continue
		}
		var commandRejected *CommandRejectedError
		if errors.As(err, &commandRejected) && commandRejected.Command == CommandAnswerQuestions {
			rejected++
			continue
		}
		t.Fatalf("concurrent answer error = %T %v", err, err)
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("concurrent answers accepted/rejected = %d/%d, want 1/1", accepted, rejected)
	}
	const combinedAnswer = "**Region**: West\n\n**Pace**: Careful\n\n**Format**: Detailed"
	waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.PendingUserInput == nil &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: "+combinedAnswer)
	})

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/questions"}); err != nil {
		t.Fatal(err)
	}
	nextBatch := waitForPendingUserInput(t, environment, flowID)
	if nextBatch.CallID == multi.CallID {
		t.Fatal("a later question batch reused the resolved call ID")
	}
	if err := environment.agent.AnswerQuestions(t.Context(), flowID, AnswerQuestionsRequest{
		CallID: nextBatch.CallID,
		Answers: []UserInputAnswer{
			{QuestionID: "region", Answer: "East"},
			{QuestionID: "pace", Answer: "Fast"},
			{QuestionID: "format", Answer: "Short"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingUserInput(t, environment, flowID)

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content: "/plan-question deployment", PlanMode: true,
	}); err != nil {
		t.Fatal(err)
	}
	plan := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{Revision: plan.Revision}); err != nil {
		t.Fatal(err)
	}
	planInput := waitForPendingUserInput(t, environment, flowID)
	if err := environment.agent.AnswerQuestions(t.Context(), flowID, answerRequest(planInput, "Production")); err != nil {
		t.Fatal(err)
	}
	waitForAgentPlan(t, environment, flowID, PlanStatusCompleted)
}

func assertAnswerRejected(t *testing.T, err error) {
	t.Helper()
	var rejected *CommandRejectedError
	if !errors.As(err, &rejected) || rejected.Command != CommandAnswerQuestions {
		t.Fatalf("answer error = %T %v, want answer rejection", err, err)
	}
}

func TestAgentPlanGuardrailsIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-plan-guardrails-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "Plan the first objective", PlanMode: true}); err != nil {
		t.Fatal(err)
	}
	first := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "Plan the revised objective", PlanMode: true}); err != nil {
		t.Fatal(err)
	}
	revised := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Plan != nil &&
			snapshot.Description.Plan.Revision > first.Revision
	})
	if revised.Description.Plan.Tasks[0].Content != "Plan the revised objective" {
		t.Fatalf("revised plan = %#v", revised.Description.Plan)
	}
	oldRevisionErr := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{Revision: first.Revision})
	var rejected *CommandRejectedError
	if !errors.As(oldRevisionErr, &rejected) {
		t.Fatalf("old revision error = %T %v", oldRevisionErr, oldRevisionErr)
	}
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/plan-clear", PlanMode: true}); err != nil {
		t.Fatal(err)
	}
	waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan == nil
	})

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content: "/plan-stop demonstrate advisory completion", PlanMode: true,
	}); err != nil {
		t.Fatal(err)
	}
	draftSnapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan != nil && snapshot.Description.Plan.Status == PlanStatusDraft
	})
	if err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		Revision: draftSnapshot.Description.Plan.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	active := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan != nil && snapshot.Description.Plan.Status == PlanStatusActive
	})
	state := waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && state.PlanNoProgressAttempts == 2
	})
	if state.PlanNoProgressAttempts != 2 {
		t.Fatalf("Plan no-progress attempts = %d, want 2", state.PlanNoProgressAttempts)
	}
	if count := countHistoryMessages(
		active.History.Messages,
		MessageRoleAssistant,
		"integration stopped before completing the active plan",
	); count != 2 {
		t.Fatalf("automatic Plan recovery responses = %d, want 2", count)
	}

	environment.replaceWorker(t, flowID)
	submitted := make(chan error, 1)
	go func() {
		submitted <- environment.agent.WaitForInteractionStatus(
			t.Context(),
			flowID,
			AgentInteractionStatusSubmitted,
		)
	}()
	if err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		Revision: active.Description.Plan.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
	continued := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan != nil && snapshot.Description.Plan.Status == PlanStatusActive &&
			countHistoryMessages(
				snapshot.History.Messages,
				MessageRoleAssistant,
				"integration stopped before completing the active plan",
			) == 4
	})
	if continued.Description.Plan.Revision != active.Description.Plan.Revision {
		t.Fatalf("Continue changed Plan revision: got %d, want %d", continued.Description.Plan.Revision, active.Description.Plan.Revision)
	}
	state = readAgentState(t, environment, flowID)
	if state.PlanNoProgressAttempts != 2 {
		t.Fatalf("Continue recovery no-progress attempts = %d, want 2", state.PlanNoProgressAttempts)
	}
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool integration_tool {}"}); err != nil {
		t.Fatal(err)
	}
	afterBlockedTool := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			historyContainsText(snapshot.History.Messages, string(toolErrorUnknownOrDisabled))
	})
	if afterBlockedTool.Description.Plan == nil || afterBlockedTool.Description.Plan.Status != PlanStatusActive ||
		afterBlockedTool.Description.Plan.Revision != active.Description.Plan.Revision {
		t.Fatalf("blocked tool changed active plan: %#v", afterBlockedTool.Description.Plan)
	}
	state = readAgentState(t, environment, flowID)
	if state.PlanNoProgressAttempts != 0 {
		t.Fatalf("user turn did not reset Plan no-progress attempts: %d", state.PlanNoProgressAttempts)
	}
}

func TestAgentRejectsPlanExecutionWhileBusyWithoutPublishing(t *testing.T) {
	modelClient := newBlockingPlanModel()
	environment := newAgentIntegrationEnvironment(t, modelClient, newIntegrationToolRegistry())
	flowID := FlowID("agent-plan-busy-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content: "/plan-stop reject busy execution", PlanMode: true,
	}); err != nil {
		t.Fatal(err)
	}
	draftSnapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan != nil && snapshot.Description.Plan.Status == PlanStatusDraft
	})
	draft := draftSnapshot.Description.Plan
	if err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{Revision: draft.Revision}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-modelClient.started:
	case <-time.After(integrationWaitTimeout):
		t.Fatal("timed out waiting for the active Plan model call")
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusCallingModel
	})

	err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{Revision: draft.Revision})
	var rejected *CommandRejectedError
	if !errors.As(err, &rejected) || rejected.Command != CommandExecutePlan {
		t.Fatalf("busy Execute Plan error = %T %v, want execute rejection", err, err)
	}
	var executions []dex.ChannelMessage[PlanExecutionRequest]
	if err := environment.sdk.GetChannelMapMessages(
		t.Context(),
		string(flowID),
		planExecutionsChannel,
		planRevisionKey(draft.Revision),
		&executions,
	); err != nil {
		t.Fatal(err)
	}
	if len(executions) != 0 {
		t.Fatalf("busy rejection left %d Plan execution message(s)", len(executions))
	}
	state := readAgentState(t, environment, flowID)
	if state.PendingPlanExecutionRevision != nil {
		t.Fatalf("busy rejection left pending Plan revision %d", *state.PendingPlanExecutionRevision)
	}

	close(modelClient.release)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if calls := modelClient.activeCalls.Load(); calls != 2 {
		t.Fatalf("active Plan model calls = %d, want exactly 2", calls)
	}
}

func TestAgentPlanTaskActivityIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-plan-activity-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content:  "stream task progress",
		PlanMode: true,
	}); err != nil {
		t.Fatal(err)
	}
	draft := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		Revision: draft.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	event := readActivityUntil(t, environment.agent, flowID, func(event AgentEvent) bool {
		return event.Kind == EventKindPlanTaskUpdated && event.PlanTaskStatus != nil &&
			*event.PlanTaskStatus == TaskStatusInProgress
	})
	activity := event.Activity
	if activity.Message != "Started plan task 1." || activity.PlanBaseRevision == nil ||
		*activity.PlanBaseRevision != draft.Revision || activity.PlanRevision == nil ||
		*activity.PlanRevision != draft.Revision+1 || activity.PlanTaskIndex == nil ||
		*activity.PlanTaskIndex != 0 || activity.CallID != nil || activity.ToolName != nil ||
		activity.MessageSequence != nil {
		t.Fatalf("Plan task Activity = %#v", event)
	}
	completed := waitForAgentPlan(t, environment, flowID, PlanStatusCompleted)
	if completed.Revision <= *activity.PlanRevision || completed.Tasks[0].Status != TaskStatusCompleted {
		t.Fatalf("completed Plan = %#v", completed)
	}
}

func TestAgentBatchSteeringIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-steer-batch-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/wait"}); err != nil {
		t.Fatal(err)
	}
	waitForPendingTimer(t, environment, flowID)
	for _, content := range []string{"first replacement objective", "final replacement objective"} {
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	queued := waitForQueuedMessages(t, environment, flowID, 2)
	for _, message := range queued {
		if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{MessageID: MessageID(message.MessageID)}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			len(snapshot.Queued) == 0 && len(snapshot.Steered) == 0 &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: final replacement objective")
	})
	users := make([]string, 0, 2)
	for _, item := range snapshot.History.Messages {
		if item.Message.Role == MessageRoleUser && strings.Contains(item.Message.Content, "replacement objective") {
			users = append(users, item.Message.Content)
		}
	}
	if len(users) != 2 || users[0] != "first replacement objective" || users[1] != "final replacement objective" {
		t.Fatalf("steered user messages = %q", users)
	}
}

func TestAgentTerminalSnapshotIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-terminal-" + randomLocalID(t))
	runID, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()})
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.sdk.StopFlow(t.Context(), string(flowID), dex.StopOptions{
		Type:   dex.TerminateFlow,
		Reason: "terminal Snapshot integration",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := environment.sdk.WaitForFlow(t.Context(), string(flowID), dex.WaitForFlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != dex.FlowTerminated {
		t.Fatalf("Flow status = %v, want terminated", result.Status)
	}

	snapshot := readSnapshot(t, environment, flowID)
	if snapshot.RunID != runID || snapshot.FlowStatus != FlowStatusTerminated {
		t.Fatalf("terminal Snapshot identity = %#v", snapshot)
	}
	if snapshot.Description != nil || snapshot.ErrorType != nil ||
		len(snapshot.History.Messages) != 0 || len(snapshot.Queued) != 0 || len(snapshot.Steered) != 0 {
		t.Fatalf("terminal Snapshot durable view = %#v", snapshot)
	}
}

func readSnapshot(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
) AgentSnapshot {
	t.Helper()
	snapshot, err := environment.agent.Snapshot(t.Context(), flowID)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func waitForSnapshot(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	accept func(AgentSnapshot) bool,
) AgentSnapshot {
	t.Helper()
	var snapshot AgentSnapshot
	waitUntil(t, environment, "Agent Snapshot", func() (bool, error) {
		var err error
		snapshot, err = environment.agent.Snapshot(t.Context(), flowID)
		return err == nil && accept(snapshot), err
	})
	return snapshot
}

func historyHasMessage(messages []SequencedMessage, role MessageRole, content string) bool {
	for _, message := range messages {
		if message.Message.Role == role && message.Message.Content == content {
			return true
		}
	}
	return false
}

func countHistoryMessages(messages []SequencedMessage, role MessageRole, content string) int {
	count := 0
	for _, message := range messages {
		if message.Message.Role == role && message.Message.Content == content {
			count++
		}
	}
	return count
}

func historyContainsText(messages []SequencedMessage, text string) bool {
	for _, message := range messages {
		if strings.Contains(message.Message.Content, text) {
			return true
		}
	}
	return false
}

func answerRequest(input PendingUserInput, answer string) AnswerQuestionsRequest {
	return AnswerQuestionsRequest{
		CallID: input.CallID,
		Answers: []UserInputAnswer{{
			QuestionID: input.Questions[0].ID,
			Answer:     answer,
		}},
	}
}

type agentIntegrationEnvironment struct {
	flow          *Flow
	address       string
	serverAddress string
	cache         *blobcache.Cache
	worker        *dex.Worker
	workerResult  chan error
	sdk           *dex.Client
	agent         *Client
}

func newAgentIntegrationEnvironment(t *testing.T, modelClient ModelClient, tools ToolRegistry) *agentIntegrationEnvironment {
	t.Helper()
	environment := &agentIntegrationEnvironment{
		flow:          NewFlow(modelClient, tools),
		address:       availableLocalAddress(t, t.Context()),
		serverAddress: os.Getenv("DEX_FLOW_SERVICE_ADDRESS"),
	}
	if environment.serverAddress == "" {
		environment.serverAddress = "127.0.0.1:8801"
	}
	environment.startWorker(t)
	t.Cleanup(func() {
		if err := environment.close(t.Context()); err != nil {
			t.Errorf("close integration environment: %v", err)
		}
	})
	return environment
}

func (environment *agentIntegrationEnvironment) startWorker(t *testing.T) {
	t.Helper()
	registry, err := dex.NewRegistry([]dex.Flow{environment.flow})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := blobcache.New(&blobcache.Config{
		Dir:      t.TempDir(),
		MaxBytes: 64 << 20,
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := dex.NewWorker(registry, cache, dex.WorkerOptions{
		BindAddress:        environment.address,
		FlowServiceAddress: environment.serverAddress,
		WorkerTarget:       dex.WorkerTarget{Address: environment.address},
		Logger:             slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(errors.Join(err, cache.Close()))
	}
	workerResult := make(chan error, 1)
	go func() {
		workerResult <- worker.Start()
	}()
	sdkClient, err := dex.NewClient(registry, cache, dex.ClientOptions{
		FlowServiceAddress: environment.serverAddress,
		WorkerTarget:       worker.WorkerTarget(),
		Logger:             slog.New(slog.DiscardHandler),
	})
	if err != nil {
		stopContext, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		t.Fatal(errors.Join(err, worker.Stop(stopContext), cache.Close()))
	}
	environment.cache = cache
	environment.worker = worker
	environment.workerResult = workerResult
	environment.sdk = sdkClient
	environment.agent = NewClient(sdkClient, environment.flow)
	waitForWorkerAddress(t, environment)
}

func (environment *agentIntegrationEnvironment) replaceWorker(t *testing.T, flowID FlowID) {
	t.Helper()
	if err := environment.stopWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	environment.startWorker(t)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(integrationWaitTimeout)
	defer deadline.Stop()
	var lastErr error
	for {
		if _, err := environment.agent.Snapshot(t.Context(), flowID); err == nil {
			return
		} else {
			lastErr = err
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("replacement Worker did not serve Agent RPCs: %v", lastErr)
		case <-t.Context().Done():
			t.Fatalf("wait for replacement Worker RPC: %v", t.Context().Err())
		}
	}
}

func (environment *agentIntegrationEnvironment) stopWorker(ctx context.Context) error {
	if environment.worker == nil {
		return nil
	}
	stopContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	stopErr := environment.worker.Stop(stopContext)
	var workerErr error
	select {
	case workerErr = <-environment.workerResult:
	case <-stopContext.Done():
		workerErr = fmt.Errorf("join integration worker: %w", stopContext.Err())
	}
	clientErr := environment.sdk.Close()
	cacheErr := environment.cache.Close()
	environment.worker = nil
	environment.sdk = nil
	environment.cache = nil
	environment.agent = nil
	return errors.Join(stopErr, workerErr, clientErr, cacheErr)
}

func (environment *agentIntegrationEnvironment) close(ctx context.Context) error {
	return environment.stopWorker(ctx)
}

func waitForWorkerAddress(t *testing.T, environment *agentIntegrationEnvironment) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(t.Context(), "tcp", environment.address)
		if err == nil {
			if closeErr := connection.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			return
		}
		select {
		case workerErr := <-environment.workerResult:
			environment.workerResult <- workerErr
			t.Fatalf("integration worker stopped: %v", workerErr)
		case <-ticker.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
}

func availableLocalAddress(t *testing.T, ctx context.Context) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func randomLocalID(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", address, time.Now().UnixNano())))
	return hex.EncodeToString(digest[:12])
}

func waitForAgentState(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	accept func(AgentState) bool,
) AgentState {
	t.Helper()
	var state AgentState
	waitUntil(t, environment, "Agent state", func() (bool, error) {
		found, err := environment.sdk.GetAttribute(t.Context(), string(flowID), agentStateAttribute, &state)
		return found && accept(state), err
	})
	return state
}

func readAgentState(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) AgentState {
	t.Helper()
	var state AgentState
	found, err := environment.sdk.GetAttribute(t.Context(), string(flowID), agentStateAttribute, &state)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Agent state is missing")
	}
	return state
}

func waitForAgentPlan(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID, status PlanStatus) AgentPlan {
	t.Helper()
	var plan AgentPlan
	waitUntil(t, environment, "Agent plan", func() (bool, error) {
		found, err := environment.sdk.GetAttribute(t.Context(), string(flowID), agentPlanAttribute, &plan)
		return found && plan.Status == status, err
	})
	return plan
}

func waitForPendingApproval(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) PendingApproval {
	t.Helper()
	var approval PendingApproval
	waitUntil(t, environment, "pending approval", func() (bool, error) {
		return environment.sdk.GetAttribute(t.Context(), string(flowID), pendingApprovalAttribute, &approval)
	})
	return approval
}

func waitForPendingUserInput(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) PendingUserInput {
	t.Helper()
	var pending PendingUserInput
	waitUntil(t, environment, "pending user input", func() (bool, error) {
		return environment.sdk.GetAttribute(t.Context(), string(flowID), pendingUserInputAttribute, &pending)
	})
	return pending
}

func waitForNoPendingUserInput(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
	t.Helper()
	waitUntil(t, environment, "cleared user input", func() (bool, error) {
		var pending PendingUserInput
		found, err := environment.sdk.GetAttribute(t.Context(), string(flowID), pendingUserInputAttribute, &pending)
		return !found, err
	})
}

func waitForPendingTimer(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
	t.Helper()
	var pending PendingTimer
	waitUntil(t, environment, "pending timer", func() (bool, error) {
		return environment.sdk.GetAttribute(t.Context(), string(flowID), pendingTimerAttribute, &pending)
	})
}

func waitForNoPendingTimer(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
	t.Helper()
	waitUntil(t, environment, "cleared timer", func() (bool, error) {
		var pending PendingTimer
		found, err := environment.sdk.GetAttribute(t.Context(), string(flowID), pendingTimerAttribute, &pending)
		return !found, err
	})
}

func waitForQueuedMessages(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	count int,
) []dex.ChannelMessage[UserMessage] {
	t.Helper()
	var messages []dex.ChannelMessage[UserMessage]
	waitUntil(t, environment, "queued messages", func() (bool, error) {
		messages = nil
		err := environment.sdk.GetChannelMessages(t.Context(), string(flowID), queuedUserMessagesChannel, &messages)
		return len(messages) == count, err
	})
	return messages
}

func waitUntil(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	description string,
	condition func() (bool, error),
) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(integrationWaitTimeout)
	defer deadline.Stop()
	for {
		matched, err := condition()
		if err != nil {
			t.Fatalf("read %s: %v", description, err)
		}
		if matched {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("wait for %s exceeded %s", description, integrationWaitTimeout)
		case <-t.Context().Done():
			t.Fatalf("wait for %s: %v", description, t.Context().Err())
		}
	}
}

func assertApplicationMessage(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	sequence Sequence,
	role MessageRole,
	content string,
) {
	t.Helper()
	message, found := readApplicationMessage(t, environment, flowID, sequence)
	if !found || message.Role != role || message.Content != content {
		t.Fatalf("message %d = found:%t role:%q content:%q", sequence, found, message.Role, message.Content)
	}
}

func historyContains(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	state AgentState,
	role MessageRole,
	content string,
) bool {
	t.Helper()
	for sequence := state.FirstRetainedSequence; sequence <= state.LastSequence; sequence++ {
		message, found := readApplicationMessage(t, environment, flowID, sequence)
		if found && message.Role == role && message.Content == content {
			return true
		}
	}
	return false
}

func readApplicationMessage(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	sequence Sequence,
) (AgentMessage, bool) {
	t.Helper()
	state := readAgentState(t, environment, flowID)
	if sequence >= state.CurrentFirstSequence {
		var message AgentMessage
		found, err := environment.sdk.GetAttributeMapInstance(
			t.Context(), string(flowID), currentMessagesAttribute, sequenceKey(sequence), &message,
		)
		if err != nil {
			t.Fatal(err)
		}
		return message, found
	}
	firstSequence := ((sequence - 1) / archiveMessageChunkSize * archiveMessageChunkSize) + 1
	var chunk ArchivedMessageChunk
	found, err := environment.sdk.GetAttributeMapInstance(
		t.Context(), string(flowID), archivedMessagesAttribute, sequenceKey(firstSequence), &chunk,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return AgentMessage{}, false
	}
	for _, archived := range chunk.Messages {
		if archived.Sequence == sequence {
			return archived.Message, true
		}
	}
	return AgentMessage{}, false
}

func assertTextStream(t *testing.T, client *Client, flowID FlowID, stream EventStream, expected string) {
	t.Helper()
	event, err := client.ReadEvent(context.Background(), flowID, stream, "")
	if err != nil {
		t.Fatal(err)
	}
	if event.Text != expected || event.ResumeToken == "" || event.CreatedAt.IsZero() || event.Source == "" {
		t.Fatalf("%s event = %#v", stream, event)
	}
}

func assertModelActivity(t *testing.T, client *Client, flowID FlowID, expectedSequence Sequence) {
	t.Helper()
	event, err := client.ReadEvent(context.Background(), flowID, EventStreamActivity, "")
	if err != nil {
		t.Fatal(err)
	}
	if event.Activity.Kind != EventKindModelStarted ||
		event.Activity.MessageSequence == nil ||
		*event.Activity.MessageSequence != expectedSequence {
		t.Fatalf("model activity = %#v", event)
	}
}

func readActivityUntil(
	t *testing.T,
	client *Client,
	flowID FlowID,
	matches func(AgentEvent) bool,
) StreamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationWaitTimeout)
	defer cancel()
	resumeToken := ResumeToken("")
	for {
		event, err := client.ReadEvent(ctx, flowID, EventStreamActivity, resumeToken)
		if err != nil {
			t.Fatalf("read Activity Stream for %s: %v", flowID, err)
		}
		resumeToken = event.ResumeToken
		if matches(event.Activity) {
			return event
		}
	}
}

type integrationModel struct{}

var _ ModelClient = integrationModel{}

func (integrationModel) Complete(ctx context.Context, request ModelRequest) (ModelReply, error) {
	if request.WriteAssistant == nil || request.WriteReasoning == nil || request.WriteActivity == nil {
		return ModelReply{}, errors.New("integration model writers are required")
	}
	if err := request.WriteReasoning("deterministic integration summary"); err != nil {
		return ModelReply{}, err
	}
	if request.ForcedTool == ToolNameWriteTodos {
		arguments := integrationPlanArguments(request.Messages, TaskStatusPending)
		return integrationToolReply(request, ToolNameWriteTodos, arguments, "drafted plan")
	}
	if planTaskStatus, hasActivePlan := integrationActivePlanTaskStatus(request.Messages); hasActivePlan {
		if strings.HasPrefix(integrationLastUserContent(request.Messages), "/plan-question ") {
			encoded, err := json.Marshal(struct {
				Questions []UserInputQuestion `json:"questions"`
			}{Questions: integrationQuestions("Which deployment environment?", []string{"Staging", "Production"})})
			if err != nil {
				return ModelReply{}, err
			}
			arguments, err := ParseJSONObject(string(encoded))
			if err != nil {
				return ModelReply{}, err
			}
			return integrationToolReply(request, ToolNameRequestUserInput, arguments, "requesting plan input")
		}
		if strings.HasPrefix(integrationLastUserContent(request.Messages), "/plan-stop ") {
			content := "integration stopped before completing the active plan"
			if err := request.WriteAssistant(content); err != nil {
				return ModelReply{}, err
			}
			return ModelReply{Content: content, ToolCalls: []ToolCall{}}, nil
		}
		nextStatus := TaskStatusCompleted
		content := "completed plan"
		if planTaskStatus == TaskStatusPending {
			nextStatus = TaskStatusInProgress
			content = "started plan task"
		}
		arguments := integrationPlanArguments(request.Messages, nextStatus)
		return integrationToolReply(request, ToolNameWriteTodos, arguments, content)
	}
	if lastMessage := integrationLastConversationMessage(request.Messages); lastMessage != nil && lastMessage.Role == MessageRoleTool {
		content := "integration tool result acknowledged"
		if err := request.WriteAssistant(content); err != nil {
			return ModelReply{}, err
		}
		return ModelReply{Content: content, ToolCalls: []ToolCall{}}, nil
	}
	userContent := integrationLastUserContent(request.Messages)
	switch userContent {
	case "/tool":
		return integrationToolReply(request, integrationToolName, MustJSONObject(`{}`), "calling integration tool")
	case "/ask":
		return ModelReply{}, errors.New("integration /ask prompt is missing")
	case "/wait":
		return integrationToolReply(request, ToolNameDurableWait, MustJSONObject(`{"duration_seconds":30,"reason":"integration wait"}`), "waiting durably")
	}
	if strings.HasPrefix(userContent, "/ask-many ") {
		prompt := strings.TrimSpace(strings.TrimPrefix(userContent, "/ask-many "))
		inputJSON, err := json.Marshal(struct {
			Questions []UserInputQuestion `json:"questions"`
		}{Questions: integrationQuestions(prompt, []string{"Yes", "No"})})
		if err != nil {
			return ModelReply{}, err
		}
		inputArguments, err := ParseJSONObject(string(inputJSON))
		if err != nil {
			return ModelReply{}, err
		}
		calls := []ToolCall{
			integrationToolCall(request, ToolNameRequestUserInput, inputArguments),
			integrationToolCall(request, ToolNameDurableWait, MustJSONObject(`{"duration_seconds":60,"reason":"superseded test"}`)),
		}
		return ModelReply{Content: "requesting input", ToolCalls: calls}, nil
	}
	if strings.HasPrefix(userContent, "/choose ") {
		parts := strings.Split(strings.TrimPrefix(userContent, "/choose "), "|")
		if len(parts) < 3 {
			return ModelReply{}, errors.New("integration /choose requires a prompt and two choices")
		}
		for index := range parts {
			parts[index] = strings.TrimSpace(parts[index])
		}
		encoded, err := json.Marshal(struct {
			Questions []UserInputQuestion `json:"questions"`
		}{Questions: integrationQuestions(parts[0], parts[1:])})
		if err != nil {
			return ModelReply{}, err
		}
		arguments, err := ParseJSONObject(string(encoded))
		if err != nil {
			return ModelReply{}, err
		}
		return integrationToolReply(request, ToolNameRequestUserInput, arguments, "requesting a choice")
	}
	if userContent == "/questions" {
		encoded, err := json.Marshal(struct {
			Questions []UserInputQuestion `json:"questions"`
		}{Questions: integrationQuestionBatch()})
		if err != nil {
			return ModelReply{}, err
		}
		arguments, err := ParseJSONObject(string(encoded))
		if err != nil {
			return ModelReply{}, err
		}
		return integrationToolReply(request, ToolNameRequestUserInput, arguments, "requesting three answers")
	}
	if strings.HasPrefix(userContent, "/tool ") {
		parts := strings.SplitN(userContent, " ", 3)
		if len(parts) != 3 {
			return ModelReply{}, errors.New("integration /tool requires a name and arguments")
		}
		arguments, err := ParseJSONObject(parts[2])
		if err != nil {
			return ModelReply{}, err
		}
		return integrationToolReply(request, ToolName(parts[1]), arguments, "calling integration tool")
	}
	if strings.HasPrefix(userContent, "/ask ") {
		prompt, err := json.Marshal(struct {
			Questions []UserInputQuestion `json:"questions"`
		}{Questions: integrationQuestions(strings.TrimSpace(strings.TrimPrefix(userContent, "/ask ")), []string{"Yes", "No"})})
		if err != nil {
			return ModelReply{}, err
		}
		arguments, err := ParseJSONObject(string(prompt))
		if err != nil {
			return ModelReply{}, err
		}
		return integrationToolReply(request, ToolNameRequestUserInput, arguments, "requesting input")
	}
	content := "integration response: " + userContent
	if err := request.WriteAssistant(content); err != nil {
		return ModelReply{}, err
	}
	return ModelReply{Content: content, ToolCalls: []ToolCall{}}, nil
}

func (integrationModel) Summarize(_ context.Context, request SummarizeRequest) (string, error) {
	parts := make([]string, 0, len(request.Messages)+1)
	if request.PreviousSummary != "" {
		parts = append(parts, request.PreviousSummary)
	}
	for _, message := range request.Messages {
		parts = append(parts, string(message.Role)+": "+message.Content)
	}
	return strings.Join(parts, "\n"), nil
}

func (integrationModel) CountTokens(_ Model, messages []AgentMessage) int {
	total := 0
	for _, message := range messages {
		total += max(1, len(message.Content)/4)
	}
	return total
}

type blockingPlanModel struct {
	integrationModel
	started     chan struct{}
	release     chan struct{}
	activeCalls atomic.Int32
}

var _ ModelClient = (*blockingPlanModel)(nil)

func newBlockingPlanModel() *blockingPlanModel {
	return &blockingPlanModel{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (model *blockingPlanModel) Complete(ctx context.Context, request ModelRequest) (ModelReply, error) {
	if _, hasActivePlan := integrationActivePlanTaskStatus(request.Messages); !hasActivePlan {
		return model.integrationModel.Complete(ctx, request)
	}
	call := model.activeCalls.Add(1)
	if call == 1 {
		model.started <- struct{}{}
		select {
		case <-model.release:
		case <-ctx.Done():
			return ModelReply{}, ctx.Err()
		}
	}
	content := "integration stopped before completing the active plan"
	if err := request.WriteAssistant(content); err != nil {
		return ModelReply{}, err
	}
	return ModelReply{Content: content, ToolCalls: []ToolCall{}}, nil
}

func integrationToolReply(request ModelRequest, name ToolName, arguments JSONObject, content string) (ModelReply, error) {
	if err := request.WriteAssistant(content); err != nil {
		return ModelReply{}, err
	}
	return ModelReply{
		Content:   content,
		ToolCalls: []ToolCall{integrationToolCall(request, name, arguments)},
	}, nil
}

func integrationToolCall(request ModelRequest, name ToolName, arguments JSONObject) ToolCall {
	digest := sha256.Sum256([]byte(
		string(request.FlowID) + "\x00" + string(name) + "\x00" + arguments.String() + "\x00" +
			fmt.Sprintf("%d", len(request.Messages)) + "\x00" + integrationLastUserContent(request.Messages),
	))
	return ToolCall{ID: CallID("call-" + hex.EncodeToString(digest[:16])), Name: name, Arguments: arguments}
}

func integrationQuestions(prompt string, labels []string) []UserInputQuestion {
	options := make([]UserInputOption, 0, len(labels))
	for _, label := range labels {
		options = append(options, UserInputOption{
			Label:       label,
			Description: "Choose " + label + ".",
		})
	}
	return []UserInputQuestion{{
		ID:       "answer",
		Header:   "Details",
		Question: prompt,
		Options:  options,
	}}
}

func integrationQuestionBatch() []UserInputQuestion {
	return []UserInputQuestion{
		{ID: "region", Header: "Region", Question: "Which region?", Options: []UserInputOption{
			{Label: "West", Description: "Use west."},
			{Label: "East", Description: "Use east."},
		}},
		{ID: "pace", Header: "Pace", Question: "Which pace?", Options: []UserInputOption{
			{Label: "Fast", Description: "Move fast."},
			{Label: "Careful", Description: "Move carefully."},
		}},
		{ID: "format", Header: "Format", Question: "Which format?", Options: []UserInputOption{
			{Label: "Short", Description: "Keep it short."},
			{Label: "Detailed", Description: "Include details."},
		}},
	}
}

func integrationPlanArguments(messages []AgentMessage, status TaskStatus) JSONObject {
	content := integrationLastUserContent(messages)
	if content == "" {
		content = "integration objective"
	}
	tasks := []PlanTask{{Content: content, Status: status}}
	if strings.EqualFold(content, "/plan-clear") {
		tasks = []PlanTask{}
	}
	encoded, err := json.Marshal(writeTodosArguments{Todos: tasks})
	if err != nil {
		panic(err)
	}
	return MustJSONObject(string(encoded))
}

func integrationLastUserContent(messages []AgentMessage) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == MessageRoleUser {
			return messages[index].Content
		}
	}
	return ""
}

func integrationLastConversationMessage(messages []AgentMessage) *AgentMessage {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != MessageRoleSystem {
			return &messages[index]
		}
	}
	return nil
}

func integrationActivePlanTaskStatus(messages []AgentMessage) (TaskStatus, bool) {
	if len(messages) == 0 || messages[len(messages)-1].Role != MessageRoleSystem ||
		!strings.Contains(messages[len(messages)-1].Content, "The user approved this plan. Execute it") {
		return "", false
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == MessageRoleSystem && strings.Contains(message.Content, "Current durable plan:") {
			if !strings.Contains(message.Content, `"status":"active"`) {
				return "", false
			}
			if strings.Contains(message.Content, `"status":"pending"`) {
				return TaskStatusPending, true
			}
			if strings.Contains(message.Content, `"status":"in_progress"`) {
				return TaskStatusInProgress, true
			}
			return "", false
		}
	}
	return "", false
}

type integrationToolRegistry struct {
	mutex       sync.Mutex
	invocations []integrationToolInvocation
}

type integrationToolInvocation struct {
	flowID          FlowID
	callID          CallID
	runtimeMetadata JSONObject
}

var _ ToolRegistry = (*integrationToolRegistry)(nil)

func newIntegrationToolRegistry() *integrationToolRegistry {
	return &integrationToolRegistry{}
}

func (*integrationToolRegistry) ServerNames() []string {
	return []string{"integration"}
}

func (*integrationToolRegistry) RegisteredTools() []RegisteredTool {
	return []RegisteredTool{{
		ServerName: "integration",
		RemoteName: string(integrationToolName),
		Definition: integrationToolDefinition(),
	}}
}

func (*integrationToolRegistry) Definitions([]string, []ToolName) []ToolDefinition {
	return []ToolDefinition{integrationToolDefinition()}
}

func (registry *integrationToolRegistry) Execute(ctx context.Context, invocation ToolInvocation) (ToolExecutionResult, error) {
	if invocation.Name != integrationToolName {
		return ToolExecutionResult{}, fmt.Errorf("unexpected integration tool %q", invocation.Name)
	}
	registry.mutex.Lock()
	registry.invocations = append(registry.invocations, integrationToolInvocation{
		flowID:          invocation.FlowID,
		callID:          invocation.CallID,
		runtimeMetadata: invocation.RuntimeMetadata,
	})
	registry.mutex.Unlock()
	if err := invocation.WriteProgress("integration tool completed"); err != nil {
		return ToolExecutionResult{}, err
	}
	return ToolExecutionResult{Content: `{"ok":true}`, Outcome: ToolOutcomeSucceeded}, nil
}

func (registry *integrationToolRegistry) assertCallsUseID(t *testing.T, callID CallID) {
	t.Helper()
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if len(registry.invocations) != 1 {
		t.Fatalf("tool executions = %d, want exactly one", len(registry.invocations))
	}
	for _, actual := range registry.invocations {
		if actual.callID != callID {
			t.Fatalf("tool call ID changed across Worker replacement: got %q, want %q", actual.callID, callID)
		}
	}
}

func (registry *integrationToolRegistry) assertInvocation(
	t *testing.T,
	flowID FlowID,
	callID CallID,
	runtimeMetadata JSONObject,
) {
	t.Helper()
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if len(registry.invocations) != 1 {
		t.Fatalf("tool executions = %d, want exactly one", len(registry.invocations))
	}
	actual := registry.invocations[0]
	if actual.flowID != flowID || actual.callID != callID || actual.runtimeMetadata != runtimeMetadata {
		t.Fatalf("tool invocation = %#v", actual)
	}
}

func (registry *integrationToolRegistry) assertExecutionCount(t *testing.T, expected int) {
	t.Helper()
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if len(registry.invocations) != expected {
		t.Fatalf("tool executions = %d, want %d", len(registry.invocations), expected)
	}
}

func integrationToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:             integrationToolName,
		Description:      "Exercise an approved external-effect boundary.",
		InputSchema:      MustJSONObject(`{"type":"object","additionalProperties":false}`),
		RequiresApproval: true,
		MaximumAttempts:  1,
	}
}

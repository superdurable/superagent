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

var integrationMessageSequence atomic.Uint64

func integrationRequestID(prefix string) RequestID {
	return RequestID(fmt.Sprintf("%s-%d", prefix, integrationMessageSequence.Add(1)))
}

func integrationUserMessage(content string, isPlanMode bool) SendMessageRequest {
	messageID := MessageID(integrationRequestID("integration-message"))
	return SendMessageRequest{
		RequestID: RequestID(messageID),
		Message: UserMessage{
			MessageID: messageID,
			Content:   content,
			PlanMode:  isPlanMode,
		},
	}
}

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

	runID, err := environment.agent.Start(t.Context(), flowID, config)
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

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("hello", false)); err != nil {
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

	environment.replaceWorker(t)
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/tool", false)); err != nil {
		t.Fatal(err)
	}
	approval := waitForPendingApproval(t, environment, flowID)
	if approval.ToolName != integrationToolName {
		t.Fatalf("pending tool = %q", approval.ToolName)
	}
	environment.replaceWorker(t)
	recoveredApproval := waitForPendingApproval(t, environment, flowID)
	if recoveredApproval.CallID != approval.CallID || recoveredApproval.Arguments != approval.Arguments {
		t.Fatalf("approval changed across Worker replacement: got %#v, want %#v", recoveredApproval, approval)
	}
	approvalRequest := ToolApprovalRequest{
		RequestID: integrationRequestID("approve"), CallID: approval.CallID, Approved: true,
	}
	approvalReceipt, err := environment.agent.ApproveTool(t.Context(), flowID, approvalRequest)
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && len(state.PendingToolCalls) == 0
	})
	replayedApproval, err := environment.agent.ApproveTool(t.Context(), flowID, approvalRequest)
	if err != nil || !replayedApproval.IsReplay || replayedApproval.AcceptedAt != approvalReceipt.AcceptedAt {
		t.Fatalf("replayed approval = %#v, %v; first = %#v", replayedApproval, err, approvalReceipt)
	}
	conflictingApproval := approvalRequest
	conflictingApproval.Approved = false
	_, approvalConflictErr := environment.agent.ApproveTool(t.Context(), flowID, conflictingApproval)
	var approvalConflict *CommandIdempotencyConflictError
	if !errors.As(approvalConflictErr, &approvalConflict) || approvalConflict.Command != CommandApproveTool {
		t.Fatalf("approval idempotency conflict = %T %v", approvalConflictErr, approvalConflictErr)
	}
	toolRegistry.assertCallsUseIdentity(t, flowID, approval.CallID)

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/tool", false)); err != nil {
		t.Fatal(err)
	}
	rejectedApproval := waitForPendingApproval(t, environment, flowID)
	if _, err := environment.agent.ApproveTool(t.Context(), flowID, ToolApprovalRequest{
		RequestID: integrationRequestID("approve"), CallID: rejectedApproval.CallID, Approved: false,
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

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("ship safely", true)); err != nil {
		t.Fatal(err)
	}
	plan := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	_, stalePlanErr := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		RequestID: integrationRequestID("execute-plan"), Revision: plan.Revision + 1,
	})
	var stalePlan *CommandRejectedError
	if !errors.As(stalePlanErr, &stalePlan) || stalePlan.Command != CommandExecutePlan {
		t.Fatalf("stale plan execution error = %T %v", stalePlanErr, stalePlanErr)
	}
	executionRequest := PlanExecutionRequest{
		RequestID: integrationRequestID("execute-plan"), Revision: plan.Revision,
	}
	executionReceipt, err := environment.agent.ExecutePlan(t.Context(), flowID, executionRequest)
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentPlan(t, environment, flowID, PlanStatusCompleted)
	replayedExecution, err := environment.agent.ExecutePlan(t.Context(), flowID, executionRequest)
	if err != nil || !replayedExecution.IsReplay || replayedExecution.AcceptedAt != executionReceipt.AcceptedAt {
		t.Fatalf("replayed plan execution = %#v, %v; first = %#v", replayedExecution, err, executionReceipt)
	}
	_, completedPlanErr := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		RequestID: integrationRequestID("execute-plan"), Revision: plan.Revision,
	})
	var completedPlan *CommandRejectedError
	if !errors.As(completedPlanErr, &completedPlan) || completedPlan.Command != CommandExecutePlan {
		t.Fatalf("completed plan execution error = %T %v", completedPlanErr, completedPlanErr)
	}

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/ask deployment region", false)); err != nil {
		t.Fatal(err)
	}
	pendingInput := waitForPendingUserInput(t, environment, flowID)
	environment.replaceWorker(t)
	if err := environment.agent.AnswerQuestions(t.Context(), flowID, answerRequest(pendingInput, "us-west")); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingUserInput(t, environment, flowID)

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/wait", false)); err != nil {
		t.Fatal(err)
	}
	waitForPendingTimer(t, environment, flowID)
	environment.replaceWorker(t)
	stateBeforeQueue := readAgentState(t, environment, flowID)
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("queued message", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("delete me", false)); err != nil {
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
	deleteRequest := DeleteQueuedMessageRequest{
		RequestID: integrationRequestID("delete"), MessageID: queued[1].Value.MessageID,
	}
	deleteReceipt, err := environment.agent.DeleteQueuedMessage(t.Context(), flowID, deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	waitForQueuedMessages(t, environment, flowID, 1)
	replayedDelete, deleteErr := environment.agent.DeleteQueuedMessage(t.Context(), flowID, deleteRequest)
	if deleteErr != nil || !replayedDelete.IsReplay || replayedDelete.AcceptedAt != deleteReceipt.AcceptedAt {
		t.Fatalf("replayed queue delete = %#v, %v; first = %#v", replayedDelete, deleteErr, deleteReceipt)
	}
	_, deleteErr = environment.agent.DeleteQueuedMessage(t.Context(), flowID, DeleteQueuedMessageRequest{
		RequestID: integrationRequestID("delete"), MessageID: queued[1].Value.MessageID,
	})
	var deletedMessageNotFound *PendingMessageNotFoundError
	if !errors.As(deleteErr, &deletedMessageNotFound) {
		t.Fatalf("new queue delete error = %T %v", deleteErr, deleteErr)
	}
	steerRequest := SteerMessageRequest{
		RequestID: integrationRequestID("steer"), MessageID: queued[0].Value.MessageID,
	}
	steerReceipt, err := environment.agent.SteerMessage(t.Context(), flowID, steerRequest)
	if err != nil {
		t.Fatal(err)
	}
	if steerReceipt.IsReplay {
		t.Fatalf("initial steer receipt = %#v", steerReceipt)
	}
	waitForNoPendingTimer(t, environment, flowID)
	waitForQueuedMessages(t, environment, flowID, 0)
	state = waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && state.LastSequence > stateBeforeQueue.LastSequence
	})
	if !historyContains(t, environment, flowID, state, MessageRoleUser, "queued message") {
		t.Fatal("steered message did not enter application history")
	}
	replayedSteer, steerErr := environment.agent.SteerMessage(t.Context(), flowID, steerRequest)
	if steerErr != nil || !replayedSteer.IsReplay || replayedSteer.AcceptedAt != steerReceipt.AcceptedAt {
		t.Fatalf("replayed steer = %#v, %v; first = %#v", replayedSteer, steerErr, steerReceipt)
	}
	_, steerErr = environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
		RequestID: integrationRequestID("steer"), MessageID: queued[0].Value.MessageID,
	})
	var pendingMessageNotFound *PendingMessageNotFoundError
	if !errors.As(steerErr, &pendingMessageNotFound) {
		t.Fatalf("new queue steer error = %T %v", steerErr, steerErr)
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
	if _, err := environment.agent.Start(t.Context(), flowID, NewAgentConfig()); err != nil {
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
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("status cycle", false)); err != nil {
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

func TestAgentEnsureStartedAndCancellationIdempotencyIntegration(t *testing.T) {
	toolRegistry := newIntegrationToolRegistry()
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, toolRegistry)
	flowID := FlowID("agent-ensure-" + randomLocalID(t))
	messageID := MessageID("initial-" + randomLocalID(t))
	request := EnsureStartRequest{
		RequestID:          RequestID("ensure-" + randomLocalID(t)),
		Config:             NewAgentConfig(),
		ApplicationContext: `{"session_id":"session-1","workspace_id":"workspace-1"}`,
		InitialMessage: &UserMessage{
			MessageID: messageID,
			Content:   "/wait",
		},
	}
	first, err := environment.agent.EnsureStarted(t.Context(), flowID, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.IsReplay || first.RunID == "" || first.AcceptedAt.IsZero() || first.InitialMessage == nil ||
		first.InitialMessage.IsReplay || first.InitialMessage.MessageID != messageID {
		t.Fatalf("first EnsureStarted receipt = %#v", first)
	}
	waitForPendingTimer(t, environment, flowID)

	replayContext, cancelReplay := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelReplay()
	replayed, err := environment.agent.EnsureStarted(replayContext, flowID, request)
	if err != nil {
		t.Fatalf("EnsureStarted replay during active turn: %v", err)
	}
	if !replayed.IsReplay || replayed.RunID != first.RunID || replayed.AcceptedAt != first.AcceptedAt ||
		replayed.InitialMessage == nil || !replayed.InitialMessage.IsReplay ||
		replayed.InitialMessage.AcceptedAt != first.InitialMessage.AcceptedAt {
		t.Fatalf("replayed EnsureStarted receipt = %#v; first = %#v", replayed, first)
	}

	message, found := readApplicationMessage(t, environment, flowID, 1)
	if !found || message.MessageID != messageID || message.CreatedAt != first.InitialMessage.AcceptedAt {
		t.Fatalf("committed initial message = %#v, found %t; receipt = %#v", message, found, first.InitialMessage)
	}

	identityConflictRequest := request
	identityConflictRequest.ApplicationContext = `{"session_id":"another-session"}`
	_, identityErr := environment.agent.EnsureStarted(t.Context(), flowID, identityConflictRequest)
	var identityConflict *StartIdentityConflictError
	if !errors.As(identityErr, &identityConflict) {
		t.Fatalf("EnsureStarted identity conflict = %T %v", identityErr, identityErr)
	}

	requestConflict := request
	conflictingMessage := *request.InitialMessage
	conflictingMessage.Content = "different initial request"
	requestConflict.InitialMessage = &conflictingMessage
	_, requestErr := environment.agent.EnsureStarted(t.Context(), flowID, requestConflict)
	var commandConflict *CommandIdempotencyConflictError
	if !errors.As(requestErr, &commandConflict) || commandConflict.Command != CommandStart {
		t.Fatalf("EnsureStarted request conflict = %T %v", requestErr, requestErr)
	}

	cancelRequestID := RequestID("cancel-" + randomLocalID(t))
	canceled, err := environment.agent.Cancel(t.Context(), flowID, cancelRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.IsReplay || canceled.FlowStatus != FlowStatusCanceled || canceled.AcceptedAt.IsZero() {
		t.Fatalf("first cancellation receipt = %#v", canceled)
	}
	replayedCancellation, err := environment.agent.Cancel(t.Context(), flowID, cancelRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if !replayedCancellation.IsReplay || replayedCancellation.FlowStatus != FlowStatusCanceled ||
		replayedCancellation.AcceptedAt != canceled.AcceptedAt {
		t.Fatalf("replayed cancellation = %#v; first = %#v", replayedCancellation, canceled)
	}
	_, terminalErr := environment.agent.Cancel(t.Context(), flowID, integrationRequestID("cancel"))
	var terminal *AgentAlreadyTerminalError
	if !errors.As(terminalErr, &terminal) || terminal.Status != FlowStatusCanceled {
		t.Fatalf("new cancellation against terminal Flow = %T %v", terminalErr, terminalErr)
	}

	toolFlowID := FlowID("agent-context-" + randomLocalID(t))
	toolContext := `{"session_id":"session-tool","sandbox_id":"sandbox-7"}`
	toolStart := EnsureStartRequest{
		RequestID:          RequestID("ensure-tool-" + randomLocalID(t)),
		Config:             NewAgentConfig(),
		ApplicationContext: toolContext,
		InitialMessage: &UserMessage{
			MessageID: MessageID("tool-message-" + randomLocalID(t)),
			Content:   "/tool",
		},
	}
	if _, err := environment.agent.EnsureStarted(t.Context(), toolFlowID, toolStart); err != nil {
		t.Fatal(err)
	}
	approval := waitForPendingApproval(t, environment, toolFlowID)
	snapshotJSON, err := json.Marshal(readSnapshot(t, environment, toolFlowID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshotJSON), toolContext) {
		t.Fatal("opaque application context leaked into the Agent Snapshot")
	}
	environment.replaceWorker(t)
	if _, err := environment.agent.ApproveTool(t.Context(), toolFlowID, ToolApprovalRequest{
		RequestID: integrationRequestID("approve"),
		CallID:    approval.CallID,
		Approved:  true,
	}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, toolFlowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && len(state.PendingToolCalls) == 0
	})
	toolRegistry.assertApplicationContext(t, toolFlowID, approval.CallID, toolContext)
}

func TestAgentMessageIdempotencyIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-message-idempotency-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, NewAgentConfig()); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/wait", false)); err != nil {
		t.Fatal(err)
	}
	waitForPendingTimer(t, environment, flowID)

	request := integrationUserMessage("one durable submission", false)
	first, err := environment.agent.SendMessage(t.Context(), flowID, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := environment.agent.SendMessage(t.Context(), flowID, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.IsReplay || !replayed.IsReplay || first.AcceptedAt.IsZero() || replayed.AcceptedAt != first.AcceptedAt {
		t.Fatalf("message receipts = first %#v, replay %#v", first, replayed)
	}

	secondRequest := request
	secondRequest.RequestID = integrationRequestID("send-alias")
	messageReplay, err := environment.agent.SendMessage(t.Context(), flowID, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !messageReplay.IsReplay || messageReplay.AcceptedAt != first.AcceptedAt {
		t.Fatalf("message-level replay = %#v; first = %#v", messageReplay, first)
	}

	requestConflict := request
	requestConflict.Message.Content = "same request ID, different payload"
	_, requestErr := environment.agent.SendMessage(t.Context(), flowID, requestConflict)
	var commandConflict *CommandIdempotencyConflictError
	if !errors.As(requestErr, &commandConflict) || commandConflict.Command != CommandSendMessage {
		t.Fatalf("send request conflict = %T %v", requestErr, requestErr)
	}

	messageConflictRequest := requestConflict
	messageConflictRequest.RequestID = integrationRequestID("send-conflict")
	_, messageErr := environment.agent.SendMessage(t.Context(), flowID, messageConflictRequest)
	var messageConflict *MessageIdempotencyConflictError
	if !errors.As(messageErr, &messageConflict) || messageConflict.MessageID != request.Message.MessageID {
		t.Fatalf("message identity conflict = %T %v", messageErr, messageErr)
	}

	queued := waitForQueuedMessages(t, environment, flowID, 1)
	if queued[0].Value.MessageID != request.Message.MessageID || queued[0].Value.AcceptedAt != first.AcceptedAt {
		t.Fatalf("queued message = %#v; receipt = %#v", queued[0].Value, first)
	}
	var accepted acceptedUserMessage
	found, err := environment.sdk.GetAttributeMapInstance(
		t.Context(),
		string(flowID),
		acceptedUserMessagesAttribute,
		acceptedMessageInstance(request.Message.MessageID),
		&accepted,
	)
	if err != nil || !found || accepted.MessageID != request.Message.MessageID || accepted.AcceptedAt != first.AcceptedAt {
		t.Fatalf("accepted-message record = %#v, found %t, error %v", accepted, found, err)
	}
	encodedAccepted, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedAccepted), request.Message.Content) {
		t.Fatalf("accepted-message record copied content: %s", encodedAccepted)
	}
}

func TestAgentMessageArchiveIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-archive-" + randomLocalID(t))
	config := NewAgentConfig()
	config.MaxContextTokens = 1_000_000
	if _, err := environment.agent.Start(t.Context(), flowID, config); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})

	for index := 1; index <= 15; index++ {
		content := fmt.Sprintf("archive %02d", index)
		if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage(content, false)); err != nil {
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
	if _, err := environment.agent.MessagesAfter(t.Context(), flowID, -1, 1); err == nil {
		t.Fatal("negative after sequence was accepted")
	}
	if _, err := environment.agent.MessagesAfter(t.Context(), flowID, 0, MaximumForwardHistoryLimit+1); err == nil {
		t.Fatal("oversized forward page was accepted")
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
	if _, err := environment.agent.Start(t.Context(), flowID, NewAgentConfig()); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/ask-many What date should I use?", false)); err != nil {
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
	environment.replaceWorker(t)
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

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/choose Where should I deploy? | Staging | Production", false)); err != nil {
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
	environment.replaceWorker(t)
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
	if _, err := environment.agent.Start(t.Context(), flowID, NewAgentConfig()); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("Plan the first objective", true)); err != nil {
		t.Fatal(err)
	}
	first := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("Plan the revised objective", true)); err != nil {
		t.Fatal(err)
	}
	revised := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Plan != nil &&
			snapshot.Description.Plan.Revision > first.Revision
	})
	if revised.Description.Plan.Tasks[0].Content != "Plan the revised objective" {
		t.Fatalf("revised plan = %#v", revised.Description.Plan)
	}
	_, oldRevisionErr := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		RequestID: integrationRequestID("execute-plan"), Revision: first.Revision,
	})
	var rejected *CommandRejectedError
	if !errors.As(oldRevisionErr, &rejected) {
		t.Fatalf("old revision error = %T %v", oldRevisionErr, oldRevisionErr)
	}
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/plan-clear", true)); err != nil {
		t.Fatal(err)
	}
	waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan == nil
	})

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/plan-stop demonstrate advisory completion", true)); err != nil {
		t.Fatal(err)
	}
	draftSnapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan != nil && snapshot.Description.Plan.Status == PlanStatusDraft
	})
	if _, err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		RequestID: integrationRequestID("execute-plan"), Revision: draftSnapshot.Description.Plan.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	active := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan != nil && snapshot.Description.Plan.Status == PlanStatusActive
	})
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/tool integration_tool {}", false)); err != nil {
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
}

func TestAgentPlanTaskActivityIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-plan-activity-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, NewAgentConfig()); err != nil {
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
	if _, err := environment.agent.Start(t.Context(), flowID, NewAgentConfig()); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/wait", false)); err != nil {
		t.Fatal(err)
	}
	waitForPendingTimer(t, environment, flowID)
	for _, content := range []string{"first replacement objective", "final replacement objective"} {
		if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage(content, false)); err != nil {
			t.Fatal(err)
		}
	}
	queued := waitForQueuedMessages(t, environment, flowID, 2)
	for _, message := range queued {
		if _, err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
			RequestID: integrationRequestID("steer"), MessageID: message.Value.MessageID,
		}); err != nil {
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
	runID, err := environment.agent.Start(t.Context(), flowID, NewAgentConfig())
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

func (environment *agentIntegrationEnvironment) replaceWorker(t *testing.T) {
	t.Helper()
	if err := environment.stopWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	environment.startWorker(t)
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
	mutex      sync.Mutex
	identities []toolInvocationIdentity
}

type toolInvocationIdentity struct {
	flowID             FlowID
	callID             CallID
	applicationContext string
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
	registry.identities = append(registry.identities, toolInvocationIdentity{
		flowID:             invocation.FlowID,
		callID:             invocation.CallID,
		applicationContext: invocation.ApplicationContext,
	})
	registry.mutex.Unlock()
	if err := invocation.WriteProgress("integration tool completed"); err != nil {
		return ToolExecutionResult{}, err
	}
	return ToolExecutionResult{Content: `{"ok":true}`, Outcome: ToolOutcomeSucceeded}, nil
}

func (registry *integrationToolRegistry) assertCallsUseIdentity(t *testing.T, flowID FlowID, callID CallID) {
	t.Helper()
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if len(registry.identities) != 1 {
		t.Fatalf("tool executions = %d, want exactly one", len(registry.identities))
	}
	for _, actual := range registry.identities {
		if actual.flowID != flowID {
			t.Fatalf("tool Flow ID changed across Worker replacement: got %q, want %q", actual.flowID, flowID)
		}
		if actual.callID != callID {
			t.Fatalf("tool call ID changed across Worker replacement: got %q, want %q", actual.callID, callID)
		}
	}
}

func (registry *integrationToolRegistry) assertApplicationContext(
	t *testing.T,
	flowID FlowID,
	callID CallID,
	applicationContext string,
) {
	t.Helper()
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	for _, actual := range registry.identities {
		if actual.flowID == flowID && actual.callID == callID {
			if actual.applicationContext != applicationContext {
				t.Fatalf("tool application context = %q, want %q", actual.applicationContext, applicationContext)
			}
			return
		}
	}
	t.Fatalf("tool invocation %q/%q was not recorded", flowID, callID)
}

func (registry *integrationToolRegistry) assertExecutionCount(t *testing.T, expected int) {
	t.Helper()
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if len(registry.identities) != expected {
		t.Fatalf("tool executions = %d, want %d", len(registry.identities), expected)
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

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
	if _, err := environment.agent.SendMessage(
		t.Context(),
		flowID,
		integrationUserMessage(`/tool integration_tool {"attempt":2}`, false),
	); err != nil {
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
	completedPlanReplay, completedPlanErr := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		RequestID: integrationRequestID("execute-plan"), Revision: plan.Revision,
	})
	if completedPlanErr != nil || !completedPlanReplay.IsReplay || completedPlanReplay.AcceptedAt != executionReceipt.AcceptedAt {
		t.Fatalf("completed plan execution replay = %#v, %v", completedPlanReplay, completedPlanErr)
	}

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/ask deployment region", false)); err != nil {
		t.Fatal(err)
	}
	pendingInput := waitForPendingUserInput(t, environment, flowID)
	environment.replaceWorker(t)
	if _, err := environment.agent.AnswerQuestions(t.Context(), flowID, answerRequest(pendingInput, "us-west")); err != nil {
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
		ApplicationContext: `{"account_id":"account-1","resource_id":"resource-1"}`,
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
		first.MutationRevision != 1 || first.InitialMessage.IsReplay ||
		first.InitialMessage.MessageID != messageID || first.InitialMessage.MutationRevision != 2 {
		t.Fatalf("first EnsureStarted receipt = %#v", first)
	}
	identity, err := environment.agent.VerifyIdentity(t.Context(), flowID, request.ApplicationContext)
	if err != nil {
		t.Fatal(err)
	}
	if identity.FlowID != flowID || identity.RunID != first.RunID || identity.IsTerminalReservation {
		t.Fatalf("active Agent identity = %#v", identity)
	}
	wrongContext := `{"session_id":"wrong-session"}`
	_, mismatchErr := environment.agent.VerifyIdentity(t.Context(), flowID, wrongContext)
	var identityMismatch *AgentIdentityMismatchError
	if !errors.As(mismatchErr, &identityMismatch) || identityMismatch.FlowID != flowID ||
		strings.Contains(mismatchErr.Error(), request.ApplicationContext) ||
		strings.Contains(mismatchErr.Error(), wrongContext) {
		t.Fatalf("Agent identity mismatch = %T %v", mismatchErr, mismatchErr)
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
	identityReplayRequest := request
	identityReplayRequest.RequestID = integrationRequestID("ensure-alias")
	identityReplay, err := environment.agent.EnsureStarted(t.Context(), flowID, identityReplayRequest)
	if err != nil {
		t.Fatalf("EnsureStarted global-identity replay: %v", err)
	}
	if !identityReplay.IsReplay || identityReplay.InitialMessage == nil || !identityReplay.InitialMessage.IsReplay ||
		identityReplay.InitialMessage.AcceptedAt != first.InitialMessage.AcceptedAt {
		t.Fatalf("global-identity replay = %#v; first = %#v", identityReplay, first)
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
	if !errors.As(requestErr, &identityConflict) {
		t.Fatalf("EnsureStarted global initial-message conflict = %T %v", requestErr, requestErr)
	}
	differentRequestConflict := requestConflict
	differentRequestConflict.RequestID = integrationRequestID("ensure-different-initial")
	_, differentRequestErr := environment.agent.EnsureStarted(t.Context(), flowID, differentRequestConflict)
	if !errors.As(differentRequestErr, &identityConflict) {
		t.Fatalf("EnsureStarted different-request initial-message conflict = %T %v", differentRequestErr, differentRequestErr)
	}

	terminalMessage := integrationUserMessage("accepted before terminal transition", false)
	terminalMessageReceipt, err := environment.agent.SendMessage(t.Context(), flowID, terminalMessage)
	if err != nil {
		t.Fatal(err)
	}
	deletedMessage := integrationUserMessage("deleted before terminal transition", false)
	if _, err := environment.agent.SendMessage(t.Context(), flowID, deletedMessage); err != nil {
		t.Fatal(err)
	}
	waitForQueuedMessages(t, environment, flowID, 2)
	deleteRequest := DeleteQueuedMessageRequest{
		RequestID: integrationRequestID("delete-terminal"),
		MessageID: deletedMessage.Message.MessageID,
	}
	deleteReceipt, err := environment.agent.DeleteQueuedMessage(t.Context(), flowID, deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	steerRequest := SteerMessageRequest{
		RequestID: integrationRequestID("steer-terminal"),
		MessageID: terminalMessage.Message.MessageID,
	}
	steerReceipt, err := environment.agent.SteerMessage(t.Context(), flowID, steerRequest)
	if err != nil {
		t.Fatal(err)
	}

	cancelRequestID := RequestID("cancel-" + randomLocalID(t))
	canceled, err := environment.agent.Cancel(t.Context(), flowID, cancelRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.IsReplay || canceled.FlowStatus != FlowStatusCanceled || canceled.AcceptedAt.IsZero() {
		t.Fatalf("first cancellation receipt = %#v", canceled)
	}
	if canceled.MutationRevision != steerReceipt.MutationRevision+1 {
		t.Fatalf("cancellation revision = %d, want %d", canceled.MutationRevision, steerReceipt.MutationRevision+1)
	}
	replayedCancellation, err := environment.agent.Cancel(t.Context(), flowID, cancelRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if !replayedCancellation.IsReplay || replayedCancellation.FlowStatus != FlowStatusCanceled ||
		replayedCancellation.AcceptedAt != canceled.AcceptedAt ||
		replayedCancellation.MutationRevision != canceled.MutationRevision {
		t.Fatalf("replayed cancellation = %#v; first = %#v", replayedCancellation, canceled)
	}
	terminalIdentity, err := environment.agent.VerifyIdentity(t.Context(), flowID, request.ApplicationContext)
	if err != nil {
		t.Fatal(err)
	}
	if terminalIdentity != identity {
		t.Fatalf("terminal Agent identity = %#v, active = %#v", terminalIdentity, identity)
	}
	terminalSnapshot := readSnapshot(t, environment, flowID)
	if terminalSnapshot.FlowStatus != FlowStatusCanceled ||
		terminalSnapshot.MutationRevision != canceled.MutationRevision {
		t.Fatalf("terminal Agent Snapshot = %#v", terminalSnapshot)
	}
	terminalStartReplay, err := environment.agent.EnsureStarted(t.Context(), flowID, request)
	if err != nil {
		t.Fatalf("EnsureStarted terminal replay: %v", err)
	}
	if !terminalStartReplay.IsReplay || terminalStartReplay.AcceptedAt != first.AcceptedAt ||
		terminalStartReplay.InitialMessage == nil ||
		terminalStartReplay.InitialMessage.AcceptedAt != first.InitialMessage.AcceptedAt {
		t.Fatalf("terminal start replay = %#v; first = %#v", terminalStartReplay, first)
	}
	terminalMessageReplay, err := environment.agent.SendMessage(t.Context(), flowID, terminalMessage)
	if err != nil {
		t.Fatalf("SendMessage terminal replay: %v", err)
	}
	if !terminalMessageReplay.IsReplay || terminalMessageReplay.AcceptedAt != terminalMessageReceipt.AcceptedAt {
		t.Fatalf("terminal message replay = %#v; first = %#v", terminalMessageReplay, terminalMessageReceipt)
	}
	terminalDeleteReplay, err := environment.agent.DeleteQueuedMessage(t.Context(), flowID, deleteRequest)
	if err != nil || !terminalDeleteReplay.IsReplay || terminalDeleteReplay.AcceptedAt != deleteReceipt.AcceptedAt {
		t.Fatalf("terminal delete replay = %#v, %v; first = %#v", terminalDeleteReplay, err, deleteReceipt)
	}
	terminalSteerReplay, err := environment.agent.SteerMessage(t.Context(), flowID, steerRequest)
	if err != nil || !terminalSteerReplay.IsReplay || terminalSteerReplay.AcceptedAt != steerReceipt.AcceptedAt {
		t.Fatalf("terminal steer replay = %#v, %v; first = %#v", terminalSteerReplay, err, steerReceipt)
	}
	_, terminalErr := environment.agent.Cancel(t.Context(), flowID, integrationRequestID("cancel"))
	var terminal *AgentAlreadyTerminalError
	if !errors.As(terminalErr, &terminal) || terminal.Status != FlowStatusCanceled {
		t.Fatalf("new cancellation against terminal Flow = %T %v", terminalErr, terminalErr)
	}

	toolFlowID := FlowID("agent-context-" + randomLocalID(t))
	toolContext := `{"account_id":"account-tool","resource_id":"resource-7"}`
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

func TestAgentOriginMainFlowCompatibilityIntegration(t *testing.T) {
	toolRegistry := newIntegrationToolRegistry()
	environment := &agentIntegrationEnvironment{
		flow:          NewFlow(integrationModel{}, toolRegistry),
		address:       availableLocalAddress(t, t.Context()),
		serverAddress: os.Getenv("DEX_FLOW_SERVICE_ADDRESS"),
	}
	if environment.serverAddress == "" {
		environment.serverAddress = "127.0.0.1:8801"
	}
	legacyFlow := &originMainAgentFlow{}
	environment.startWorkerWithFlow(t, legacyFlow, false)
	t.Cleanup(func() {
		if err := environment.close(t.Context()); err != nil {
			t.Errorf("close legacy integration environment: %v", err)
		}
	})

	config := NewAgentConfig()
	activeFlowID := FlowID("agent-legacy-active-" + randomLocalID(t))
	terminalFlowID := FlowID("agent-legacy-terminal-" + randomLocalID(t))
	for _, flowID := range []FlowID{activeFlowID, terminalFlowID} {
		requestID := "legacy-start:" + string(flowID)
		if _, err := environment.sdk.StartFlow(
			t.Context(),
			legacyFlow,
			string(flowID),
			config,
			dex.StartFlowOptions{IDReusePolicy: dex.IDReuseDisallow, RequestID: &requestID},
		); err != nil {
			t.Fatal(err)
		}
		var status AgentInteractionStatus
		if err := environment.sdk.WaitForAttributeMatch(
			t.Context(),
			string(flowID),
			originMainInteractionStatusAttribute,
			dex.AttributeMatchEqual(AgentInteractionStatusWaiting),
			&status,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := environment.sdk.StopFlow(t.Context(), string(terminalFlowID), dex.StopOptions{
		Type:   dex.TerminateFlow,
		Reason: "legacy terminal compatibility",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.sdk.WaitForFlow(t.Context(), string(terminalFlowID), dex.WaitForFlowOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := environment.stopWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	environment.startWorker(t)
	uninitializedFlowID := FlowID("agent-legacy-uninitialized-" + randomLocalID(t))
	uninitializedRequestID := "legacy-uninitialized:" + string(uninitializedFlowID)
	if _, err := environment.sdk.StartFlow(
		t.Context(),
		environment.flow,
		string(uninitializedFlowID),
		config,
		dex.StartFlowOptions{IDReusePolicy: dex.IDReuseDisallow, RequestID: &uninitializedRequestID},
	); err != nil {
		t.Fatal(err)
	}
	uninitializedResult, err := environment.sdk.WaitForFlow(
		t.Context(),
		string(uninitializedFlowID),
		dex.WaitForFlowOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if uninitializedResult.Status != dex.FlowFailed ||
		!strings.Contains(uninitializedResult.ErrorMessage, "legacy Agent start identity is unavailable") {
		t.Fatalf("uninitialized legacy Flow result = %#v", uninitializedResult)
	}

	unprovenRequest := EnsureStartRequest{
		RequestID: integrationRequestID("unproven-legacy"),
		Config:    config,
	}
	_, unprovenErr := environment.agent.EnsureStarted(t.Context(), activeFlowID, unprovenRequest)
	var legacyIdentity *LegacyStartIdentityError
	if !errors.As(unprovenErr, &legacyIdentity) {
		t.Fatalf("unproven active legacy identity = %T %v", unprovenErr, unprovenErr)
	}
	migrationRequest := EnsureStartRequest{
		RequestID: originMainStartRequestID(activeFlowID),
		Config:    config,
	}
	migrated, err := environment.agent.EnsureStarted(t.Context(), activeFlowID, migrationRequest)
	if err != nil {
		t.Fatalf("migrate active origin/main Flow: %v", err)
	}
	if !migrated.IsReplay {
		t.Fatalf("active legacy migration receipt = %#v", migrated)
	}
	snapshot := readSnapshot(t, environment, activeFlowID)
	if len(snapshot.History.Messages) != 1 || snapshot.History.Messages[0].Message.MessageID == "" ||
		!strings.HasPrefix(string(snapshot.History.Messages[0].Message.MessageID), "legacy:") {
		t.Fatalf("migrated legacy history = %#v", snapshot.History.Messages)
	}
	if _, err := environment.agent.SendMessage(t.Context(), activeFlowID, integrationUserMessage("/tool", false)); err != nil {
		t.Fatal(err)
	}
	approval := waitForPendingApproval(t, environment, activeFlowID)
	if _, err := environment.agent.ApproveTool(t.Context(), activeFlowID, ToolApprovalRequest{
		RequestID: integrationRequestID("approve-legacy"),
		CallID:    approval.CallID,
		Approved:  true,
	}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, activeFlowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && len(state.PendingToolCalls) == 0
	})
	toolRegistry.assertApplicationContext(t, activeFlowID, approval.CallID, "")

	conflictingMigration := migrationRequest
	conflictingMigration.RequestID = integrationRequestID("migrate-context")
	conflictingMigration.ApplicationContext = `{"resource_id":"cannot-infer"}`
	_, conflictErr := environment.agent.EnsureStarted(t.Context(), activeFlowID, conflictingMigration)
	var identityConflict *StartIdentityConflictError
	if !errors.As(conflictErr, &identityConflict) {
		t.Fatalf("migrated Flow context conflict = %T %v", conflictErr, conflictErr)
	}

	terminalReplay := EnsureStartRequest{RequestID: originMainStartRequestID(terminalFlowID), Config: config}
	replayed, terminalErr := environment.agent.EnsureStarted(t.Context(), terminalFlowID, terminalReplay)
	if terminalErr != nil || !replayed.IsReplay {
		t.Fatalf("terminal legacy exact replay = %#v, %v", replayed, terminalErr)
	}
	terminalUnproven := terminalReplay
	terminalUnproven.RequestID = integrationRequestID("unproven-terminal")
	_, terminalUnprovenErr := environment.agent.EnsureStarted(t.Context(), terminalFlowID, terminalUnproven)
	if !errors.As(terminalUnprovenErr, &legacyIdentity) {
		t.Fatalf("terminal unproven legacy identity = %T %v", terminalUnprovenErr, terminalUnprovenErr)
	}
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

func TestAgentMutationRevisionAndPendingAdmissionIntegration(t *testing.T) {
	modelClient := newBlockingIntegrationModel()
	environment := newAgentIntegrationEnvironment(t, modelClient, newIntegrationToolRegistry())
	t.Cleanup(modelClient.release)
	flowID := FlowID("agent-mutation-revision-" + randomLocalID(t))
	startRequest := EnsureStartRequest{
		RequestID: integrationRequestID("revision-start"),
		Config:    NewAgentConfig(),
	}
	started, err := environment.agent.EnsureStarted(t.Context(), flowID, startRequest)
	if err != nil {
		t.Fatal(err)
	}
	if started.MutationRevision != 1 {
		t.Fatalf("start mutation revision = %d, want 1", started.MutationRevision)
	}
	initialSnapshot := readSnapshot(t, environment, flowID)
	if initialSnapshot.MutationRevision != started.MutationRevision {
		t.Fatalf("initial Snapshot revision = %d, want %d", initialSnapshot.MutationRevision, started.MutationRevision)
	}

	blockRequest := integrationUserMessage("/block", false)
	blockRequest.ExpectedRevision = mutationRevisionPointer(initialSnapshot.MutationRevision)
	blocked, err := environment.agent.SendMessage(t.Context(), flowID, blockRequest)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.MutationRevision != 2 {
		t.Fatalf("blocking message revision = %d, want 2", blocked.MutationRevision)
	}
	modelClient.waitUntilBlocked(t)

	staleRequest := integrationUserMessage("stale mutation", false)
	staleRequest.ExpectedRevision = mutationRevisionPointer(initialSnapshot.MutationRevision)
	_, staleErr := environment.agent.SendMessage(t.Context(), flowID, staleRequest)
	var staleRevision *StaleMutationRevisionError
	if !errors.As(staleErr, &staleRevision) || staleRevision.Command != CommandSendMessage ||
		staleRevision.Expected != 1 || staleRevision.Actual != 2 {
		t.Fatalf("stale mutation error = %T %v", staleErr, staleErr)
	}

	replayed, err := environment.agent.SendMessage(t.Context(), flowID, blockRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.IsReplay || replayed.MutationRevision != blocked.MutationRevision {
		t.Fatalf("replayed message = %#v, first = %#v", replayed, blocked)
	}

	queuedRequest := integrationUserMessage("move to steering", false)
	queuedRequest.ExpectedRevision = mutationRevisionPointer(blocked.MutationRevision)
	queuedReceipt, err := environment.agent.SendMessage(t.Context(), flowID, queuedRequest)
	if err != nil {
		t.Fatal(err)
	}
	steerRequest := SteerMessageRequest{
		RequestID:        integrationRequestID("revision-steer"),
		MessageID:        queuedRequest.Message.MessageID,
		ExpectedRevision: mutationRevisionPointer(queuedReceipt.MutationRevision),
	}
	steeredReceipt, err := environment.agent.SteerMessage(t.Context(), flowID, steerRequest)
	if err != nil {
		t.Fatal(err)
	}
	if steeredReceipt.MutationRevision != queuedReceipt.MutationRevision+1 {
		t.Fatalf("steer revision = %d, want %d", steeredReceipt.MutationRevision, queuedReceipt.MutationRevision+1)
	}

	directMessages := make([]any, MaximumPendingMessageCount-2)
	for index := range directMessages {
		directMessages[index] = UserMessage{
			MessageID:  MessageID(fmt.Sprintf("capacity-fixture-%03d", index)),
			Content:    "capacity fixture",
			AcceptedAt: time.Now().UTC(),
		}
	}
	if err := environment.sdk.PublishToChannel(
		t.Context(),
		string(flowID),
		queuedUserMessagesChannel,
		directMessages...,
	); err != nil {
		t.Fatal(err)
	}
	waitForPendingMessageTotal(t, environment, flowID, MaximumPendingMessageCount-1)

	raceRequests := []SendMessageRequest{
		integrationUserMessage("capacity race one", false),
		integrationUserMessage("capacity race two", false),
	}
	races := invokeConcurrentMessages(t, environment, flowID, raceRequests)
	acceptedCount := 0
	var retryRequest *SendMessageRequest
	for index := range races {
		if races[index].err == nil {
			acceptedCount++
			continue
		}
		var capacity *PendingMessageCapacityError
		var lockConflict *dex.RPCLockConflictError
		if !errors.As(races[index].err, &capacity) && !errors.As(races[index].err, &lockConflict) {
			t.Fatalf("capacity race error = %T %v", races[index].err, races[index].err)
		}
		retryRequest = &raceRequests[index]
	}
	if acceptedCount != 1 {
		t.Fatalf("capacity race accepted %d messages, want 1", acceptedCount)
	}
	waitForPendingMessageTotal(t, environment, flowID, MaximumPendingMessageCount)
	if retryRequest == nil {
		extra := integrationUserMessage("capacity rejection", false)
		retryRequest = &extra
	}
	_, capacityErr := environment.agent.SendMessage(t.Context(), flowID, *retryRequest)
	var capacity *PendingMessageCapacityError
	if !errors.As(capacityErr, &capacity) || capacity.Pending != MaximumPendingMessageCount ||
		capacity.Limit != MaximumPendingMessageCount {
		t.Fatalf("capacity error = %T %v", capacityErr, capacityErr)
	}
	fullSnapshot := readSnapshot(t, environment, flowID)
	if len(fullSnapshot.Queued)+len(fullSnapshot.Steered) != MaximumPendingMessageCount ||
		fullSnapshot.MutationRevision != steeredReceipt.MutationRevision+1 {
		t.Fatalf("full pending Snapshot = revision %d, queued %d, steered %d", fullSnapshot.MutationRevision, len(fullSnapshot.Queued), len(fullSnapshot.Steered))
	}

	_, staleReplayErr := environment.agent.SendMessage(t.Context(), flowID, staleRequest)
	var staleReplay *StaleMutationRevisionError
	if !errors.As(staleReplayErr, &staleReplay) || staleReplay.Actual != staleRevision.Actual {
		t.Fatalf("stale command replay = %T %v", staleReplayErr, staleReplayErr)
	}
	modelClient.release()
}

func TestAgentPendingMessageContentAdmissionIntegration(t *testing.T) {
	modelClient := newBlockingIntegrationModel()
	environment := newAgentIntegrationEnvironment(t, modelClient, newIntegrationToolRegistry())
	t.Cleanup(modelClient.release)
	flowID := FlowID("agent-pending-content-" + randomLocalID(t))
	if _, err := environment.agent.EnsureStarted(t.Context(), flowID, EnsureStartRequest{
		RequestID: integrationRequestID("content-start"),
		Config:    NewAgentConfig(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.agent.SendMessage(
		t.Context(),
		flowID,
		integrationUserMessage("/block", false),
	); err != nil {
		t.Fatal(err)
	}
	modelClient.waitUntilBlocked(t)

	largeRequest := integrationUserMessage(
		strings.Repeat("x", MaximumPendingMessageContentBytes-1),
		false,
	)
	largeReceipt, err := environment.agent.SendMessage(t.Context(), flowID, largeRequest)
	if err != nil {
		t.Fatal(err)
	}
	steeredReceipt, err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
		RequestID:        integrationRequestID("content-steer"),
		MessageID:        largeRequest.Message.MessageID,
		ExpectedRevision: mutationRevisionPointer(largeReceipt.MutationRevision),
	})
	if err != nil {
		t.Fatal(err)
	}

	raceRequests := []SendMessageRequest{
		integrationUserMessage("a", false),
		integrationUserMessage("b", false),
	}
	races := invokeConcurrentMessages(t, environment, flowID, raceRequests)
	acceptedCount := 0
	var retryRequest *SendMessageRequest
	for index := range races {
		if races[index].err == nil {
			acceptedCount++
			continue
		}
		var capacity *PendingMessageCapacityError
		var lockConflict *dex.RPCLockConflictError
		if !errors.As(races[index].err, &capacity) && !errors.As(races[index].err, &lockConflict) {
			t.Fatalf("content capacity race error = %T %v", races[index].err, races[index].err)
		}
		retryRequest = &raceRequests[index]
	}
	if acceptedCount != 1 || retryRequest == nil {
		t.Fatalf("content capacity race accepted %d messages; results = %#v", acceptedCount, races)
	}
	_, capacityErr := environment.agent.SendMessage(t.Context(), flowID, *retryRequest)
	var capacity *PendingMessageCapacityError
	if !errors.As(capacityErr, &capacity) || capacity.Pending != 2 ||
		capacity.Limit != MaximumPendingMessageCount ||
		capacity.PendingContentBytes != MaximumPendingMessageContentBytes ||
		capacity.RequestedContentBytes != 1 ||
		capacity.ContentByteLimit != MaximumPendingMessageContentBytes {
		t.Fatalf("content capacity error = %T %#v", capacityErr, capacity)
	}
	snapshot := readSnapshot(t, environment, flowID)
	if len(snapshot.Queued) != 1 || len(snapshot.Steered) != 1 ||
		pendingSnapshotContentBytes(snapshot) != MaximumPendingMessageContentBytes ||
		snapshot.MutationRevision != steeredReceipt.MutationRevision+1 {
		t.Fatalf(
			"bounded content Snapshot: queued %d, steered %d, bytes %d, revision %d",
			len(snapshot.Queued),
			len(snapshot.Steered),
			pendingSnapshotContentBytes(snapshot),
			snapshot.MutationRevision,
		)
	}
}

func pendingSnapshotContentBytes(snapshot AgentSnapshot) int {
	contentBytes := 0
	for _, message := range snapshot.Queued {
		contentBytes += len(message.Value.Content)
	}
	for _, message := range snapshot.Steered {
		contentBytes += len(message.Value.Content)
	}
	return contentBytes
}

func TestAgentCancelBeforeStartReservesIdentityIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-cancel-before-start-" + randomLocalID(t))
	missingFlowID := FlowID("agent-identity-absent-" + randomLocalID(t))
	_, missingErr := environment.agent.VerifyIdentity(t.Context(), missingFlowID, "")
	var identityNotFound *AgentIdentityNotFoundError
	if !errors.As(missingErr, &identityNotFound) || identityNotFound.FlowID != missingFlowID {
		t.Fatalf("absent Agent identity = %T %v", missingErr, missingErr)
	}
	request := CancelRequest{
		RequestID: integrationRequestID("cancel-before-start"),
		Reason:    "session was deleted before the Agent started",
	}
	first, err := environment.agent.EnsureCanceled(t.Context(), flowID, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.IsReplay || first.FlowStatus != FlowStatusCanceled || first.MutationRevision != 1 {
		t.Fatalf("first cancellation reservation = %#v", first)
	}
	reservedIdentity, err := environment.agent.VerifyIdentity(t.Context(), flowID, "")
	if err != nil {
		t.Fatal(err)
	}
	if reservedIdentity.FlowID != flowID || reservedIdentity.RunID == "" || !reservedIdentity.IsTerminalReservation {
		t.Fatalf("reserved Agent identity = %#v", reservedIdentity)
	}
	_, reservedMismatchErr := environment.agent.VerifyIdentity(t.Context(), flowID, `{"session_id":"not-reserved"}`)
	var reservedMismatch *AgentIdentityMismatchError
	if !errors.As(reservedMismatchErr, &reservedMismatch) {
		t.Fatalf("reserved Agent identity mismatch = %T %v", reservedMismatchErr, reservedMismatchErr)
	}
	replayed, err := environment.agent.EnsureCanceled(t.Context(), flowID, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.IsReplay || replayed.AcceptedAt != first.AcceptedAt ||
		replayed.MutationRevision != first.MutationRevision {
		t.Fatalf("replayed cancellation reservation = %#v, first = %#v", replayed, first)
	}

	conflicting := request
	conflicting.Reason = "different reason"
	_, conflictErr := environment.agent.EnsureCanceled(t.Context(), flowID, conflicting)
	var conflict *CommandIdempotencyConflictError
	if !errors.As(conflictErr, &conflict) || conflict.Command != CommandCancel {
		t.Fatalf("cancellation reservation conflict = %T %v", conflictErr, conflictErr)
	}
	_, startErr := environment.agent.EnsureStarted(t.Context(), flowID, EnsureStartRequest{
		RequestID: integrationRequestID("start-after-cancel"),
		Config:    NewAgentConfig(),
	})
	var terminal *AgentAlreadyTerminalError
	if !errors.As(startErr, &terminal) || terminal.Status != FlowStatusCanceled {
		t.Fatalf("start after cancellation reservation = %T %v", startErr, startErr)
	}
	snapshot := readSnapshot(t, environment, flowID)
	if snapshot.FlowStatus != FlowStatusCanceled || snapshot.MutationRevision != first.MutationRevision {
		t.Fatalf("reserved terminal Snapshot = %#v", snapshot)
	}
	history, err := environment.agent.MessagesAfter(t.Context(), flowID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 0 || history.NextAfterSequence != nil ||
		history.FirstRetainedSequence != 0 || history.LastSequence != 0 || history.IsTruncated {
		t.Fatalf("reserved terminal history = %#v", history)
	}

	raceFlowID := FlowID("agent-start-cancel-race-" + randomLocalID(t))
	raceStart := EnsureStartRequest{
		RequestID: integrationRequestID("racing-start"),
		Config:    NewAgentConfig(),
	}
	raceCancel := CancelRequest{
		RequestID: integrationRequestID("racing-cancel"),
		Reason:    "concurrent lifecycle cleanup",
	}
	gate := make(chan struct{})
	startResult := make(chan error, 1)
	cancelResult := make(chan error, 1)
	go func() {
		<-gate
		_, startErr := environment.agent.EnsureStarted(t.Context(), raceFlowID, raceStart)
		startResult <- startErr
	}()
	go func() {
		<-gate
		_, cancelErr := environment.agent.EnsureCanceled(t.Context(), raceFlowID, raceCancel)
		cancelResult <- cancelErr
	}()
	close(gate)
	if err := <-cancelResult; err != nil {
		t.Fatalf("racing cancellation: %v", err)
	}
	_ = <-startResult
	raceSnapshot := readSnapshot(t, environment, raceFlowID)
	if raceSnapshot.FlowStatus != FlowStatusCanceled {
		t.Fatalf("racing lifecycle Snapshot = %#v", raceSnapshot)
	}
}

type concurrentMessageResult struct {
	index   int
	receipt MessageReceipt
	err     error
}

func invokeConcurrentMessages(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	requests []SendMessageRequest,
) []concurrentMessageResult {
	t.Helper()
	gate := make(chan struct{})
	results := make(chan concurrentMessageResult, len(requests))
	var waitGroup sync.WaitGroup
	for index, request := range requests {
		request := request
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-gate
			receipt, err := environment.agent.SendMessage(t.Context(), flowID, request)
			results <- concurrentMessageResult{index: index, receipt: receipt, err: err}
		}()
	}
	close(gate)
	waitGroup.Wait()
	close(results)
	collected := make([]concurrentMessageResult, len(requests))
	for result := range results {
		collected[result.index] = result
	}
	return collected
}

func mutationRevisionPointer(revision MutationRevision) *MutationRevision {
	return &revision
}

func TestAgentConcurrentDomainCommandFencingIntegration(t *testing.T) {
	toolRegistry := newIntegrationToolRegistry()
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, toolRegistry)
	flowID := FlowID("agent-command-fence-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, NewAgentConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/tool", false)); err != nil {
		t.Fatal(err)
	}
	approval := waitForPendingApproval(t, environment, flowID)
	approvalRequests := []ToolApprovalRequest{
		{RequestID: integrationRequestID("approve-race"), CallID: approval.CallID, Approved: true},
		{RequestID: integrationRequestID("approve-race"), CallID: approval.CallID, Approved: true},
	}
	approvalReceipts := invokeConcurrentApprovals(t, environment, flowID, approvalRequests)
	assertOneDomainAcceptance(t, approvalReceipts)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && len(state.PendingToolCalls) == 0
	})
	toolRegistry.assertExecutionCount(t, 1)
	oppositeRequest := ToolApprovalRequest{
		RequestID: integrationRequestID("approve-opposite"),
		CallID:    approval.CallID,
		Approved:  false,
	}
	_, oppositeErr := environment.agent.ApproveTool(t.Context(), flowID, oppositeRequest)
	var rejected *CommandRejectedError
	if !errors.As(oppositeErr, &rejected) || rejected.Command != CommandApproveTool {
		t.Fatalf("opposite approval after fenced acceptance = %T %v", oppositeErr, oppositeErr)
	}

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("ship safely", true)); err != nil {
		t.Fatal(err)
	}
	plan := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	planRequests := []PlanExecutionRequest{
		{RequestID: integrationRequestID("plan-race"), Revision: plan.Revision},
		{RequestID: integrationRequestID("plan-race"), Revision: plan.Revision},
	}
	planReceipts := invokeConcurrentPlanExecutions(t, environment, flowID, planRequests)
	assertOneDomainAcceptance(t, planReceipts)
	waitForAgentPlan(t, environment, flowID, PlanStatusCompleted)
	if err := environment.sdk.StopFlow(t.Context(), string(flowID), dex.StopOptions{
		Type:   dex.TerminateFlow,
		Reason: "terminal command reconciliation integration",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.sdk.WaitForFlow(t.Context(), string(flowID), dex.WaitForFlowOptions{}); err != nil {
		t.Fatal(err)
	}
	terminalApproval, err := environment.agent.ApproveTool(t.Context(), flowID, approvalRequests[0])
	if err != nil || !terminalApproval.IsReplay || terminalApproval.AcceptedAt != approvalReceipts[0].AcceptedAt ||
		terminalApproval.MutationRevision != approvalReceipts[0].MutationRevision {
		t.Fatalf("terminal approval replay = %#v, %v", terminalApproval, err)
	}
	terminalPlan, err := environment.agent.ExecutePlan(t.Context(), flowID, planRequests[0])
	if err != nil || !terminalPlan.IsReplay || terminalPlan.AcceptedAt != planReceipts[0].AcceptedAt ||
		terminalPlan.MutationRevision != planReceipts[0].MutationRevision {
		t.Fatalf("terminal plan replay = %#v, %v", terminalPlan, err)
	}
}

func invokeConcurrentApprovals(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	requests []ToolApprovalRequest,
) []CommandReceipt {
	t.Helper()
	type result struct {
		index   int
		receipt CommandReceipt
		err     error
	}
	gate := make(chan struct{})
	results := make(chan result, len(requests))
	var wg sync.WaitGroup
	for index, request := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			receipt, err := environment.agent.ApproveTool(t.Context(), flowID, request)
			results <- result{index: index, receipt: receipt, err: err}
		}()
	}
	close(gate)
	wg.Wait()
	close(results)
	receipts := make([]CommandReceipt, len(requests))
	for result := range results {
		if result.err != nil {
			var conflict *dex.RPCLockConflictError
			if !errors.As(result.err, &conflict) {
				t.Fatalf("concurrent approval %d: %v", result.index, result.err)
			}
			result.receipt, result.err = environment.agent.ApproveTool(t.Context(), flowID, requests[result.index])
		}
		if result.err != nil {
			t.Fatalf("retry concurrent approval %d: %v", result.index, result.err)
		}
		receipts[result.index] = result.receipt
	}
	return receipts
}

func invokeConcurrentPlanExecutions(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	requests []PlanExecutionRequest,
) []CommandReceipt {
	t.Helper()
	type result struct {
		index   int
		receipt CommandReceipt
		err     error
	}
	gate := make(chan struct{})
	results := make(chan result, len(requests))
	var wg sync.WaitGroup
	for index, request := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			receipt, err := environment.agent.ExecutePlan(t.Context(), flowID, request)
			results <- result{index: index, receipt: receipt, err: err}
		}()
	}
	close(gate)
	wg.Wait()
	close(results)
	receipts := make([]CommandReceipt, len(requests))
	for result := range results {
		if result.err != nil {
			var conflict *dex.RPCLockConflictError
			if !errors.As(result.err, &conflict) {
				t.Fatalf("concurrent plan execution %d: %v", result.index, result.err)
			}
			result.receipt, result.err = environment.agent.ExecutePlan(t.Context(), flowID, requests[result.index])
		}
		if result.err != nil {
			t.Fatalf("retry concurrent plan execution %d: %v", result.index, result.err)
		}
		receipts[result.index] = result.receipt
	}
	return receipts
}

func assertOneDomainAcceptance(t *testing.T, receipts []CommandReceipt) {
	t.Helper()
	if len(receipts) != 2 || receipts[0].AcceptedAt.IsZero() || receipts[0].AcceptedAt != receipts[1].AcceptedAt ||
		receipts[0].MutationRevision <= 0 || receipts[0].MutationRevision != receipts[1].MutationRevision {
		t.Fatalf("fenced command receipts = %#v", receipts)
	}
	replayCount := 0
	for _, receipt := range receipts {
		if receipt.IsReplay {
			replayCount++
		}
	}
	if replayCount != 1 {
		t.Fatalf("fenced command replay count = %d; receipts = %#v", replayCount, receipts)
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
	_, rejectedMessage := environment.agent.SendMessage(
		t.Context(),
		flowID,
		integrationUserMessage("September 12", false),
	)
	var sendRejected *CommandRejectedError
	if !errors.As(rejectedMessage, &sendRejected) || sendRejected.Command != CommandSendMessage {
		t.Fatalf("message while question pending = %T %v", rejectedMessage, rejectedMessage)
	}
	if _, err := environment.agent.AnswerQuestions(
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
	_, staleAnswer := environment.agent.AnswerQuestions(
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
	if _, err := environment.agent.AnswerQuestions(t.Context(), flowID, answerRequest(*input, "Production")); err != nil {
		t.Fatal(err)
	}
	waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.PendingUserInput == nil &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: **Details**: Production")
	})

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/questions", false)); err != nil {
		t.Fatal(err)
	}
	multi := waitForPendingUserInput(t, environment, flowID)
	if len(multi.Questions) != 3 {
		t.Fatalf("question count = %d, want 3", len(multi.Questions))
	}
	missing := answerBatchRequest(multi.CallID, []UserInputAnswer{
		{QuestionID: "region", Answer: "West"},
		{QuestionID: "pace", Answer: "Careful"},
	})
	_, missingErr := environment.agent.AnswerQuestions(t.Context(), flowID, missing)
	assertAnswerRejected(t, missingErr)
	unknown := answerBatchRequest(multi.CallID, []UserInputAnswer{
		{QuestionID: "region", Answer: "West"},
		{QuestionID: "pace", Answer: "Careful"},
		{QuestionID: "unknown", Answer: "Detailed"},
	})
	_, unknownErr := environment.agent.AnswerQuestions(t.Context(), flowID, unknown)
	assertAnswerRejected(t, unknownErr)
	if current := readSnapshot(t, environment, flowID); current.Description == nil ||
		current.Description.PendingUserInput == nil || current.Description.PendingUserInput.CallID != multi.CallID {
		t.Fatalf("invalid answer changed pending batch: %#v", current.Description)
	}

	answer := answerBatchRequest(multi.CallID, []UserInputAnswer{
		{QuestionID: "format", Answer: "Detailed"},
		{QuestionID: "region", Answer: "West"},
		{QuestionID: "pace", Answer: "Careful"},
	})
	environment.replaceWorker(t)
	type answerResult struct {
		receipt MessageReceipt
		err     error
	}
	results := make(chan answerResult, 2)
	for range 2 {
		go func() {
			receipt, err := environment.agent.AnswerQuestions(t.Context(), flowID, answer)
			results <- answerResult{receipt: receipt, err: err}
		}()
	}
	accepted := 0
	replayed := 0
	receipts := make([]MessageReceipt, 0, 2)
	for range 2 {
		result := <-results
		if result.err == nil {
			accepted++
			receipts = append(receipts, result.receipt)
			if result.receipt.IsReplay {
				replayed++
			}
			continue
		}
		t.Fatalf("concurrent answer error = %T %v", result.err, result.err)
	}
	if accepted != 2 || replayed != 1 {
		t.Fatalf("concurrent answers accepted/replayed = %d/%d, want 2/1", accepted, replayed)
	}
	if receipts[0].AcceptedAt != receipts[1].AcceptedAt ||
		receipts[0].MutationRevision != receipts[1].MutationRevision ||
		receipts[0].MutationRevision <= 0 {
		t.Fatalf("concurrent answer receipts = %#v", receipts)
	}
	const combinedAnswer = "**Region**: West\n\n**Pace**: Careful\n\n**Format**: Detailed"
	waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description != nil && snapshot.Description.PendingUserInput == nil &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: "+combinedAnswer)
	})

	if _, err := environment.agent.SendMessage(t.Context(), flowID, integrationUserMessage("/questions", false)); err != nil {
		t.Fatal(err)
	}
	nextBatch := waitForPendingUserInput(t, environment, flowID)
	if nextBatch.CallID == multi.CallID {
		t.Fatal("a later question batch reused the resolved call ID")
	}
	if _, err := environment.agent.AnswerQuestions(t.Context(), flowID, answerBatchRequest(
		nextBatch.CallID,
		[]UserInputAnswer{
			{QuestionID: "region", Answer: "East"},
			{QuestionID: "pace", Answer: "Fast"},
			{QuestionID: "format", Answer: "Short"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingUserInput(t, environment, flowID)

	if _, err := environment.agent.SendMessage(
		t.Context(),
		flowID,
		integrationUserMessage("/plan-question deployment", true),
	); err != nil {
		t.Fatal(err)
	}
	plan := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if _, err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		RequestID: integrationRequestID("execute-question-plan"), Revision: plan.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	planInput := waitForPendingUserInput(t, environment, flowID)
	if _, err := environment.agent.AnswerQuestions(t.Context(), flowID, answerRequest(planInput, "Production")); err != nil {
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
	if _, err := environment.agent.SendMessage(
		t.Context(),
		flowID,
		integrationUserMessage("stream task progress", true),
	); err != nil {
		t.Fatal(err)
	}
	draft := waitForAgentPlan(t, environment, flowID, PlanStatusDraft)
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if _, err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		RequestID: integrationRequestID("execute-activity-plan"), Revision: draft.Revision,
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
	return answerBatchRequest(input.CallID, []UserInputAnswer{{
		QuestionID: input.Questions[0].ID,
		Answer:     answer,
	}})
}

func answerBatchRequest(callID CallID, answers []UserInputAnswer) AnswerQuestionsRequest {
	identity := "answer:" + encodedFingerprint([]byte(fmt.Sprintf("%s:%v", callID, answers)))
	return AnswerQuestionsRequest{
		RequestID: RequestID(identity),
		MessageID: MessageID(identity),
		CallID:    callID,
		Answers:   answers,
	}
}

var (
	originMainConfigAttribute             = dex.DefineAttribute[AgentConfig]("AgentConfig")
	originMainApplicationContextAttribute = dex.DefineAttribute[string]("ApplicationContext")
	originMainInitializedAttribute        = dex.DefineAttribute[bool]("AgentInitialized")
	originMainStateAttribute              = dex.DefineAttribute[AgentState]("AgentState")
	originMainInteractionStatusAttribute  = dex.DefineAttribute[AgentInteractionStatus]("AgentInteractionStatus")
	originMainMessagesAttribute           = dex.DefineAttributeMap[originMainAgentMessage]("CurrentMessages")
	originMainDurableCommandsAttribute    = dex.DefineAttributeMap[durableCommandRecord]("DurableCommands")
	originMainQueuedChannel               = dex.DefineChannel[originMainUserMessage]("QueuedUserMessages")
)

type originMainUserMessage struct {
	Content  string `json:"content"`
	PlanMode bool   `json:"plan_mode"`
}

type originMainAgentMessage struct {
	Role                 MessageRole           `json:"role"`
	Content              string                `json:"content"`
	ToolCalls            []ToolCall            `json:"tool_calls"`
	ToolCallID           *CallID               `json:"tool_call_id,omitempty"`
	ToolName             *ToolName             `json:"tool_name,omitempty"`
	ProviderContextItems []ProviderContextItem `json:"provider_context_items"`
	CreatedAt            time.Time             `json:"created_at"`
}

type originMainAgentFlow struct{}

var _ dex.Flow = (*originMainAgentFlow)(nil)

func (*originMainAgentFlow) GetFlowType() string { return flowTypeAIAgent }

func (*originMainAgentFlow) GetSteps() []dex.StepDef {
	return []dex.StepDef{
		dex.DefineStartStep(originMainInitStep{}),
		dex.DefineStep(originMainAwaitStep{}),
	}
}

func (*originMainAgentFlow) GetPersistenceSchema() dex.PersistenceSchema {
	return dex.PersistenceSchema{
		Attributes: []dex.AttributeDef{
			originMainConfigAttribute,
			originMainApplicationContextAttribute,
			originMainInitializedAttribute,
			originMainStateAttribute,
			originMainInteractionStatusAttribute,
			originMainMessagesAttribute,
			originMainDurableCommandsAttribute,
		},
		Channels: []dex.ChannelDef{originMainQueuedChannel},
	}
}

type originMainInitStep struct {
	dex.StepDefaultsNoWaitFor[AgentConfig]
}

var _ dex.Step[AgentConfig] = originMainInitStep{}

func (originMainInitStep) GetStepType() string { return string(stepTypeInit) }

func (originMainInitStep) Execute(ctx dex.Context, config AgentConfig) (*dex.StepDecision, error) {
	state := NewAgentState()
	state.NextSequence = 2
	state.LastSequence = 1
	if err := originMainConfigAttribute.Set(ctx, config); err != nil {
		return nil, err
	}
	if err := originMainApplicationContextAttribute.Set(ctx, ""); err != nil {
		return nil, err
	}
	if err := originMainStateAttribute.Set(ctx, state); err != nil {
		return nil, err
	}
	if err := originMainInteractionStatusAttribute.Set(ctx, AgentInteractionStatusSubmitted); err != nil {
		return nil, err
	}
	if err := originMainInitializedAttribute.Set(ctx, true); err != nil {
		return nil, err
	}
	requestID := originMainStartRequestID(FlowID(ctx.FlowID()))
	fingerprint, err := (EnsureStartRequest{RequestID: requestID, Config: config}).fingerprint()
	if err != nil {
		return nil, err
	}
	if err := originMainDurableCommandsAttribute.Set(
		ctx,
		durableCommandInstance(CommandStart, requestID),
		durableCommandRecord{
			RequestID:   requestID,
			Command:     CommandStart,
			Fingerprint: fingerprint,
			RecordedAt:  time.Now().UTC(),
			Outcome:     durableCommandAccepted,
		},
	); err != nil {
		return nil, err
	}
	if err := originMainMessagesAttribute.Set(ctx, sequenceKey(1), originMainAgentMessage{
		Role:                 MessageRoleUser,
		Content:              "message persisted by origin/main",
		ToolCalls:            []ToolCall{},
		ProviderContextItems: []ProviderContextItem{},
		CreatedAt:            time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	return dex.GoTo(originMainAwaitStep{}, nil), nil
}

func originMainStartRequestID(flowID FlowID) RequestID {
	return RequestID("origin-main-start:" + string(flowID))
}

type originMainAwaitStep struct {
	dex.StepDefaults
}

var _ dex.Step[dex.None] = originMainAwaitStep{}

func (originMainAwaitStep) GetStepType() string { return string(stepTypeAwaitUser) }

func (originMainAwaitStep) GetStepOptions() *dex.StepOptions {
	return &dex.StepOptions{ExecuteLoadAttributeMaps: []dex.AttributeDef{originMainMessagesAttribute}}
}

func (originMainAwaitStep) WaitFor(ctx dex.Context, _ dex.None) (*dex.Wait, error) {
	if err := originMainInteractionStatusAttribute.Set(ctx, AgentInteractionStatusWaiting); err != nil {
		return nil, err
	}
	return dex.Until(originMainQueuedChannel.ForOne()), nil
}

func (originMainAwaitStep) Execute(dex.Context, dex.None) (*dex.StepDecision, error) {
	return dex.GoTo(originMainAwaitStep{}, nil), nil
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
	environment.startWorkerWithFlow(t, environment.flow, true)
}

func (environment *agentIntegrationEnvironment) startWorkerWithFlow(
	t *testing.T,
	registeredFlow dex.Flow,
	isCurrentAgent bool,
) {
	t.Helper()
	registry, err := dex.NewRegistry([]dex.Flow{registeredFlow})
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
	if isCurrentAgent {
		environment.agent = NewClient(sdkClient, environment.flow)
	} else {
		environment.agent = nil
	}
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

func waitForPendingMessageTotal(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	count int,
) {
	t.Helper()
	waitUntil(t, environment, "pending message total", func() (bool, error) {
		var queued []dex.ChannelMessage[UserMessage]
		if err := environment.sdk.GetChannelMessages(
			t.Context(),
			string(flowID),
			queuedUserMessagesChannel,
			&queued,
		); err != nil {
			return false, err
		}
		var steered []dex.ChannelMessage[UserMessage]
		if err := environment.sdk.GetChannelMessages(
			t.Context(),
			string(flowID),
			steeredUserMessagesChannel,
			&steered,
		); err != nil {
			return false, err
		}
		return len(queued)+len(steered) == count, nil
	})
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

type blockingIntegrationModel struct {
	started       chan struct{}
	releaseSignal chan struct{}
	startOnce     sync.Once
	releaseOnce   sync.Once
}

var _ ModelClient = (*blockingIntegrationModel)(nil)

func newBlockingIntegrationModel() *blockingIntegrationModel {
	return &blockingIntegrationModel{
		started:       make(chan struct{}),
		releaseSignal: make(chan struct{}),
	}
}

func (model *blockingIntegrationModel) Complete(ctx context.Context, request ModelRequest) (ModelReply, error) {
	if integrationLastUserContent(request.Messages) != "/block" {
		return (integrationModel{}).Complete(ctx, request)
	}
	model.startOnce.Do(func() { close(model.started) })
	select {
	case <-model.releaseSignal:
	case <-ctx.Done():
		return ModelReply{}, ctx.Err()
	}
	content := "integration block released"
	if err := request.WriteAssistant(content); err != nil {
		return ModelReply{}, err
	}
	return ModelReply{Content: content, ToolCalls: []ToolCall{}}, nil
}

func (*blockingIntegrationModel) Summarize(ctx context.Context, request SummarizeRequest) (string, error) {
	return (integrationModel{}).Summarize(ctx, request)
}

func (*blockingIntegrationModel) CountTokens(model Model, messages []AgentMessage) int {
	return (integrationModel{}).CountTokens(model, messages)
}

func (model *blockingIntegrationModel) waitUntilBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-model.started:
	case <-time.After(integrationWaitTimeout):
		t.Fatal("blocking model did not start")
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

func (model *blockingIntegrationModel) release() {
	model.releaseOnce.Do(func() { close(model.releaseSignal) })
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

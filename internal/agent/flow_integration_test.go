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
	"slices"
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

func TestAgentInactivityExpirationIntegration(t *testing.T) {
	t.Run("disabled timeout has no deadline", func(t *testing.T) {
		environment := newAgentIntegrationEnvironment(
			t, integrationModel{}, newIntegrationToolRegistry(),
		)
		flowID := FlowID("agent-inactivity-disabled-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		snapshot := readSnapshot(t, environment, flowID)
		if snapshot.Description.InactivityDeadline != nil {
			t.Fatalf("disabled Snapshot = %#v", snapshot.Description)
		}
	})

	t.Run("accepted user operation advances the global deadline", func(t *testing.T) {
		handler := newIntegrationExpirationHandler(0)
		environment := newAgentIntegrationEnvironment(
			t,
			integrationModel{},
			newIntegrationToolRegistry(),
			WithInactivityExpirationHandler(handler),
		)
		flowID := FlowID("agent-inactivity-reset-" + randomLocalID(t))
		config := NewAgentConfig()
		config.InactivityTimeoutSeconds = 5
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: config}); err != nil {
			t.Fatal(err)
		}
		initial := readSnapshot(t, environment, flowID)
		if initial.Description.InactivityDeadline == nil {
			t.Fatalf("initial Snapshot = %#v", initial.Description)
		}
		delay := time.NewTimer(200 * time.Millisecond)
		defer delay.Stop()
		select {
		case <-delay.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "reset deadline"}); err != nil {
			t.Fatal(err)
		}
		advanced := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
			return snapshot.Description.InactivityDeadline != nil &&
				snapshot.Description.InactivityDeadline.After(*initial.Description.InactivityDeadline)
		})
		if advanced.Description.InactivityDeadline == nil {
			t.Fatalf("advanced Snapshot = %#v", advanced.Description)
		}
	})

	t.Run("message wait expires once and survives Worker replacement", func(t *testing.T) {
		handler := newIntegrationExpirationHandler(0)
		environment := newAgentIntegrationEnvironment(
			t,
			integrationModel{},
			newIntegrationToolRegistry(),
			WithInactivityExpirationHandler(handler),
		)
		flowID := FlowID("agent-inactivity-message-" + randomLocalID(t))
		config := NewAgentConfig()
		config.InactivityTimeoutSeconds = 2
		metadata := MustJSONObject(`{"session_id":"session-1"}`)
		runID, err := environment.agent.Start(t.Context(), flowID, StartRequest{
			Config: config, RuntimeMetadata: metadata,
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshot := readSnapshot(t, environment, flowID)
		if snapshot.Description.InactivityDeadline == nil ||
			snapshot.Description.Status != AgentStatusWaitingForMessage {
			t.Fatalf("armed Snapshot = %#v", snapshot.Description)
		}
		environment.replaceWorker(t, flowID)
		expiration := handler.waitForSuccess(t)
		if expiration.FlowID != flowID || expiration.RunID != runID ||
			expiration.RuntimeMetadata != metadata ||
			!expiration.Deadline.Equal(*snapshot.Description.InactivityDeadline) {
			t.Fatalf("expiration = %#v, Snapshot = %#v", expiration, snapshot.Description)
		}
		assertInactivityCompleted(t, environment, flowID)
		handler.assertAttempts(t, 1)
		assertInactivityEventCount(t, environment.agent, flowID, 1)
	})

	t.Run("question approval and recovery waits expire", func(t *testing.T) {
		tests := []struct {
			name  string
			tools ToolRegistry
			start func(*testing.T, *agentIntegrationEnvironment, FlowID)
		}{
			{
				name:  "question",
				tools: newIntegrationToolRegistry(),
				start: func(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
					if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/questions"}); err != nil {
						t.Fatal(err)
					}
					waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
						return snapshot.Description.PendingUserInput != nil && snapshot.Description.InactivityDeadline != nil
					})
				},
			},
			{
				name:  "approval",
				tools: newIntegrationToolRegistry(),
				start: func(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
					if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
						t.Fatal(err)
					}
					waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
						return snapshot.Description.PendingApproval != nil && snapshot.Description.InactivityDeadline != nil
					})
				},
			},
			{
				name: "manual recovery",
				tools: func() ToolRegistry {
					registry := newRetryToolRegistryForTestOnly(0, false)
					registry.returnedUnknown = true
					registry.retryExhaustionPolicy = ToolRetryExhaustionPolicyManualRecovery
					return registry
				}(),
				start: func(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
					if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
						t.Fatal(err)
					}
					waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
						return snapshot.Description.PendingToolRecovery != nil && snapshot.Description.InactivityDeadline != nil
					})
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				handler := newIntegrationExpirationHandler(0)
				environment := newAgentIntegrationEnvironment(
					t, integrationModel{}, test.tools, WithInactivityExpirationHandler(handler),
				)
				flowID := FlowID("agent-inactivity-" + strings.ReplaceAll(test.name, " ", "-") + "-" + randomLocalID(t))
				config := NewAgentConfig()
				config.InactivityTimeoutSeconds = 1
				if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: config}); err != nil {
					t.Fatal(err)
				}
				test.start(t, environment, flowID)
				_ = handler.waitForSuccess(t)
				assertInactivityCompleted(t, environment, flowID)
				handler.assertAttempts(t, 1)
			})
		}
	})

	t.Run("durable wait extends the global deadline", func(t *testing.T) {
		handler := newIntegrationExpirationHandler(0)
		environment := newAgentIntegrationEnvironment(
			t, integrationModel{}, newIntegrationToolRegistry(), WithInactivityExpirationHandler(handler),
		)
		flowID := FlowID("agent-inactivity-durable-wait-" + randomLocalID(t))
		config := NewAgentConfig()
		config.InactivityTimeoutSeconds = 3
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: config}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/wait"}); err != nil {
			t.Fatal(err)
		}
		waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
			return snapshot.Description.Status == AgentStatusWaitingForTimer &&
				snapshot.Description.PendingTimer != nil && snapshot.Description.InactivityDeadline != nil
		})
		observation := time.NewTimer(1500 * time.Millisecond)
		defer observation.Stop()
		select {
		case expiration := <-handler.successes:
			t.Fatalf("expired during durable wait: %#v", expiration)
		case <-observation.C:
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "resume after durable wait"}); err != nil {
			t.Fatal(err)
		}
		queued := waitForQueuedMessages(t, environment, flowID, 1)
		if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{MessageID: queued[0].Value.MessageID}); err != nil {
			t.Fatal(err)
		}
		rearmed := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
			return snapshot.Description.Status == AgentStatusWaitingForMessage &&
				snapshot.Description.InactivityDeadline != nil
		})
		if rearmed.Description.InactivityDeadline == nil || time.Until(*rearmed.Description.InactivityDeadline) < 20*time.Second {
			t.Fatalf("rearmed Snapshot = %#v", rearmed.Description)
		}
		handler.assertAttempts(t, 0)
	})

	t.Run("callback retries with one stable expiration", func(t *testing.T) {
		handler := newIntegrationExpirationHandler(2)
		environment := newAgentIntegrationEnvironment(
			t, integrationModel{}, newIntegrationToolRegistry(), WithInactivityExpirationHandler(handler),
		)
		flowID := FlowID("agent-inactivity-retry-" + randomLocalID(t))
		config := NewAgentConfig()
		config.InactivityTimeoutSeconds = 1
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: config}); err != nil {
			t.Fatal(err)
		}
		want := handler.waitForSuccess(t)
		assertInactivityCompleted(t, environment, flowID)
		handler.assertStableAttempts(t, 3, want)
		assertInactivityEventCount(t, environment.agent, flowID, 1)
	})
}

type integrationExpirationHandler struct {
	mutex             sync.Mutex
	failuresRemaining int
	attempts          []InactivityExpiration
	successes         chan InactivityExpiration
}

func newIntegrationExpirationHandler(failures int) *integrationExpirationHandler {
	return &integrationExpirationHandler{
		failuresRemaining: failures,
		successes:         make(chan InactivityExpiration, 1),
	}
}

func (handler *integrationExpirationHandler) HandleInactivityExpiration(
	_ context.Context,
	expiration InactivityExpiration,
) error {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	handler.attempts = append(handler.attempts, expiration)
	if handler.failuresRemaining > 0 {
		handler.failuresRemaining--
		return errors.New("temporary expiration callback failure")
	}
	select {
	case handler.successes <- expiration:
	default:
	}
	return nil
}

func (handler *integrationExpirationHandler) waitForSuccess(t *testing.T) InactivityExpiration {
	t.Helper()
	select {
	case expiration := <-handler.successes:
		return expiration
	case <-time.After(integrationWaitTimeout):
		t.Fatal("timed out waiting for inactivity expiration")
		return InactivityExpiration{}
	}
}

func (handler *integrationExpirationHandler) assertAttempts(t *testing.T, want int) {
	t.Helper()
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	if len(handler.attempts) != want {
		t.Fatalf("expiration attempts = %d, want %d: %#v", len(handler.attempts), want, handler.attempts)
	}
}

func (handler *integrationExpirationHandler) assertStableAttempts(
	t *testing.T,
	wantCount int,
	want InactivityExpiration,
) {
	t.Helper()
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	if len(handler.attempts) != wantCount {
		t.Fatalf("expiration attempts = %d, want %d: %#v", len(handler.attempts), wantCount, handler.attempts)
	}
	for _, attempt := range handler.attempts {
		if attempt != want {
			t.Fatalf("expiration attempt changed: got %#v, want %#v", attempt, want)
		}
	}
}

func assertInactivityCompleted(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationWaitTimeout)
	defer cancel()
	result, err := environment.sdk.WaitForFlow(ctx, string(flowID), dex.WaitForFlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != dex.FlowCompleted {
		t.Fatalf("Flow status = %v, want completed", result.Status)
	}
	snapshot := readSnapshot(t, environment, flowID)
	if snapshot.Description.Status != AgentStatusExpiring || snapshot.Description.InactivityDeadline != nil {
		t.Fatalf("expired Snapshot = %#v", snapshot.Description)
	}
}

func assertInactivityEventCount(t *testing.T, client *Client, flowID FlowID, want int) {
	t.Helper()
	events, err := client.ListRecentEvents(t.Context(), flowID, EventStreamActivity, MaximumRecentEventLimit)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Activity.Kind == EventKindInactivityExpired {
			count++
		}
	}
	if count != want {
		t.Fatalf("inactivity event count = %d, want %d: %#v", count, want, events)
	}
}

func TestAgentToolRetryIntegration(t *testing.T) {
	t.Run("transient failure succeeds through Dex retry", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(2, false)
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-retry-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		waitForCompletedToolTurnForTestOnly(t, environment, flowID, 4)
		tools.assertAttemptsForTestOnly(t, []int32{1, 2, 3}, ToolOutcomeSucceeded)
		assertRecentToolAttemptForTestOnly(t, environment.agent, flowID, 3)
		for _, sequenced := range readSnapshot(t, environment, flowID).History.Messages {
			message := sequenced.Message
			if message.Role == MessageRoleTool && (message.StartedAt == nil || message.CreatedAt.Before(*message.StartedAt)) {
				t.Fatalf("retried tool timing = %#v", message)
			}
		}
	})

	t.Run("known failure is not retried", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(0, true)
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-known-failure-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		waitForCompletedToolTurnForTestOnly(t, environment, flowID, 4)
		tools.assertAttemptsForTestOnly(t, []int32{1}, ToolOutcomeKnownFailure)
	})

	t.Run("returned unknown is not retried and waits for recovery", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(0, false)
		tools.returnedUnknown = true
		tools.retryExhaustionPolicy = ToolRetryExhaustionPolicyManualRecovery
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-returned-unknown-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		pending := waitForPendingToolRecoveryForTestOnly(t, environment, flowID, "")
		resolveSingleToolRecoveryForTestOnly(
			t,
			environment,
			flowID,
			pending,
			ToolRecoveryActionContinueWithUnknown,
		)
		waitForCompletedToolTurnForTestOnly(t, environment, flowID, 4)
		tools.assertAttemptsForTestOnly(t, []int32{1}, ToolOutcomeUnknown)
	})

	t.Run("retry exhaustion records unknown and continues", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(10, false)
		tools.maximumAttempts = 2
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-exhaustion-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		waitForCompletedToolTurnForTestOnly(t, environment, flowID, 4)
		tools.assertAttemptsForTestOnly(t, []int32{1, 2}, ToolOutcomeUnknown)
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "still alive"}); err != nil {
			t.Fatal(err)
		}
		waitForAgentState(t, environment, flowID, func(state AgentState) bool {
			return state.Status == AgentStatusWaitingForMessage && state.LastSequence >= 6
		})
	})

	t.Run("retry exhaustion waits for manual recovery by default", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(10, false)
		tools.maximumAttempts = 2
		tools.retryExhaustionPolicy = ToolRetryExhaustionPolicyManualRecovery
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-manual-recovery-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		snapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
			return snapshot.Description.Status == AgentStatusWaitingForToolRecovery &&
				snapshot.Description.PendingToolRecovery != nil
		})
		pending := snapshot.Description.PendingToolRecovery
		if pending == nil || len(pending.Calls) != 1 {
			t.Fatalf("pending recovery = %#v", pending)
		}
		for _, message := range snapshot.History.Messages {
			if message.Message.Role == MessageRoleTool {
				t.Fatalf("tool result committed before recovery: %#v", message)
			}
		}
		if err := environment.agent.ResolveToolRecovery(t.Context(), flowID, ResolveToolRecoveryRequest{
			RecoveryID: pending.RecoveryID,
			Resolution: ToolRecoveryResolutionResume,
			Decisions: []ToolRecoveryDecision{{
				CallID: pending.Calls[0].CallID,
				Action: ToolRecoveryActionContinueWithUnknown,
			}},
		}); err != nil {
			t.Fatal(err)
		}
		waitForCompletedToolTurnForTestOnly(t, environment, flowID, 4)
		tools.assertAttemptsForTestOnly(t, []int32{1, 2}, ToolOutcomeUnknown)
	})

	t.Run("manual retry survives replacement and starts a fresh retry execution", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(10, false)
		tools.maximumAttempts = 2
		tools.retryExhaustionPolicy = ToolRetryExhaustionPolicyManualRecovery
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-manual-retry-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		first := waitForPendingToolRecoveryForTestOnly(t, environment, flowID, "")
		environment.replaceWorker(t, flowID)
		resolveSingleToolRecoveryForTestOnly(t, environment, flowID, first, ToolRecoveryActionRetry)

		second := waitForPendingToolRecoveryForTestOnly(t, environment, flowID, first.RecoveryID)
		if second.Calls[0].CallID != first.Calls[0].CallID {
			t.Fatalf("retry call ID = %q, want %q", second.Calls[0].CallID, first.Calls[0].CallID)
		}
		environment.replaceWorker(t, flowID)
		tools.setFailuresRemainingForTestOnly(0)
		resolveSingleToolRecoveryForTestOnly(t, environment, flowID, second, ToolRecoveryActionRetry)
		waitForCompletedToolTurnForTestOnly(t, environment, flowID, 4)
		tools.assertManualRetryAttemptsForTestOnly(t, []int32{1, 2, 1, 2, 1})
	})

	t.Run("manual stop records unknown without another model call", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(10, false)
		tools.maximumAttempts = 1
		tools.retryExhaustionPolicy = ToolRetryExhaustionPolicyManualRecovery
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-manual-stop-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		pending := waitForPendingToolRecoveryForTestOnly(t, environment, flowID, "")
		if err := environment.agent.ResolveToolRecovery(t.Context(), flowID, ResolveToolRecoveryRequest{
			RecoveryID: pending.RecoveryID,
			Resolution: ToolRecoveryResolutionStop,
			Decisions:  []ToolRecoveryDecision{},
		}); err != nil {
			t.Fatal(err)
		}
		state := waitForAgentState(t, environment, flowID, func(state AgentState) bool {
			return state.Status == AgentStatusWaitingForMessage && len(state.PendingToolCalls) == 0
		})
		if state.LastSequence != 3 {
			t.Fatalf("last sequence after stop = %d, want 3", state.LastSequence)
		}
		tools.assertAttemptsForTestOnly(t, []int32{1}, ToolOutcomeUnknown)
	})

	t.Run("steering abandons manual recovery and replans", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(10, false)
		tools.maximumAttempts = 1
		tools.retryExhaustionPolicy = ToolRetryExhaustionPolicyManualRecovery
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-recovery-steering-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		_ = waitForPendingToolRecoveryForTestOnly(t, environment, flowID, "")
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "replan after recovery"}); err != nil {
			t.Fatal(err)
		}
		queued := waitForQueuedMessages(t, environment, flowID, 1)
		if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
			MessageID: queued[0].Value.MessageID,
		}); err != nil {
			t.Fatal(err)
		}
		snapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
			return snapshot.Description.Status == AgentStatusWaitingForMessage &&
				snapshot.Description.PendingToolRecovery == nil &&
				historyHasMessage(
					snapshot.History.Messages,
					MessageRoleAssistant,
					"integration response: replan after recovery",
				)
		})
		if historyToolCountForTestOnly(snapshot) != 1 {
			t.Fatalf("tool result count after steering = %d, want 1", historyToolCountForTestOnly(snapshot))
		}
		tools.assertAttemptsForTestOnly(t, []int32{1}, ToolOutcomeUnknown)
	})

	t.Run("recovery resolution and steering commit exclusively", func(t *testing.T) {
		tools := newRetryToolRegistryForTestOnly(10, false)
		tools.maximumAttempts = 1
		tools.retryExhaustionPolicy = ToolRetryExhaustionPolicyManualRecovery
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, tools)
		flowID := FlowID("agent-tool-recovery-race-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		pending := waitForPendingToolRecoveryForTestOnly(t, environment, flowID, "")
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "race replacement"}); err != nil {
			t.Fatal(err)
		}
		queued := waitForQueuedMessages(t, environment, flowID, 1)
		errorsByCommand := make(chan error, 2)
		go func() {
			errorsByCommand <- environment.agent.ResolveToolRecovery(t.Context(), flowID, ResolveToolRecoveryRequest{
				RecoveryID: pending.RecoveryID,
				Resolution: ToolRecoveryResolutionResume,
				Decisions: []ToolRecoveryDecision{{
					CallID: pending.Calls[0].CallID,
					Action: ToolRecoveryActionContinueWithUnknown,
				}},
			})
		}()
		go func() {
			errorsByCommand <- environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
				MessageID: queued[0].Value.MessageID,
			})
		}()
		accepted := 0
		for range 2 {
			if err := <-errorsByCommand; err == nil {
				accepted++
			}
		}
		if accepted != 1 {
			t.Fatalf("accepted concurrent recovery commands = %d, want 1", accepted)
		}
		waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
			return snapshot.Description.Status == AgentStatusWaitingForMessage &&
				snapshot.Description.PendingToolRecovery == nil
		})
	})
}

func TestAgentParallelToolsIntegration(t *testing.T) {
	tools := newParallelToolRegistryForTestOnly()
	environment := newAgentIntegrationEnvironment(t, parallelIntegrationModel{integrationModel{}}, tools)
	flowID := FlowID("agent-parallel-tools-" + randomLocalID(t))
	config := NewAgentConfig()
	config.MaxParallelToolCalls = 2
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: config}); err != nil {
		t.Fatal(err)
	}
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/parallel"}); err != nil {
		t.Fatal(err)
	}
	waitForCompletedToolTurnForTestOnly(t, environment, flowID, 5)
	if tools.maximumActive.Load() < 2 {
		t.Fatalf("maximum concurrent tools = %d, want at least 2", tools.maximumActive.Load())
	}
	snapshot := readSnapshot(t, environment, flowID)
	toolNames := []ToolName{}
	for _, message := range snapshot.History.Messages {
		if message.Message.Role == MessageRoleTool && message.Message.ToolName != nil {
			toolNames = append(toolNames, *message.Message.ToolName)
			if message.Message.StartedAt == nil || message.Message.CreatedAt.Before(*message.Message.StartedAt) {
				t.Fatalf("parallel tool timing = %#v", message.Message)
			}
		}
	}
	if !slices.Equal(toolNames, []ToolName{"parallel_read_a", "parallel_read_b"}) {
		t.Fatalf("tool history order = %q", toolNames)
	}
}

func TestAgentParallelToolRecoveryIntegration(t *testing.T) {
	tools := mixedParallelToolRegistryForTestOnly{}
	environment := newAgentIntegrationEnvironment(t, mixedParallelIntegrationModel{integrationModel{}}, tools)
	flowID := FlowID("agent-parallel-recovery-" + randomLocalID(t))
	config := NewAgentConfig()
	config.MaxParallelToolCalls = 4
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: config}); err != nil {
		t.Fatal(err)
	}
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/parallel-recovery"}); err != nil {
		t.Fatal(err)
	}
	pending := waitForPendingToolRecoveryForTestOnly(t, environment, flowID, "")
	if pending.Calls[0].ToolName != "parallel_manual_unknown" {
		t.Fatalf("manual recovery calls = %#v", pending.Calls)
	}
	if snapshot := readSnapshot(t, environment, flowID); historyToolCountForTestOnly(snapshot) != 0 {
		t.Fatalf("parallel batch committed before recovery: %#v", snapshot.History.Messages)
	}
	resolveSingleToolRecoveryForTestOnly(
		t,
		environment,
		flowID,
		pending,
		ToolRecoveryActionContinueWithUnknown,
	)
	waitForCompletedToolTurnForTestOnly(t, environment, flowID, 7)
	snapshot := readSnapshot(t, environment, flowID)
	toolNames := []ToolName{}
	for _, message := range snapshot.History.Messages {
		if message.Message.Role == MessageRoleTool && message.Message.ToolName != nil {
			toolNames = append(toolNames, *message.Message.ToolName)
		}
	}
	want := []ToolName{
		"parallel_success",
		"parallel_known_failure",
		"parallel_automatic_unknown",
		"parallel_manual_unknown",
	}
	if !slices.Equal(toolNames, want) {
		t.Fatalf("tool history order = %q, want %q", toolNames, want)
	}
}

func TestAgentRejectsInvalidWriteTodosIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-invalid-write-todos-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content: "/invalid-write-todos", PlanMode: true,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
			historyContainsText(snapshot.History.Messages, string(toolErrorInvalidPlan)) &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration tool result acknowledged")
	})
	if snapshot.Description.Plan != nil {
		t.Fatalf("invalid write_todos created plan %#v", snapshot.Description.Plan)
	}
	if !historyContainsText(snapshot.History.Messages, `"status":"failed"`) {
		t.Fatalf("invalid write_todos known failure is missing: %#v", snapshot.History.Messages)
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
	if initialSnapshot.Description.Status != AgentStatusWaitingForMessage ||
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
	if latestSnapshot.History.Messages[0].Message.StartedAt != nil ||
		latestSnapshot.History.Messages[1].Message.StartedAt == nil ||
		latestSnapshot.History.Messages[1].Message.CreatedAt.Before(*latestSnapshot.History.Messages[1].Message.StartedAt) {
		t.Fatalf("initial message timing = %#v", latestSnapshot.History.Messages)
	}
	assertTextStream(t, environment.agent, flowID, EventStreamAssistant, "integration response: hello")
	assertTextStream(t, environment.agent, flowID, EventStreamReasoning, "deterministic integration summary")
	assertModelActivity(t, environment.agent, flowID, state.LastSequence)
	consumed := readActivityUntil(t, environment.agent, flowID, func(event AgentEvent) bool {
		return event.Kind == EventKindInputConsumed && event.InputConsumption != nil &&
			len(event.InputConsumption.QueuedMessageIDs) == 1
	})
	if consumed.Activity.Message != "Consumed 1 queued user message." ||
		strings.Contains(consumed.Activity.Message, "hello") {
		t.Fatalf("queued input consumption Activity = %#v", consumed.Activity)
	}
	assertRecentTextEvents(t, environment.agent, flowID, EventStreamAssistant, "integration response: hello")
	assertRecentTextEvents(t, environment.agent, flowID, EventStreamReasoning, "deterministic integration summary")
	assertRecentActivityEvents(t, environment.agent, flowID)

	environment.replaceWorker(t, flowID)
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
		t.Fatal(err)
	}
	snapshotRequired := readActivityUntil(t, environment.agent, flowID, func(event AgentEvent) bool {
		return event.Kind == EventKindSnapshotRequired
	})
	approvalSnapshot := readSnapshot(t, environment, flowID)
	if approvalSnapshot.Description.PendingApproval == nil {
		t.Fatalf("Snapshot after approval control = %#v", approvalSnapshot)
	}
	approval := *approvalSnapshot.Description.PendingApproval
	if approval.ToolName != integrationToolName {
		t.Fatalf("pending tool = %q", approval.ToolName)
	}
	if approval.StartedAt == nil {
		t.Fatal("pending approval started at is nil")
	}
	assertRecentActivityKindsForTestOnly(t, environment.agent, flowID, EventKindToolApprovalRequested)
	if snapshotRequired.Activity.Message != "Durable interaction state changed." ||
		snapshotRequired.Activity.InputConsumption != nil {
		t.Fatalf("Snapshot control Activity = %#v", snapshotRequired.Activity)
	}
	environment.replaceWorker(t, flowID)
	recoveredApproval := waitForPendingApproval(t, environment, flowID)
	if recoveredApproval.CallID != approval.CallID || recoveredApproval.Arguments != approval.Arguments ||
		recoveredApproval.StartedAt == nil || !recoveredApproval.StartedAt.Equal(*approval.StartedAt) {
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
	assertRecentActivityKindsForTestOnly(t, environment.agent, flowID, EventKindToolApprovalResolved)
	approvedSnapshot := readSnapshot(t, environment, flowID)
	approvedToolMessage, found := findToolResultMessage(approvedSnapshot.History.Messages, approval.CallID)
	if !found || approvedToolMessage.StartedAt == nil || approvedToolMessage.CreatedAt.Before(*approvedToolMessage.StartedAt) {
		t.Fatalf("approved tool timing = %#v", approvedToolMessage)
	}

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
	if pendingInput.StartedAt == nil {
		t.Fatal("pending user input started at is nil")
	}
	assertRecentActivityKindsForTestOnly(t, environment.agent, flowID, EventKindUserInputRequested)
	environment.replaceWorker(t, flowID)
	if err := environment.agent.AnswerQuestions(t.Context(), flowID, answerRequest(pendingInput, "us-west")); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingUserInput(t, environment, flowID)
	assertRecentActivityKindsForTestOnly(t, environment.agent, flowID, EventKindUserInputAnswered)

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/wait"}); err != nil {
		t.Fatal(err)
	}
	waitForPendingTimer(t, environment, flowID)
	if timer := readSnapshot(t, environment, flowID).Description.PendingTimer; timer == nil || timer.StartedAt == nil {
		t.Fatalf("pending timer timing = %#v", timer)
	}
	assertRecentActivityKindsForTestOnly(t, environment.agent, flowID, EventKindTimerStarted)
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
		if firstQueueSnapshot.Queued[index].MessageID != queued[index].Value.MessageID {
			t.Fatalf("Snapshot message ID = %q, want application ID %q", firstQueueSnapshot.Queued[index].MessageID, queued[index].Value.MessageID)
		}
	}
	if err := environment.agent.DeleteQueuedMessage(t.Context(), flowID, queued[1].Value.MessageID); err != nil {
		t.Fatal(err)
	}
	waitForQueuedMessages(t, environment, flowID, 1)
	deleteErr := environment.agent.DeleteQueuedMessage(t.Context(), flowID, queued[1].Value.MessageID)
	var deletedMessageNotFound *PendingMessageNotFoundError
	if !errors.As(deleteErr, &deletedMessageNotFound) {
		t.Fatalf("repeated queue delete error = %T %v", deleteErr, deleteErr)
	}
	if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
		MessageID: queued[0].Value.MessageID,
	}); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingTimer(t, environment, flowID)
	assertRecentActivityKindsForTestOnly(t, environment.agent, flowID, EventKindTimerResolved)
	waitForQueuedMessages(t, environment, flowID, 0)
	state = waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && state.LastSequence > stateBeforeQueue.LastSequence
	})
	if !historyContains(t, environment, flowID, state, MessageRoleUser, "queued message") {
		t.Fatal("steered message did not enter application history")
	}
	steerErr := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
		MessageID: queued[0].Value.MessageID,
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
	if state.FirstRetainedSequence > 1 {
		_, archiveErr := environment.agent.GetArchivedMessages(
			t.Context(),
			flowID,
			state.FirstRetainedSequence,
		)
		var notFound *ArchivedMessagesNotFoundError
		if !errors.As(archiveErr, &notFound) {
			t.Fatalf("trimmed archive error = %T %v", archiveErr, archiveErr)
		}
		_ = readSnapshot(t, environment, flowID)
	}
}

func TestAgentTimerSnapshotNotificationIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-timer-snapshot-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/wait"}); err != nil {
		t.Fatal(err)
	}
	readActivityUntil(t, environment.agent, flowID, func(event AgentEvent) bool {
		return event.Kind == EventKindSnapshotRequired
	})
	snapshot := readSnapshot(t, environment, flowID)
	if snapshot.Description.PendingTimer == nil {
		t.Fatalf("Snapshot after timer control = %#v", snapshot)
	}
}

func TestAgentWaitingInputRoundIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-interaction-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	initial := readSnapshot(t, environment, flowID)
	if initial.Description.WaitingInputRound != 1 {
		t.Fatalf("initial waiting input round = %#v, want 1", initial.Description)
	}

	type roundResult struct {
		round WaitingInputRound
		err   error
	}
	advanced := make(chan roundResult, 1)
	go func() {
		round, err := environment.agent.WaitForWaitingInputRound(t.Context(), flowID, 1)
		advanced <- roundResult{round: round, err: err}
	}()
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "round cycle"}); err != nil {
		t.Fatal(err)
	}
	first := <-advanced
	if first.err != nil || first.round <= 1 {
		t.Fatalf("first round result = %#v", first)
	}
	snapshot := readSnapshot(t, environment, flowID)
	if snapshot.Description.WaitingInputRound != first.round ||
		!historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: round cycle") {
		t.Fatalf("reconciled Snapshot = %#v", snapshot)
	}

	environment.replaceWorker(t, flowID)
	advanced = make(chan roundResult, 1)
	go func() {
		round, err := environment.agent.WaitForWaitingInputRound(t.Context(), flowID, first.round)
		advanced <- roundResult{round: round, err: err}
	}()
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "replacement cycle"}); err != nil {
		t.Fatal(err)
	}
	second := <-advanced
	if second.err != nil || second.round <= first.round {
		t.Fatalf("replacement round result = %#v", second)
	}
}

func TestAgentConsumesSteeringAtApprovalAndModelBoundariesIntegration(t *testing.T) {
	t.Run("approval", func(t *testing.T) {
		environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
		flowID := FlowID("agent-approval-consumption-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
			t.Fatal(err)
		}
		waitForPendingApproval(t, environment, flowID)
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "private approval steering"}); err != nil {
			t.Fatal(err)
		}
		queued := waitForQueuedMessages(t, environment, flowID, 1)
		messageID := queued[0].Value.MessageID
		if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{MessageID: messageID}); err != nil {
			t.Fatal(err)
		}
		assertSteeredInputConsumption(t, environment.agent, flowID, []MessageID{messageID}, []string{"private approval steering"})
	})

	t.Run("model", func(t *testing.T) {
		model := newBlockingFirstModel()
		environment := newAgentIntegrationEnvironment(t, model, newIntegrationToolRegistry())
		flowID := FlowID("agent-model-consumption-" + randomLocalID(t))
		if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
			t.Fatal(err)
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "start model"}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-model.started:
		case <-time.After(integrationWaitTimeout):
			t.Fatal("model did not start")
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "private model steering"}); err != nil {
			t.Fatal(err)
		}
		queued := waitForQueuedMessages(t, environment, flowID, 1)
		messageID := queued[0].Value.MessageID
		if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{MessageID: messageID}); err != nil {
			t.Fatal(err)
		}
		close(model.release)
		assertSteeredInputConsumption(t, environment.agent, flowID, []MessageID{messageID}, []string{"private model steering"})
	})
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

	first, err := environment.agent.GetArchivedMessages(t.Context(), flowID, 11)
	if err != nil {
		t.Fatal(err)
	}
	second, err := environment.agent.GetArchivedMessages(t.Context(), flowID, 21)
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
	aggregated, err := environment.agent.GetArchivedMessageRange(t.Context(), flowID, 21, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregated.Messages) != 20 || aggregated.Messages[0].Sequence != 1 ||
		aggregated.Messages[19].Sequence != 20 || aggregated.NextBeforeSequence != nil {
		t.Fatalf("aggregated archive = %#v", aggregated)
	}
	forward, err := environment.agent.GetMessagesAfterForTestOnly(t.Context(), flowID, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Messages) != 7 || forward.Messages[0].Sequence != 1 || forward.Messages[6].Sequence != 7 ||
		forward.NextAfterSequence == nil || *forward.NextAfterSequence != 7 || !forward.IsTruncated ||
		forward.FirstRetainedSequence != 1 || forward.LastSequence != 30 {
		t.Fatalf("first forward page = %#v", forward)
	}
	forward, err = environment.agent.GetMessagesAfterForTestOnly(
		t.Context(),
		flowID,
		*forward.NextAfterSequence,
		maximumForwardHistoryLimitForTestOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Messages) != 23 || forward.Messages[0].Sequence != 8 || forward.Messages[22].Sequence != 30 ||
		forward.NextAfterSequence != nil || forward.IsTruncated {
		t.Fatalf("second forward page = %#v", forward)
	}
	empty, err := environment.agent.GetMessagesAfterForTestOnly(t.Context(), flowID, 30, 0)
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
		return snapshot.Description.PendingUserInput != nil
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
	if closed.Description.PendingUserInput != nil {
		t.Fatalf("question remained after accepted answer: %#v", closed.Description)
	}
	answeredSnapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.PendingUserInput == nil &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: **Details**: September 12")
	})
	answered := false
	for _, entry := range answeredSnapshot.History.Messages {
		if entry.Message.Role == MessageRoleUser && entry.Message.AnsweredInputCallID != nil &&
			*entry.Message.AnsweredInputCallID == snapshot.Description.PendingUserInput.CallID {
			answered = true
			break
		}
	}
	if !answered {
		t.Fatalf("answered input call ID missing from durable history: %#v", answeredSnapshot.History.Messages)
	}
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
		return snapshot.Description.PendingUserInput != nil &&
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
		return snapshot.Description.PendingUserInput == nil &&
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
	if current := readSnapshot(t, environment, flowID); current.Description.PendingUserInput == nil || current.Description.PendingUserInput.CallID != multi.CallID {
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
		return snapshot.Description.PendingUserInput == nil &&
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

func TestAgentQuestionAnswerPriorityIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-answer-priority-" + randomLocalID(t))
	if _, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()}); err != nil {
		t.Fatal(err)
	}
	initial := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.Status == AgentStatusWaitingForMessage
	})
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/wait"}); err != nil {
		t.Fatal(err)
	}
	waitForPendingTimer(t, environment, flowID)
	for _, content := range []string{
		"/ask Which deployment target?",
		"steered after question",
		"ordinary queued request",
	} {
		if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	queued := waitForQueuedMessages(t, environment, flowID, 3)
	if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
		MessageID: queued[0].Value.MessageID,
	}); err != nil {
		t.Fatal(err)
	}
	pendingSnapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.PendingUserInput != nil &&
			snapshot.Description.WaitingInputRound > initial.Description.WaitingInputRound
	})
	if len(pendingSnapshot.Queued) != 2 {
		t.Fatalf("queue while awaiting answer = %#v", pendingSnapshot.Queued)
	}
	var steeringID MessageID
	for _, message := range pendingSnapshot.Queued {
		if message.Value.Content == "steered after question" {
			steeringID = message.MessageID
		}
	}
	if steeringID == "" {
		t.Fatalf("steering target missing from queue: %#v", pendingSnapshot.Queued)
	}
	if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{MessageID: steeringID}); err != nil {
		t.Fatal(err)
	}
	pendingSnapshot = waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.PendingUserInput != nil &&
			len(snapshot.Steered) == 1 && len(snapshot.Queued) == 1
	})
	environment.replaceWorker(t, flowID)
	if err := environment.agent.AnswerQuestions(
		t.Context(),
		flowID,
		answerRequest(*pendingSnapshot.Description.PendingUserInput, "priority answer"),
	); err != nil {
		t.Fatal(err)
	}
	completed := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
			len(snapshot.Queued) == 0 && len(snapshot.Steered) == 0 &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: **Details**: priority answer") &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: steered after question") &&
			historyHasMessage(snapshot.History.Messages, MessageRoleAssistant, "integration response: ordinary queued request")
	})
	answerSequence := Sequence(0)
	steeredSequence := Sequence(0)
	queuedSequence := Sequence(0)
	for _, item := range completed.History.Messages {
		if item.Message.Role != MessageRoleUser {
			continue
		}
		switch item.Message.Content {
		case "**Details**: priority answer":
			answerSequence = item.Sequence
		case "steered after question":
			steeredSequence = item.Sequence
		case "ordinary queued request":
			queuedSequence = item.Sequence
		}
	}
	if answerSequence == 0 || steeredSequence == 0 || queuedSequence == 0 ||
		answerSequence >= steeredSequence || steeredSequence >= queuedSequence {
		t.Fatalf(
			"user input order answer=%d steered=%d queued=%d; history=%#v",
			answerSequence,
			steeredSequence,
			queuedSequence,
			completed.History.Messages,
		)
	}
	answered := readActivityUntil(t, environment.agent, flowID, func(event AgentEvent) bool {
		return event.Kind == EventKindUserInputAnswered && event.CallID != nil &&
			*event.CallID == pendingSnapshot.Description.PendingUserInput.CallID
	})
	if answered.Activity.Message != "Answered 1 question." ||
		answered.Activity.MessageSequence == nil ||
		*answered.Activity.MessageSequence != answerSequence ||
		strings.Contains(answered.Activity.Message, "priority answer") {
		t.Fatalf("answered user input Activity = %#v", answered.Activity)
	}
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
		return snapshot.Description.Plan != nil &&
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
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan == nil
	})

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content: "/plan-stop demonstrate advisory completion", PlanMode: true,
	}); err != nil {
		t.Fatal(err)
	}
	draftSnapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
			snapshot.Description.Plan != nil && snapshot.Description.Plan.Status == PlanStatusDraft
	})
	if err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		Revision: draftSnapshot.Description.Plan.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	active := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
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
	advanced := make(chan error, 1)
	go func() {
		_, err := environment.agent.WaitForWaitingInputRound(
			t.Context(), flowID, active.Description.WaitingInputRound,
		)
		advanced <- err
	}()
	if err := environment.agent.ExecutePlan(t.Context(), flowID, PlanExecutionRequest{
		Revision: active.Description.Plan.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-advanced; err != nil {
		t.Fatal(err)
	}
	continued := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
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
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
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
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
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
	executions, err := environment.agent.GetPlanExecutionMessagesForTestOnly(
		t.Context(), flowID, draft.Revision,
	)
	if err != nil {
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
	consumed := readActivityUntil(t, environment.agent, flowID, func(event AgentEvent) bool {
		return event.Kind == EventKindInputConsumed && event.InputConsumption != nil &&
			event.InputConsumption.PlanExecutionRevision != nil &&
			*event.InputConsumption.PlanExecutionRevision == draft.Revision
	})
	if consumed.Activity.Message != fmt.Sprintf(
		"Consumed plan execution request for revision %d.", draft.Revision,
	) {
		t.Fatalf("Plan input consumption Activity = %#v", consumed.Activity)
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
	initial := readSnapshot(t, environment, flowID)
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
	queuedSnapshot := readSnapshot(t, environment, flowID)
	if queuedSnapshot.Description.WaitingInputRound != initial.Description.WaitingInputRound {
		t.Fatalf("queued input created a false waiting round: initial=%#v queued=%#v", initial.Description, queuedSnapshot.Description)
	}
	for _, message := range queued {
		if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{MessageID: message.Value.MessageID}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.Status == AgentStatusWaitingForMessage &&
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
	assertSteeredInputConsumption(
		t,
		environment.agent,
		flowID,
		[]MessageID{queued[0].Value.MessageID, queued[1].Value.MessageID},
		[]string{queued[0].Value.Value.Content, queued[1].Value.Value.Content},
	)
}

func TestAgentSnapshotRemainsReadableAfterTerminationIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-snapshot-after-termination-" + randomLocalID(t))
	runID, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()})
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.sdk.StopFlow(t.Context(), string(flowID), dex.StopOptions{
		Type:   dex.TerminateFlow,
		Reason: "Snapshot query after termination integration",
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
	if _, err := environment.agent.WaitForWaitingInputRound(t.Context(), flowID, 1); err == nil {
		t.Fatal("waiting input round remained active after termination")
	}
	snapshot := readSnapshot(t, environment, flowID)
	if snapshot.RunID != runID || snapshot.Description.Status != AgentStatusWaitingForMessage {
		t.Fatalf("Snapshot after termination = %#v", snapshot)
	}
	if len(snapshot.History.Messages) != 0 || len(snapshot.Queued) != 0 || len(snapshot.Steered) != 0 {
		t.Fatalf("Snapshot durable view after termination = %#v", snapshot)
	}
}

func TestAgentSnapshotAfterContinueAsNewIntegration(t *testing.T) {
	environment := newAgentIntegrationEnvironment(t, integrationModel{}, newIntegrationToolRegistry())
	flowID := FlowID("agent-continue-as-new-" + randomLocalID(t))
	firstRunID, err := environment.agent.Start(t.Context(), flowID, StartRequest{Config: NewAgentConfig()})
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage
	})
	if err := environment.sdk.TriggerContinueAsNew(t.Context(), string(flowID)); err != nil {
		t.Fatal(err)
	}
	var snapshot AgentSnapshot
	waitUntil(t, environment, "continued Agent run", func() (bool, error) {
		var snapshotErr error
		snapshot, snapshotErr = environment.agent.GetSnapshot(t.Context(), flowID)
		return snapshotErr == nil && snapshot.RunID != firstRunID, snapshotErr
	})
	if snapshot.RunID == firstRunID {
		t.Fatalf("Snapshot after continue-as-new = %#v", snapshot)
	}
	environment.replaceWorker(t, flowID)
	replaced := readSnapshot(t, environment, flowID)
	if replaced.RunID != snapshot.RunID {
		t.Fatalf("Snapshot after Worker replacement = %#v, want run %q", replaced, snapshot.RunID)
	}
}

func readSnapshot(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
) AgentSnapshot {
	t.Helper()
	snapshot, err := environment.agent.GetSnapshot(t.Context(), flowID)
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
		snapshot, err = environment.agent.GetSnapshot(t.Context(), flowID)
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

func registerRPCDefinitionsForTestOnly(flow *Flow) {
	flow.rpcDefinitionsForTestOnly = []dex.RPCDef{
		dex.DefineRPC(flow.GetFlowStateForTestOnly, &dex.RPCOptions{
			Timeout:      defaultCommandTimeout,
			LoadChannels: []dex.ChannelDef{queuedUserMessagesChannel},
		}),
		dex.DefineRPC(flow.GetPlanExecutionMessagesForTestOnly, &dex.RPCOptions{
			Timeout:         defaultCommandTimeout,
			LoadChannelMaps: []dex.ChannelDef{planExecutionsChannel},
		}),
		dex.DefineRPC(flow.GetMessagesAfterForTestOnly, &dex.RPCOptions{
			Timeout: defaultCommandTimeout,
			LoadAttributeMaps: []dex.AttributeDef{
				currentMessagesAttribute,
				archivedMessagesAttribute,
			},
		}),
	}
}

func newAgentIntegrationEnvironment(
	t *testing.T,
	modelClient ModelClient,
	tools ToolRegistry,
	options ...FlowOption,
) *agentIntegrationEnvironment {
	t.Helper()
	flow := NewFlow(modelClient, tools, options...)
	flow.minimumInactivityTimeoutSecondsForTestOnly = 1
	flow.inactivityResetMinimumExtensionForTestOnly = 100 * time.Millisecond
	registerRPCDefinitionsForTestOnly(flow)
	environment := &agentIntegrationEnvironment{
		flow:          flow,
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
		if _, err := environment.agent.GetSnapshot(t.Context(), flowID); err == nil {
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
		view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
		state = view.State
		return view.HasState && accept(state), err
	})
	return state
}

func readAgentState(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) AgentState {
	t.Helper()
	view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasState {
		t.Fatal("Agent state is missing")
	}
	return view.State
}

func waitForAgentPlan(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID, status PlanStatus) AgentPlan {
	t.Helper()
	var plan *AgentPlan
	waitUntil(t, environment, "Agent plan", func() (bool, error) {
		view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
		plan = view.Plan
		return plan != nil && plan.Status == status, err
	})
	return *plan
}

func waitForPendingApproval(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) PendingApproval {
	t.Helper()
	var approval *PendingApproval
	waitUntil(t, environment, "pending approval", func() (bool, error) {
		view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
		approval = view.PendingApproval
		return approval != nil, err
	})
	return *approval
}

func waitForPendingUserInput(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) PendingUserInput {
	t.Helper()
	var pending *PendingUserInput
	waitUntil(t, environment, "pending user input", func() (bool, error) {
		view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
		pending = view.PendingInput
		return pending != nil, err
	})
	return *pending
}

func waitForNoPendingUserInput(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
	t.Helper()
	waitUntil(t, environment, "cleared user input", func() (bool, error) {
		view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
		return view.PendingInput == nil, err
	})
}

func waitForPendingTimer(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
	t.Helper()
	waitUntil(t, environment, "pending timer", func() (bool, error) {
		view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
		return view.PendingTimer != nil, err
	})
}

func waitForNoPendingTimer(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
	t.Helper()
	waitUntil(t, environment, "cleared timer", func() (bool, error) {
		view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
		return view.PendingTimer == nil, err
	})
}

func waitForQueuedMessages(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	count int,
) []dex.ChannelMessage[PendingUserMessage] {
	t.Helper()
	var messages []dex.ChannelMessage[PendingUserMessage]
	waitUntil(t, environment, "queued messages", func() (bool, error) {
		view, err := environment.agent.GetFlowStateForTestOnly(t.Context(), flowID)
		messages = view.Queued
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

func findToolResultMessage(messages []SequencedMessage, callID CallID) (AgentMessage, bool) {
	for _, sequenced := range messages {
		message := sequenced.Message
		if message.Role == MessageRoleTool && message.ToolCallID != nil && *message.ToolCallID == callID {
			return message, true
		}
	}
	return AgentMessage{}, false
}

func readApplicationMessage(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	sequence Sequence,
) (AgentMessage, bool) {
	t.Helper()
	page, err := environment.agent.GetMessagesAfterForTestOnly(t.Context(), flowID, sequence-1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Sequence != sequence {
		return AgentMessage{}, false
	}
	return page.Messages[0].Message, true
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
	event := readActivityUntil(t, client, flowID, func(event AgentEvent) bool {
		return event.Kind == EventKindModelStarted
	})
	if event.Activity.Kind != EventKindModelStarted ||
		event.Activity.MessageSequence == nil ||
		*event.Activity.MessageSequence != expectedSequence {
		t.Fatalf("model activity = %#v", event)
	}
}

func assertRecentTextEvents(
	t *testing.T,
	client *Client,
	flowID FlowID,
	stream EventStream,
	expected string,
) {
	t.Helper()
	events, err := client.ListRecentEvents(t.Context(), flowID, stream, MaximumRecentEventLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].Text != expected {
		t.Fatalf("recent %s events = %#v", stream, events)
	}
	latest, err := client.ListRecentEvents(t.Context(), flowID, stream, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].ResumeToken != events[len(events)-1].ResumeToken {
		t.Fatalf("latest %s event = %#v, want tail of %#v", stream, latest, events)
	}
}

func assertRecentActivityEvents(t *testing.T, client *Client, flowID FlowID) {
	t.Helper()
	events, err := client.ListRecentEvents(t.Context(), flowID, EventStreamActivity, MaximumRecentEventLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("recent activity events = %#v", events)
	}
	for index := 1; index < len(events); index++ {
		if events[index].CreatedAt.Before(events[index-1].CreatedAt) {
			t.Fatalf("activity events are not chronological: %#v", events)
		}
	}
}

func assertRecentToolAttemptForTestOnly(t *testing.T, client *Client, flowID FlowID, expected int32) {
	t.Helper()
	events, err := client.ListRecentEvents(t.Context(), flowID, EventStreamActivity, MaximumRecentEventLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Activity.Kind == EventKindToolProgress && event.Activity.Attempt != nil && *event.Activity.Attempt == expected {
			return
		}
	}
	t.Fatalf("tool attempt %d is missing from recent activity: %#v", expected, events)
}

func assertRecentActivityKindsForTestOnly(t *testing.T, client *Client, flowID FlowID, expected ...EventKind) {
	t.Helper()
	events, err := client.ListRecentEvents(t.Context(), flowID, EventStreamActivity, MaximumRecentEventLimit)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[EventKind]bool, len(expected))
	for _, event := range events {
		found[event.Activity.Kind] = true
	}
	for _, kind := range expected {
		if !found[kind] {
			t.Fatalf("activity kind %q is missing from recent activity: %#v", kind, events)
		}
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

func assertSteeredInputConsumption(
	t *testing.T,
	client *Client,
	flowID FlowID,
	messageIDs []MessageID,
	secrets []string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationWaitTimeout)
	defer cancel()
	remaining := make(map[MessageID]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		remaining[messageID] = struct{}{}
	}
	resumeToken := ResumeToken("")
	for len(remaining) > 0 {
		event, err := client.ReadEvent(ctx, flowID, EventStreamActivity, resumeToken)
		if err != nil {
			t.Fatalf("read Activity Stream for %s: %v", flowID, err)
		}
		resumeToken = event.ResumeToken
		consumption := event.Activity.InputConsumption
		if event.Activity.Kind != EventKindInputConsumed || consumption == nil ||
			len(consumption.SteeredMessageIDs) == 0 {
			continue
		}
		if event.Activity.Message != consumedMessagesDescription(len(consumption.SteeredMessageIDs), "steered") {
			t.Fatalf("steered input consumption Activity = %#v", event.Activity)
		}
		for _, messageID := range consumption.SteeredMessageIDs {
			if _, expected := remaining[messageID]; !expected {
				t.Fatalf("unexpected steered message ID %q in %#v", messageID, event.Activity)
			}
			delete(remaining, messageID)
		}
		for _, secret := range secrets {
			if strings.Contains(event.Activity.Message, secret) {
				t.Fatalf("steered input consumption Activity leaked content: %#v", event.Activity)
			}
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
		if integrationLastUserContent(request.Messages) == "/invalid-write-todos" {
			return integrationToolReply(request, ToolNameWriteTodos, MustJSONObject(`{}`), "writing invalid plan")
		}
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

type blockingFirstModel struct {
	integrationModel
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

var _ ModelClient = (*blockingFirstModel)(nil)

func newBlockingFirstModel() *blockingFirstModel {
	return &blockingFirstModel{started: make(chan struct{}), release: make(chan struct{})}
}

func (model *blockingFirstModel) Complete(ctx context.Context, request ModelRequest) (ModelReply, error) {
	shouldBlock := false
	model.once.Do(func() {
		shouldBlock = true
		close(model.started)
	})
	if shouldBlock {
		select {
		case <-model.release:
		case <-ctx.Done():
			return ModelReply{}, ctx.Err()
		}
	}
	return model.integrationModel.Complete(ctx, request)
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
	encoded, err := json.Marshal(struct {
		Todos []PlanTask `json:"todos"`
	}{Todos: tasks})
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

type retryToolRegistryForTestOnly struct {
	mutex                 sync.Mutex
	failuresRemaining     int
	knownFailure          bool
	returnedUnknown       bool
	maximumAttempts       int
	retryExhaustionPolicy ToolRetryExhaustionPolicy
	invocations           []ToolInvocation
	returnedOutcomes      []ToolOutcome
}

type parallelIntegrationModel struct {
	integrationModel
}

func (parallelIntegrationModel) Complete(ctx context.Context, request ModelRequest) (ModelReply, error) {
	if integrationLastUserContent(request.Messages) != "/parallel" {
		return integrationModel{}.Complete(ctx, request)
	}
	if last := integrationLastConversationMessage(request.Messages); last != nil && last.Role == MessageRoleTool {
		return integrationModel{}.Complete(ctx, request)
	}
	if err := request.WriteAssistant("reading in parallel"); err != nil {
		return ModelReply{}, err
	}
	return ModelReply{
		Content: "reading in parallel",
		ToolCalls: []ToolCall{
			integrationToolCall(request, "parallel_read_a", MustJSONObject(`{}`)),
			integrationToolCall(request, "parallel_read_b", MustJSONObject(`{}`)),
		},
	}, nil
}

type mixedParallelIntegrationModel struct {
	integrationModel
}

func (mixedParallelIntegrationModel) Complete(ctx context.Context, request ModelRequest) (ModelReply, error) {
	if integrationLastUserContent(request.Messages) != "/parallel-recovery" {
		return integrationModel{}.Complete(ctx, request)
	}
	if last := integrationLastConversationMessage(request.Messages); last != nil && last.Role == MessageRoleTool {
		return integrationModel{}.Complete(ctx, request)
	}
	if err := request.WriteAssistant("reading mixed parallel results"); err != nil {
		return ModelReply{}, err
	}
	names := []ToolName{
		"parallel_success",
		"parallel_known_failure",
		"parallel_automatic_unknown",
		"parallel_manual_unknown",
	}
	calls := make([]ToolCall, 0, len(names))
	for _, name := range names {
		calls = append(calls, integrationToolCall(request, name, MustJSONObject(`{}`)))
	}
	return ModelReply{Content: "reading mixed parallel results", ToolCalls: calls}, nil
}

type mixedParallelToolRegistryForTestOnly struct{}

func (mixedParallelToolRegistryForTestOnly) ServerNames() []string { return []string{"parallel"} }

func (registry mixedParallelToolRegistryForTestOnly) RegisteredTools() []RegisteredTool {
	definitions := registry.Definitions(nil, nil)
	result := make([]RegisteredTool, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, RegisteredTool{
			ServerName: "parallel",
			RemoteName: string(definition.Name),
			Definition: definition,
		})
	}
	return result
}

func (mixedParallelToolRegistryForTestOnly) Definitions([]string, []ToolName) []ToolDefinition {
	names := []ToolName{
		"parallel_success",
		"parallel_known_failure",
		"parallel_automatic_unknown",
		"parallel_manual_unknown",
	}
	definitions := make([]ToolDefinition, 0, len(names))
	for _, name := range names {
		definition := parallelDefinitionForTestOnly(name)
		if name == "parallel_automatic_unknown" {
			definition.RetryExhaustionPolicy = ToolRetryExhaustionPolicyContinueWithUnknown
		}
		definitions = append(definitions, definition)
	}
	return definitions
}

func (mixedParallelToolRegistryForTestOnly) Execute(
	_ context.Context,
	invocation ToolInvocation,
) (ToolExecutionResult, error) {
	switch invocation.Name {
	case "parallel_success":
		return ToolExecutionResult{Content: `{"ok":true}`, Outcome: ToolOutcomeSucceeded}, nil
	case "parallel_known_failure":
		return ToolExecutionResult{
			Content: `{"ok":false}`,
			Outcome: ToolOutcomeKnownFailure,
			IsError: true,
		}, nil
	case "parallel_automatic_unknown":
		return ToolExecutionResult{
			Content: `{"outcome":"unknown"}`,
			Outcome: ToolOutcomeUnknown,
			IsError: true,
		}, nil
	case "parallel_manual_unknown":
		return ToolExecutionResult{}, errors.New("manual unknown fixture")
	default:
		return ToolExecutionResult{}, fmt.Errorf("unexpected mixed parallel tool %q", invocation.Name)
	}
}

type parallelToolRegistryForTestOnly struct {
	ready         chan struct{}
	readyOnce     sync.Once
	active        atomic.Int32
	maximumActive atomic.Int32
}

func newParallelToolRegistryForTestOnly() *parallelToolRegistryForTestOnly {
	return &parallelToolRegistryForTestOnly{ready: make(chan struct{})}
}

func (*parallelToolRegistryForTestOnly) ServerNames() []string { return []string{"parallel"} }

func (registry *parallelToolRegistryForTestOnly) RegisteredTools() []RegisteredTool {
	definitions := registry.Definitions(nil, nil)
	return []RegisteredTool{
		{ServerName: "parallel", RemoteName: "read_a", Definition: definitions[0]},
		{ServerName: "parallel", RemoteName: "read_b", Definition: definitions[1]},
	}
}

func (*parallelToolRegistryForTestOnly) Definitions([]string, []ToolName) []ToolDefinition {
	definition := func(name ToolName) ToolDefinition {
		return ToolDefinition{
			Name:                      name,
			Description:               "Exercise bounded parallel execution.",
			InputSchema:               MustJSONObject(`{"type":"object","additionalProperties":false}`),
			AttemptTimeout:            10 * time.Second,
			MaximumAttempts:           1,
			RetryTotalDuration:        10 * time.Second,
			SupportsParallelExecution: true,
			RetryExhaustionPolicy:     ToolRetryExhaustionPolicyManualRecovery,
		}
	}
	return []ToolDefinition{definition("parallel_read_a"), definition("parallel_read_b")}
}

func (registry *parallelToolRegistryForTestOnly) Execute(
	ctx context.Context,
	_ ToolInvocation,
) (ToolExecutionResult, error) {
	active := registry.active.Add(1)
	defer registry.active.Add(-1)
	for {
		maximum := registry.maximumActive.Load()
		if active <= maximum || registry.maximumActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	if active >= 2 {
		registry.readyOnce.Do(func() { close(registry.ready) })
	}
	select {
	case <-ctx.Done():
		return ToolExecutionResult{}, ctx.Err()
	case <-registry.ready:
		return ToolExecutionResult{Content: `{"ok":true}`, Outcome: ToolOutcomeSucceeded}, nil
	}
}

func newRetryToolRegistryForTestOnly(failures int, knownFailure bool) *retryToolRegistryForTestOnly {
	return &retryToolRegistryForTestOnly{
		failuresRemaining:     failures,
		knownFailure:          knownFailure,
		maximumAttempts:       3,
		retryExhaustionPolicy: ToolRetryExhaustionPolicyContinueWithUnknown,
	}
}

func (*retryToolRegistryForTestOnly) ServerNames() []string { return []string{"integration"} }

func (registry *retryToolRegistryForTestOnly) RegisteredTools() []RegisteredTool {
	return []RegisteredTool{{
		ServerName: "integration",
		RemoteName: string(integrationToolName),
		Definition: registry.definitionForTestOnly(),
	}}
}

func (registry *retryToolRegistryForTestOnly) Definitions([]string, []ToolName) []ToolDefinition {
	return []ToolDefinition{registry.definitionForTestOnly()}
}

func (registry *retryToolRegistryForTestOnly) definitionForTestOnly() ToolDefinition {
	return ToolDefinition{
		Name:                  integrationToolName,
		Description:           "Exercise Dex-owned retries.",
		InputSchema:           MustJSONObject(`{"type":"object","additionalProperties":false}`),
		AttemptTimeout:        10 * time.Second,
		MaximumAttempts:       registry.maximumAttempts,
		RetryTotalDuration:    20 * time.Second,
		RetryExhaustionPolicy: registry.retryExhaustionPolicy,
	}
}

func (registry *retryToolRegistryForTestOnly) Execute(
	_ context.Context,
	invocation ToolInvocation,
) (ToolExecutionResult, error) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	registry.invocations = append(registry.invocations, invocation)
	if registry.knownFailure {
		registry.returnedOutcomes = append(registry.returnedOutcomes, ToolOutcomeKnownFailure)
		return ToolExecutionResult{
			Content: `{"status":"failed","error":"lease expired; retry with a new tool call"}`,
			Outcome: ToolOutcomeKnownFailure,
			IsError: true,
		}, nil
	}
	if registry.returnedUnknown {
		registry.returnedOutcomes = append(registry.returnedOutcomes, ToolOutcomeUnknown)
		return ToolExecutionResult{
			Content: `{"status":"failed","outcome":"unknown"}`,
			Outcome: ToolOutcomeUnknown,
			IsError: true,
		}, nil
	}
	if registry.failuresRemaining > 0 {
		registry.failuresRemaining--
		return ToolExecutionResult{}, errors.New("transient fixture failure")
	}
	registry.returnedOutcomes = append(registry.returnedOutcomes, ToolOutcomeSucceeded)
	return ToolExecutionResult{Content: `{"ok":true}`, Outcome: ToolOutcomeSucceeded}, nil
}

func (registry *retryToolRegistryForTestOnly) setFailuresRemainingForTestOnly(failures int) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	registry.failuresRemaining = failures
}

func (registry *retryToolRegistryForTestOnly) assertManualRetryAttemptsForTestOnly(
	t *testing.T,
	attempts []int32,
) {
	t.Helper()
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if len(registry.invocations) != len(attempts) {
		t.Fatalf("tool invocations = %d, want %d", len(registry.invocations), len(attempts))
	}
	callID := registry.invocations[0].CallID
	for index, invocation := range registry.invocations {
		if invocation.Attempt != attempts[index] {
			t.Fatalf("attempt %d = %d, want %d", index, invocation.Attempt, attempts[index])
		}
		if invocation.CallID != callID {
			t.Fatalf("manual retry call ID = %q, want %q", invocation.CallID, callID)
		}
	}
	if registry.invocations[0].FirstAttemptAt != registry.invocations[1].FirstAttemptAt ||
		registry.invocations[2].FirstAttemptAt != registry.invocations[3].FirstAttemptAt ||
		registry.invocations[0].FirstAttemptAt == registry.invocations[2].FirstAttemptAt ||
		registry.invocations[2].FirstAttemptAt == registry.invocations[4].FirstAttemptAt {
		t.Fatalf("manual retry execution timestamps = %#v", registry.invocations)
	}
	if !slices.Equal(registry.returnedOutcomes, []ToolOutcome{ToolOutcomeSucceeded}) {
		t.Fatalf("returned outcomes = %v, want one success", registry.returnedOutcomes)
	}
}

func (registry *retryToolRegistryForTestOnly) assertAttemptsForTestOnly(
	t *testing.T,
	attempts []int32,
	outcome ToolOutcome,
) {
	t.Helper()
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if len(registry.invocations) != len(attempts) {
		t.Fatalf("tool invocations = %d, want %d", len(registry.invocations), len(attempts))
	}
	var callID CallID
	var firstAttemptAt time.Time
	for index, invocation := range registry.invocations {
		if invocation.Attempt != attempts[index] {
			t.Fatalf("attempt %d = %d, want %d", index, invocation.Attempt, attempts[index])
		}
		if index == 0 {
			callID = invocation.CallID
			firstAttemptAt = invocation.FirstAttemptAt
		} else if invocation.CallID != callID || !invocation.FirstAttemptAt.Equal(firstAttemptAt) {
			t.Fatalf("retry identity changed: %#v", invocation)
		}
	}
	if firstAttemptAt.IsZero() {
		t.Fatal("first attempt timestamp is empty")
	}
	if outcome == ToolOutcomeUnknown {
		if len(registry.returnedOutcomes) != 0 &&
			!slices.Equal(registry.returnedOutcomes, []ToolOutcome{ToolOutcomeUnknown}) {
			t.Fatalf("returned outcomes = %v, want none or one unknown", registry.returnedOutcomes)
		}
		return
	}
	if len(registry.returnedOutcomes) != 1 || registry.returnedOutcomes[0] != outcome {
		t.Fatalf("returned outcomes = %v, want %q", registry.returnedOutcomes, outcome)
	}
}

func waitForCompletedToolTurnForTestOnly(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	minimumSequence Sequence,
) {
	t.Helper()
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage &&
			len(state.PendingToolCalls) == 0 &&
			state.LastSequence >= minimumSequence
	})
}

func waitForPendingToolRecoveryForTestOnly(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	previous RecoveryID,
) PendingToolRecovery {
	t.Helper()
	snapshot := waitForSnapshot(t, environment, flowID, func(snapshot AgentSnapshot) bool {
		return snapshot.Description.Status == AgentStatusWaitingForToolRecovery &&
			snapshot.Description.PendingToolRecovery != nil &&
			snapshot.Description.PendingToolRecovery.RecoveryID != previous
	})
	pending := snapshot.Description.PendingToolRecovery
	if pending == nil || pending.StartedAt == nil || len(pending.Calls) != 1 {
		t.Fatalf("pending recovery = %#v", pending)
	}
	return *pending
}

func resolveSingleToolRecoveryForTestOnly(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	pending PendingToolRecovery,
	action ToolRecoveryAction,
) {
	t.Helper()
	if err := environment.agent.ResolveToolRecovery(t.Context(), flowID, ResolveToolRecoveryRequest{
		RecoveryID: pending.RecoveryID,
		Resolution: ToolRecoveryResolutionResume,
		Decisions: []ToolRecoveryDecision{{
			CallID: pending.Calls[0].CallID,
			Action: action,
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func historyToolCountForTestOnly(snapshot AgentSnapshot) int {
	count := 0
	for _, message := range snapshot.History.Messages {
		if message.Message.Role == MessageRoleTool {
			count++
		}
	}
	return count
}

func waitForStepCompletionForTestOnly(
	t *testing.T,
	environment *agentIntegrationEnvironment,
	flowID FlowID,
	step stepType,
	execution int32,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationWaitTimeout)
	defer cancel()
	if err := environment.sdk.WaitForStepCompletion(ctx, string(flowID), dex.StepExecutionID{
		StepType:        string(step),
		ExecutionNumber: &execution,
	}, dex.WaitForStepCompletionOptions{}); err != nil {
		t.Fatalf("wait for %s completion: %v", step, err)
	}
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

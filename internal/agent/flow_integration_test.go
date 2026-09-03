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
	"testing"
	"time"

	"github.com/superdurable/dex/blob-cache-go/blobcache"
	"github.com/superdurable/dex/sdk-go/dex"
)

const integrationToolName ToolName = "integration_tool"

func TestAgentFlowDurabilityIntegration(t *testing.T) {
	modelClient := integrationModel{}
	toolRegistry := newIntegrationToolRegistry()
	environment := newAgentIntegrationEnvironment(t, modelClient, toolRegistry)
	flowID := FlowID("agent-integration-" + randomLocalID(t))
	config := NewAgentConfig()
	config.MaxContextTokens = 80
	config.CompactionTriggerFraction = 0.60
	config.CompactionKeepFraction = 0.20
	config.MessageRetentionLimit = 8

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

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	state := waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && state.LastSequence >= 2
	})
	assertApplicationMessage(t, environment, flowID, state.LastSequence-1, MessageRoleUser, "hello")
	assertApplicationMessage(t, environment, flowID, state.LastSequence, MessageRoleAssistant, "integration response: hello")
	assertTextStream(t, environment.agent, flowID, EventStreamAssistant, "integration response: hello")
	assertTextStream(t, environment.agent, flowID, EventStreamReasoning, "deterministic integration summary")

	environment.replaceWorker(t)
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/tool"}); err != nil {
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
	if err := environment.agent.ApproveTool(t.Context(), flowID, ToolApprovalRequest{
		CallID: approval.CallID, Approved: true,
	}); err != nil {
		t.Fatal(err)
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && len(state.PendingToolCalls) == 0
	})
	toolRegistry.assertCallsUseID(t, approval.CallID)

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{
		Content: "ship safely", PlanMode: true,
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
	waitForAgentPlan(t, environment, flowID, PlanStatusCompleted)

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/ask deployment region"}); err != nil {
		t.Fatal(err)
	}
	waitForPendingUserInput(t, environment, flowID)
	environment.replaceWorker(t)
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "us-west"}); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingUserInput(t, environment, flowID)

	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "/wait"}); err != nil {
		t.Fatal(err)
	}
	waitForPendingTimer(t, environment, flowID)
	environment.replaceWorker(t)
	stateBeforeQueue := readAgentState(t, environment, flowID)
	if err := environment.agent.SendMessage(t.Context(), flowID, UserMessage{Content: "queued message"}); err != nil {
		t.Fatal(err)
	}
	queued := waitForQueuedMessages(t, environment, flowID, 1)
	if stateAfterQueue := readAgentState(t, environment, flowID); stateAfterQueue.LastSequence != stateBeforeQueue.LastSequence {
		t.Fatalf("queued message entered history: sequence advanced from %d to %d", stateBeforeQueue.LastSequence, stateAfterQueue.LastSequence)
	}
	if err := environment.agent.SteerMessage(t.Context(), flowID, SteerMessageRequest{
		MessageID: MessageID(queued[0].MessageID),
		Message:   UserMessage{Content: "urgent steering"},
	}); err != nil {
		t.Fatal(err)
	}
	waitForNoPendingTimer(t, environment, flowID)
	waitForQueuedMessages(t, environment, flowID, 0)
	state = waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.Status == AgentStatusWaitingForMessage && state.LastSequence > stateBeforeQueue.LastSequence
	})
	if !historyContains(t, environment, flowID, state, MessageRoleUser, "urgent steering") {
		t.Fatal("steered message did not enter application history")
	}
	if historyContains(t, environment, flowID, state, MessageRoleUser, "queued message") {
		t.Fatal("deleted queued message entered application history")
	}
	waitForAgentState(t, environment, flowID, func(state AgentState) bool {
		return state.CompactionGeneration > 0
	})
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

func waitForPendingUserInput(t *testing.T, environment *agentIntegrationEnvironment, flowID FlowID) {
	t.Helper()
	var pending PendingUserInput
	waitUntil(t, environment, "pending user input", func() (bool, error) {
		return environment.sdk.GetAttribute(t.Context(), string(flowID), pendingUserInputAttribute, &pending)
	})
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
	var message AgentMessage
	found, err := environment.sdk.GetAttributeMapInstance(
		t.Context(),
		string(flowID),
		agentMessagesAttribute,
		sequenceKey(sequence),
		&message,
	)
	if err != nil {
		t.Fatal(err)
	}
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
		var message AgentMessage
		found, err := environment.sdk.GetAttributeMapInstance(
			t.Context(),
			string(flowID),
			agentMessagesAttribute,
			sequenceKey(sequence),
			&message,
		)
		if err != nil {
			t.Fatal(err)
		}
		if found && message.Role == role && message.Content == content {
			return true
		}
	}
	return false
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

type integrationModel struct{}

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
	if hasActiveIntegrationPlan(request.Messages) {
		arguments := integrationPlanArguments(request.Messages, TaskStatusCompleted)
		return integrationToolReply(request, ToolNameWriteTodos, arguments, "completed plan")
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
	if strings.HasPrefix(userContent, "/ask ") {
		prompt, err := json.Marshal(struct {
			Prompt string `json:"prompt"`
		}{Prompt: strings.TrimSpace(strings.TrimPrefix(userContent, "/ask "))})
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
	digest := sha256.Sum256([]byte(string(request.FlowID) + "\x00" + string(name) + "\x00" + integrationLastUserContent(request.Messages)))
	return ModelReply{
		Content: content,
		ToolCalls: []ToolCall{{
			ID:        CallID("call-" + hex.EncodeToString(digest[:16])),
			Name:      name,
			Arguments: arguments,
		}},
	}, nil
}

func integrationPlanArguments(messages []AgentMessage, status TaskStatus) JSONObject {
	content := integrationLastUserContent(messages)
	if content == "" {
		content = "integration objective"
	}
	encoded, err := json.Marshal(writeTodosArguments{Todos: []PlanTask{{Content: content, Status: status}}})
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

func hasActiveIntegrationPlan(messages []AgentMessage) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == MessageRoleSystem && strings.Contains(message.Content, "Current durable plan:") {
			return strings.Contains(message.Content, `"status":"active"`) && strings.Contains(message.Content, `"status":"pending"`)
		}
	}
	return false
}

type integrationToolRegistry struct {
	mutex   sync.Mutex
	callIDs []CallID
}

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
	registry.callIDs = append(registry.callIDs, invocation.CallID)
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
	if len(registry.callIDs) != 1 {
		t.Fatalf("tool executions = %d, want exactly one", len(registry.callIDs))
	}
	for _, actual := range registry.callIDs {
		if actual != callID {
			t.Fatalf("tool call ID changed across Worker replacement: got %q, want %q", actual, callID)
		}
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

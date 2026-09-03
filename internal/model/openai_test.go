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

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/superdurable/superagent/internal/agent"
)

func TestOpenAICompleteUsesStatelessResponsesAndSeparatesStreams(t *testing.T) {
	t.Parallel()
	requestReceived := make(chan openAIRequestFixture, 1)
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/responses" {
			return fixtureResponse(request, http.StatusNotFound, "application/json", `{"error":{"message":"unexpected path"}}`), nil
		}
		if request.Header.Get("Authorization") != "Bearer test-openai-key" {
			return fixtureResponse(request, http.StatusUnauthorized, "application/json", `{"error":{"message":"unexpected authorization"}}`), nil
		}
		var payload openAIRequestFixture
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, fmt.Errorf("decode fixture request: %w", err)
		}
		requestReceived <- payload
		body := strings.Join([]string{
			sse(`{"type":"response.reasoning_summary_text.delta","delta":"Checking constraints.","item_id":"rs-new","output_index":0,"summary_index":0,"sequence_number":1}`),
			sse(`{"type":"response.output_text.delta","delta":"I can help.","item_id":"msg-new","output_index":1,"content_index":0,"logprobs":[],"sequence_number":2}`),
			sse(`{"type":"response.output_item.done","output_index":0,"sequence_number":3,"item":{"id":"rs-new","type":"reasoning","summary":[{"type":"summary_text","text":"Checking constraints."}],"encrypted_content":"encrypted-new","status":"completed"}}`),
			sse(`{"type":"response.output_item.done","output_index":1,"sequence_number":4,"item":{"id":"fc-new","type":"function_call","call_id":"call-plan","name":"write_todos","arguments":"{\"todos\":[]}","status":"completed"}}`),
			sse(`{"type":"response.completed","response":{"status":"completed"},"sequence_number":5}`),
			"data: [DONE]\n\n",
		}, "")
		return fixtureResponse(request, http.StatusOK, "text/event-stream", body), nil
	})}

	credentials := NewCredentialStore()
	flowID := agent.FlowID("flow-openai-test")
	if err := credentials.SetAPIKey(flowID, agent.ProviderOpenAI, "test-openai-key"); err != nil {
		t.Fatal(err)
	}
	client := NewOpenAIClient(credentials, httpClient, "https://api.openai.test")
	oldContext, err := agent.ParseJSONValue(`{"id":"rs-old","type":"reasoning","summary":[],"encrypted_content":"encrypted-old"}`)
	if err != nil {
		t.Fatal(err)
	}
	oldCallID := agent.CallID("call-old")
	oldToolName := agent.ToolName("search")
	assistantChunks := make([]string, 0)
	reasoningChunks := make([]string, 0)
	activity := make([]agent.AgentEvent, 0)
	reply, err := client.Complete(context.Background(), agent.ModelRequest{
		Config: agent.AgentConfig{
			Model:        "openai/gpt-5-mini",
			SystemPrompt: "Be helpful.",
		},
		Messages: []agent.AgentMessage{
			{
				Role: agent.MessageRoleAssistant,
				ToolCalls: []agent.ToolCall{{
					ID:        oldCallID,
					Name:      oldToolName,
					Arguments: agent.MustJSONObject(`{"query":"Dex"}`),
				}},
				ProviderContextItems: []agent.ProviderContextItem{{
					Provider: agent.ProviderOpenAI,
					Item:     oldContext,
				}},
			},
			{
				Role:       agent.MessageRoleTool,
				Content:    `{"results":[]}`,
				ToolCallID: &oldCallID,
				ToolName:   &oldToolName,
			},
			{Role: agent.MessageRoleUser, Content: "Make a plan"},
		},
		Tools: []agent.ToolDefinition{{
			Name:        agent.ToolNameWriteTodos,
			Description: "Write the plan.",
			InputSchema: agent.MustJSONObject(`{"type":"object","properties":{"todos":{"type":"array"}},"required":["todos"]}`),
		}},
		WriteAssistant: func(delta string) error {
			assistantChunks = append(assistantChunks, delta)
			return nil
		},
		WriteReasoning: func(delta string) error {
			reasoningChunks = append(reasoningChunks, delta)
			return nil
		},
		WriteActivity: func(event agent.AgentEvent) error {
			activity = append(activity, event)
			return nil
		},
		FlowID: flowID,
	})
	if err != nil {
		t.Fatal(err)
	}

	payload := <-requestReceived
	if payload.Model != "gpt-5-mini" || payload.Instructions != "Be helpful." {
		t.Fatalf("unexpected request model/instructions: %#v", payload)
	}
	if payload.Store || !payload.Stream || payload.ParallelToolCalls {
		t.Fatalf("request must be stateless, streaming, and sequential: %#v", payload)
	}
	if !slices.Equal(payload.Include, []string{"reasoning.encrypted_content"}) {
		t.Fatalf("include = %q", payload.Include)
	}
	if payload.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning summary = %q", payload.Reasoning.Summary)
	}
	if len(payload.Input) != 4 || len(payload.Tools) != 1 {
		t.Fatalf("input/tools lengths = %d/%d", len(payload.Input), len(payload.Tools))
	}
	if !slices.Equal(assistantChunks, []string{"I can help."}) ||
		!slices.Equal(reasoningChunks, []string{"Checking constraints."}) {
		t.Fatalf("assistant/reasoning chunks = %q/%q", assistantChunks, reasoningChunks)
	}
	if len(activity) != 1 || activity[0].Kind != agent.EventKindModelToolCall {
		t.Fatalf("activity = %#v", activity)
	}
	if reply.Content != "I can help." || len(reply.ToolCalls) != 1 ||
		reply.ToolCalls[0].ID != "call-plan" || reply.ToolCalls[0].Name != agent.ToolNameWriteTodos {
		t.Fatalf("reply = %#v", reply)
	}
	if len(reply.ProviderContextItems) != 1 ||
		reply.ProviderContextItems[0].Provider != agent.ProviderOpenAI {
		t.Fatalf("provider context = %#v", reply.ProviderContextItems)
	}
	var reasoningState struct {
		EncryptedContent string `json:"encrypted_content"`
		Type             string `json:"type"`
	}
	if err := json.Unmarshal([]byte(reply.ProviderContextItems[0].Item), &reasoningState); err != nil {
		t.Fatal(err)
	}
	if reasoningState.Type != "reasoning" || reasoningState.EncryptedContent != "encrypted-new" {
		t.Fatalf("reasoning state = %#v", reasoningState)
	}
}

func TestOpenAIInputDropsOrphanToolOutput(t *testing.T) {
	t.Parallel()
	callID := agent.CallID("missing")
	input, err := openAIInput([]agent.AgentMessage{{
		Role:       agent.MessageRoleTool,
		Content:    "orphan",
		ToolCallID: &callID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 0 {
		t.Fatalf("input length = %d, want 0", len(input))
	}
}

func TestOpenAIDisablesSDKRetries(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return fixtureResponse(
			request,
			http.StatusServiceUnavailable,
			"application/json",
			`{"error":{"message":"transient fixture failure"}}`,
		), nil
	})}
	credentials := NewCredentialStore()
	if err := credentials.SetDefaultAPIKey(agent.ProviderOpenAI, "test-openai-key"); err != nil {
		t.Fatal(err)
	}
	client := NewOpenAIClient(credentials, httpClient, "https://api.openai.test")
	_, err := client.Complete(context.Background(), agent.ModelRequest{
		Config:         agent.AgentConfig{Model: "openai/gpt-5-mini", SystemPrompt: "Be helpful."},
		Messages:       []agent.AgentMessage{{Role: agent.MessageRoleUser, Content: "Hello"}},
		WriteAssistant: func(string) error { return nil },
		WriteReasoning: func(string) error { return nil },
		WriteActivity:  func(agent.AgentEvent) error { return nil },
		FlowID:         "flow-no-hidden-retries",
	})
	if err == nil {
		t.Fatal("Complete() error = nil")
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1", requests.Load())
	}
}

type openAIRequestFixture struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	Include           []string          `json:"include"`
	Input             []json.RawMessage `json:"input"`
	Tools             []json.RawMessage `json:"tools"`
	Reasoning         struct {
		Summary string `json:"summary"`
	} `json:"reasoning"`
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func fixtureResponse(request *http.Request, status int, contentType string, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func sse(payload string) string {
	return "data: " + payload + "\n\n"
}

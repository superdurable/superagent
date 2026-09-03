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
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/superdurable/superagent/internal/agent"
)

func TestAnthropicCompleteMapsTextAndToolUse(t *testing.T) {
	t.Parallel()
	credentials := NewCredentialStore()
	if err := credentials.SetDefaultAPIKey(agent.ProviderAnthropic, "anthropic-test-key"); err != nil {
		t.Fatal(err)
	}
	httpClient := fixtureClient(t, func(request *http.Request) string {
		if request.Header.Get("X-Api-Key") != "anthropic-test-key" ||
			request.Header.Get("Anthropic-Version") != anthropicVersion {
			t.Error("Anthropic headers were not set")
		}
		return `{"content":[{"type":"text","text":"I will check."},{"type":"tool_use","id":"call-weather","name":"weather","input":{"city":"Seattle"}}]}`
	})
	client, err := NewAnthropicClient(credentials, httpClient, "https://anthropic.test/v1")
	if err != nil {
		t.Fatal(err)
	}
	assertProviderReply(t, client, agent.ProviderAnthropic, "anthropic/claude-test", "I will check.", "call-weather", "weather")
}

func TestGeminiCompleteCreatesStableToolCallID(t *testing.T) {
	t.Parallel()
	credentials := NewCredentialStore()
	if err := credentials.SetDefaultAPIKey(agent.ProviderGemini, "gemini-test-key"); err != nil {
		t.Fatal(err)
	}
	httpClient := fixtureClient(t, func(request *http.Request) string {
		if request.Header.Get("X-Goog-Api-Key") != "gemini-test-key" {
			t.Error("Gemini API key header was not set")
		}
		if request.URL.Path != "/v1beta/models/gemini-test:generateContent" {
			t.Errorf("Gemini path = %q", request.URL.Path)
		}
		return `{"candidates":[{"content":{"role":"model","parts":[{"text":"I will check."},{"functionCall":{"name":"weather","args":{"city":"Seattle"}}}]}}]}`
	})
	client, err := NewGeminiClient(credentials, httpClient, "https://gemini.test/v1beta")
	if err != nil {
		t.Fatal(err)
	}
	first := assertProviderReply(t, client, agent.ProviderGemini, "gemini/gemini-test", "I will check.", "", "weather")
	second := assertProviderReply(t, client, agent.ProviderGemini, "gemini/gemini-test", "I will check.", "", "weather")
	if first.ToolCalls[0].ID == "" || first.ToolCalls[0].ID != second.ToolCalls[0].ID {
		t.Fatalf("Gemini call IDs are not stable: %q and %q", first.ToolCalls[0].ID, second.ToolCalls[0].ID)
	}
}

func TestGroqCompleteMapsChatToolCall(t *testing.T) {
	t.Parallel()
	credentials := NewCredentialStore()
	if err := credentials.SetDefaultAPIKey(agent.ProviderGroq, "groq-test-key"); err != nil {
		t.Fatal(err)
	}
	httpClient := fixtureClient(t, func(request *http.Request) string {
		if request.Header.Get("Authorization") != "Bearer groq-test-key" {
			t.Error("Groq authorization header was not set")
		}
		return `{"choices":[{"message":{"role":"assistant","content":"I will check.","tool_calls":[{"id":"call-weather","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Seattle\"}"}}]}}]}`
	})
	client, err := NewGroqClient(credentials, httpClient, "https://groq.test/openai/v1")
	if err != nil {
		t.Fatal(err)
	}
	assertProviderReply(t, client, agent.ProviderGroq, "groq/llama-test", "I will check.", "call-weather", "weather")
}

func fixtureClient(t *testing.T, response func(*http.Request) string) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := response(request)
		return fixtureResponse(request, http.StatusOK, "application/json", body), nil
	})}
}

func assertProviderReply(
	t *testing.T,
	client agent.ModelClient,
	provider agent.Provider,
	model agent.Model,
	wantContent string,
	wantCallID agent.CallID,
	wantTool agent.ToolName,
) agent.ModelReply {
	t.Helper()
	assistant := make([]string, 0)
	activity := make([]agent.AgentEvent, 0)
	reply, err := client.Complete(context.Background(), agent.ModelRequest{
		Config:   agent.AgentConfig{Model: model, SystemPrompt: "Be helpful."},
		Messages: []agent.AgentMessage{{Role: agent.MessageRoleUser, Content: "Check weather."}},
		Tools: []agent.ToolDefinition{{
			Name:        wantTool,
			Description: "Get weather.",
			InputSchema: agent.MustJSONObject(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}},
		WriteAssistant: func(delta string) error {
			assistant = append(assistant, delta)
			return nil
		},
		WriteReasoning: func(string) error { return nil },
		WriteActivity: func(event agent.AgentEvent) error {
			activity = append(activity, event)
			return nil
		},
		FlowID: agent.FlowID(fmt.Sprintf("flow-%s", provider)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != wantContent || !slices.Equal(assistant, []string{wantContent}) {
		t.Fatalf("reply/stream content = %q/%q", reply.Content, assistant)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Name != wantTool {
		t.Fatalf("tool calls = %#v", reply.ToolCalls)
	}
	if wantCallID != "" && reply.ToolCalls[0].ID != wantCallID {
		t.Fatalf("call ID = %q, want %q", reply.ToolCalls[0].ID, wantCallID)
	}
	if len(activity) != 1 || activity[0].Kind != agent.EventKindModelToolCall {
		t.Fatalf("activity = %#v", activity)
	}
	return reply
}

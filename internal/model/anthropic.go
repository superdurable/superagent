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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/superdurable/superagent/internal/agent"
)

const (
	defaultAnthropicBaseURL   = "https://api.anthropic.com/v1"
	anthropicVersion          = "2023-06-01"
	anthropicMaximumTokens    = 8192
	anthropicRoleUser         = "user"
	anthropicRoleAssistant    = "assistant"
	anthropicContentText      = "text"
	anthropicContentToolUse   = "tool_use"
	anthropicContentToolReply = "tool_result"
)

// AnthropicClient implements the native Anthropic Messages API boundary.
type AnthropicClient struct {
	credentials *CredentialStore
	httpClient  *http.Client
	baseURL     *url.URL
}

// NewAnthropicClient constructs an Anthropic adapter.
func NewAnthropicClient(credentials *CredentialStore, httpClient *http.Client, baseURL string) (*AnthropicClient, error) {
	if credentials == nil {
		panic("Anthropic credential store is required")
	}
	if httpClient == nil {
		panic("Anthropic HTTP client is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultAnthropicBaseURL
	}
	parsed, err := parseProviderBaseURL("Anthropic", baseURL)
	if err != nil {
		return nil, err
	}
	return &AnthropicClient{credentials: credentials, httpClient: httpClient, baseURL: parsed}, nil
}

// Complete sends one typed Anthropic Messages request.
func (client *AnthropicClient) Complete(ctx context.Context, request agent.ModelRequest) (agent.ModelReply, error) {
	if err := validateModelRequest(request, agent.ProviderAnthropic); err != nil {
		return agent.ModelReply{}, err
	}
	modelName, err := request.Config.Model.ProviderModel()
	if err != nil {
		return agent.ModelReply{}, err
	}
	system, messages, err := anthropicMessages(request.Config.SystemPrompt, request.Messages)
	if err != nil {
		return agent.ModelReply{}, err
	}
	tools := make([]anthropicTool, 0, len(request.Tools))
	for _, definition := range request.Tools {
		tools = append(tools, anthropicTool{
			Name:        string(definition.Name),
			Description: definition.Description,
			InputSchema: json.RawMessage(definition.InputSchema),
		})
	}
	payload := anthropicRequest{
		Model:     modelName,
		MaxTokens: anthropicMaximumTokens,
		System:    system,
		Messages:  messages,
		Tools:     tools,
	}
	if request.ForcedTool != "" {
		if !hasDefinition(request.Tools, request.ForcedTool) {
			return agent.ModelReply{}, fmt.Errorf("forced tool %q is not available", request.ForcedTool)
		}
		payload.ToolChoice = &anthropicToolChoice{Type: "tool", Name: string(request.ForcedTool)}
	}
	var response anthropicResponse
	if err := doProviderJSON(ctx, client.httpClient, "Anthropic", providerURL(client.baseURL, "messages"), http.Header{
		"X-Api-Key":         []string{client.credentials.APIKey(request.FlowID, agent.ProviderAnthropic)},
		"Anthropic-Version": []string{anthropicVersion},
	}, payload, &response); err != nil {
		return agent.ModelReply{}, err
	}
	return consumeAnthropicResponse(response, request)
}

// Summarize compacts application messages with Anthropic.
func (client *AnthropicClient) Summarize(ctx context.Context, request agent.SummarizeRequest) (string, error) {
	model := request.Config.Model
	if request.Config.CompactionModel != nil {
		model = *request.Config.CompactionModel
	}
	provider, err := model.Provider()
	if err != nil {
		return "", err
	}
	if provider != agent.ProviderAnthropic {
		return "", fmt.Errorf("anthropic adapter cannot summarize with provider %q", provider)
	}
	modelName, err := model.ProviderModel()
	if err != nil {
		return "", err
	}
	transcript, err := json.Marshal(request.Messages)
	if err != nil {
		return "", fmt.Errorf("encode compaction transcript: %w", err)
	}
	prompt, err := anthropicTextBlock("Previous summary:\n" + request.PreviousSummary + "\n\nMessages:\n" + string(transcript))
	if err != nil {
		return "", err
	}
	var response anthropicResponse
	if err := doProviderJSON(ctx, client.httpClient, "Anthropic", providerURL(client.baseURL, "messages"), http.Header{
		"X-Api-Key":         []string{client.credentials.APIKey(request.FlowID, agent.ProviderAnthropic)},
		"Anthropic-Version": []string{anthropicVersion},
	}, anthropicRequest{
		Model:     modelName,
		MaxTokens: anthropicMaximumTokens,
		System:    compactionInstruction,
		Messages:  []anthropicMessage{{Role: anthropicRoleUser, Content: []json.RawMessage{prompt}}},
	}, &response); err != nil {
		return "", err
	}
	var summary strings.Builder
	for _, block := range response.Content {
		if block.Type == anthropicContentText {
			summary.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(summary.String()) == "" {
		return "", errors.New("anthropic compaction response was empty")
	}
	return strings.TrimSpace(summary.String()), nil
}

// CountTokens returns a conservative provider-neutral estimate.
func (*AnthropicClient) CountTokens(_ agent.Model, messages []agent.AgentMessage) int {
	return estimatedTokens(messages)
}

type anthropicRequest struct {
	Model      string               `json:"model"`
	MaxTokens  int                  `json:"max_tokens"`
	System     string               `json:"system"`
	Messages   []anthropicMessage   `json:"messages"`
	Tools      []anthropicTool      `json:"tools,omitempty"`
	ToolChoice *anthropicToolChoice `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type anthropicResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
}

func anthropicMessages(systemPrompt string, messages []agent.AgentMessage) (string, []anthropicMessage, error) {
	systemParts := []string{systemPrompt}
	result := make([]anthropicMessage, 0, len(messages))
	for _, message := range withoutOrphanToolOutputs(messages) {
		switch message.Role {
		case agent.MessageRoleSystem:
			if message.Content != "" {
				systemParts = append(systemParts, message.Content)
			}
		case agent.MessageRoleUser:
			block, err := anthropicTextBlock(message.Content)
			if err != nil {
				return "", nil, err
			}
			appendAnthropicMessage(&result, anthropicRoleUser, block)
		case agent.MessageRoleAssistant:
			blocks := make([]json.RawMessage, 0, len(message.ToolCalls)+1)
			if message.Content != "" {
				block, err := anthropicTextBlock(message.Content)
				if err != nil {
					return "", nil, err
				}
				blocks = append(blocks, block)
			}
			for _, call := range message.ToolCalls {
				block, err := json.Marshal(struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				}{anthropicContentToolUse, string(call.ID), string(call.Name), json.RawMessage(call.Arguments)})
				if err != nil {
					return "", nil, fmt.Errorf("encode Anthropic tool call: %w", err)
				}
				blocks = append(blocks, block)
			}
			if len(blocks) > 0 {
				result = append(result, anthropicMessage{Role: anthropicRoleAssistant, Content: blocks})
			}
		case agent.MessageRoleTool:
			if message.ToolCallID == nil {
				return "", nil, errors.New("anthropic tool message requires a call ID")
			}
			block, err := json.Marshal(struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
			}{anthropicContentToolReply, string(*message.ToolCallID), message.Content})
			if err != nil {
				return "", nil, fmt.Errorf("encode Anthropic tool result: %w", err)
			}
			appendAnthropicMessage(&result, anthropicRoleUser, block)
		default:
			return "", nil, fmt.Errorf("unsupported Anthropic message role %q", message.Role)
		}
	}
	return strings.Join(systemParts, "\n\n"), result, nil
}

func anthropicTextBlock(content string) (json.RawMessage, error) {
	encoded, err := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{anthropicContentText, content})
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic text: %w", err)
	}
	return encoded, nil
}

func appendAnthropicMessage(messages *[]anthropicMessage, role string, block json.RawMessage) {
	if len(*messages) > 0 && (*messages)[len(*messages)-1].Role == role {
		(*messages)[len(*messages)-1].Content = append((*messages)[len(*messages)-1].Content, block)
		return
	}
	*messages = append(*messages, anthropicMessage{Role: role, Content: []json.RawMessage{block}})
}

func consumeAnthropicResponse(response anthropicResponse, request agent.ModelRequest) (agent.ModelReply, error) {
	var content strings.Builder
	calls := make([]agent.ToolCall, 0)
	for _, block := range response.Content {
		switch block.Type {
		case anthropicContentText:
			content.WriteString(block.Text)
			if block.Text != "" {
				if err := request.WriteAssistant(block.Text); err != nil {
					return agent.ModelReply{}, fmt.Errorf("write assistant text: %w", err)
				}
			}
		case anthropicContentToolUse:
			if block.ID == "" || block.Name == "" {
				return agent.ModelReply{}, errors.New("anthropic returned an incomplete tool call")
			}
			arguments, err := agent.ParseJSONObject(string(block.Input))
			if err != nil {
				return agent.ModelReply{}, fmt.Errorf("anthropic function %q arguments: %w", block.Name, err)
			}
			calls = append(calls, agent.ToolCall{ID: agent.CallID(block.ID), Name: agent.ToolName(block.Name), Arguments: arguments})
		}
	}
	if err := writeToolActivities(calls, request.WriteActivity); err != nil {
		return agent.ModelReply{}, err
	}
	return agent.ModelReply{Content: content.String(), ToolCalls: calls}, nil
}

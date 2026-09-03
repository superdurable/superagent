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

const defaultGroqBaseURL = "https://api.groq.com/openai/v1"

type chatRole string

const (
	chatRoleSystem    chatRole = "system"
	chatRoleUser      chatRole = "user"
	chatRoleAssistant chatRole = "assistant"
	chatRoleTool      chatRole = "tool"
)

// GroqClient implements Groq's OpenAI-compatible Chat Completions boundary.
type GroqClient struct {
	credentials *CredentialStore
	httpClient  *http.Client
	baseURL     *url.URL
}

// NewGroqClient constructs a Groq adapter.
func NewGroqClient(credentials *CredentialStore, httpClient *http.Client, baseURL string) (*GroqClient, error) {
	if credentials == nil {
		panic("Groq credential store is required")
	}
	if httpClient == nil {
		panic("Groq HTTP client is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultGroqBaseURL
	}
	parsed, err := parseProviderBaseURL("Groq", baseURL)
	if err != nil {
		return nil, err
	}
	return &GroqClient{credentials: credentials, httpClient: httpClient, baseURL: parsed}, nil
}

// Complete sends one typed Groq completion and writes its complete visible text once.
func (client *GroqClient) Complete(ctx context.Context, request agent.ModelRequest) (agent.ModelReply, error) {
	if err := validateModelRequest(request, agent.ProviderGroq); err != nil {
		return agent.ModelReply{}, err
	}
	modelName, err := request.Config.Model.ProviderModel()
	if err != nil {
		return agent.ModelReply{}, err
	}
	messages, err := groqMessages(request.Config.SystemPrompt, request.Messages)
	if err != nil {
		return agent.ModelReply{}, err
	}
	tools := make([]groqTool, 0, len(request.Tools))
	for _, definition := range request.Tools {
		tools = append(tools, groqTool{Type: "function", Function: groqFunctionDefinition{
			Name:        string(definition.Name),
			Description: definition.Description,
			Parameters:  json.RawMessage(definition.InputSchema),
		}})
	}
	payload := groqRequest{Model: modelName, Messages: messages, Tools: tools}
	if request.ForcedTool != "" {
		if !hasDefinition(request.Tools, request.ForcedTool) {
			return agent.ModelReply{}, fmt.Errorf("forced tool %q is not available", request.ForcedTool)
		}
		payload.ToolChoice = &groqToolChoice{Type: "function"}
		payload.ToolChoice.Function.Name = string(request.ForcedTool)
	}
	var response groqResponse
	if err := doProviderJSON(
		ctx,
		client.httpClient,
		"Groq",
		providerURL(client.baseURL, "chat/completions"),
		http.Header{"Authorization": []string{"Bearer " + client.credentials.APIKey(request.FlowID, agent.ProviderGroq)}},
		payload,
		&response,
	); err != nil {
		return agent.ModelReply{}, err
	}
	if len(response.Choices) != 1 {
		return agent.ModelReply{}, fmt.Errorf("groq returned %d choices, want exactly one", len(response.Choices))
	}
	message := response.Choices[0].Message
	if message.Content != "" {
		if err := request.WriteAssistant(message.Content); err != nil {
			return agent.ModelReply{}, fmt.Errorf("write assistant text: %w", err)
		}
	}
	calls := make([]agent.ToolCall, 0, len(message.ToolCalls))
	for _, toolCall := range message.ToolCalls {
		arguments, err := agent.ParseJSONObject(toolCall.Function.Arguments)
		if err != nil {
			return agent.ModelReply{}, fmt.Errorf("groq function %q arguments: %w", toolCall.Function.Name, err)
		}
		if strings.TrimSpace(toolCall.ID) == "" || strings.TrimSpace(toolCall.Function.Name) == "" {
			return agent.ModelReply{}, errors.New("groq returned an incomplete tool call")
		}
		calls = append(calls, agent.ToolCall{
			ID:        agent.CallID(toolCall.ID),
			Name:      agent.ToolName(toolCall.Function.Name),
			Arguments: arguments,
		})
	}
	if err := writeToolActivities(calls, request.WriteActivity); err != nil {
		return agent.ModelReply{}, err
	}
	return agent.ModelReply{Content: message.Content, ToolCalls: calls}, nil
}

// Summarize compacts application messages with Groq.
func (client *GroqClient) Summarize(ctx context.Context, request agent.SummarizeRequest) (string, error) {
	model := request.Config.Model
	if request.Config.CompactionModel != nil {
		model = *request.Config.CompactionModel
	}
	provider, err := model.Provider()
	if err != nil {
		return "", err
	}
	if provider != agent.ProviderGroq {
		return "", fmt.Errorf("groq adapter cannot summarize with provider %q", provider)
	}
	modelName, err := model.ProviderModel()
	if err != nil {
		return "", err
	}
	transcript, err := json.Marshal(request.Messages)
	if err != nil {
		return "", fmt.Errorf("encode compaction transcript: %w", err)
	}
	var response groqResponse
	if err := doProviderJSON(ctx, client.httpClient, "Groq", providerURL(client.baseURL, "chat/completions"), http.Header{
		"Authorization": []string{"Bearer " + client.credentials.APIKey(request.FlowID, agent.ProviderGroq)},
	}, groqRequest{
		Model: modelName,
		Messages: []groqMessage{
			{Role: chatRoleSystem, Content: compactionInstruction},
			{Role: chatRoleUser, Content: "Previous summary:\n" + request.PreviousSummary + "\n\nMessages:\n" + string(transcript)},
		},
	}, &response); err != nil {
		return "", err
	}
	if len(response.Choices) != 1 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return "", errors.New("groq compaction response was empty")
	}
	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}

// CountTokens returns a conservative provider-neutral estimate.
func (*GroqClient) CountTokens(_ agent.Model, messages []agent.AgentMessage) int {
	return estimatedTokens(messages)
}

type groqRequest struct {
	Model      string          `json:"model"`
	Messages   []groqMessage   `json:"messages"`
	Tools      []groqTool      `json:"tools,omitempty"`
	ToolChoice *groqToolChoice `json:"tool_choice,omitempty"`
}

type groqMessage struct {
	Role       chatRole       `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []groqToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type groqTool struct {
	Type     string                 `json:"type"`
	Function groqFunctionDefinition `json:"function"`
}

type groqFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type groqToolChoice struct {
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

type groqToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type groqResponse struct {
	Choices []struct {
		Message groqMessage `json:"message"`
	} `json:"choices"`
}

func groqMessages(systemPrompt string, messages []agent.AgentMessage) ([]groqMessage, error) {
	result := []groqMessage{{Role: chatRoleSystem, Content: systemPrompt}}
	for _, message := range withoutOrphanToolOutputs(messages) {
		switch message.Role {
		case agent.MessageRoleSystem:
			result = append(result, groqMessage{Role: chatRoleSystem, Content: message.Content})
		case agent.MessageRoleUser:
			result = append(result, groqMessage{Role: chatRoleUser, Content: message.Content})
		case agent.MessageRoleAssistant:
			calls := make([]groqToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				var toolCall groqToolCall
				toolCall.ID = string(call.ID)
				toolCall.Type = "function"
				toolCall.Function.Name = string(call.Name)
				toolCall.Function.Arguments = call.Arguments.String()
				calls = append(calls, toolCall)
			}
			result = append(result, groqMessage{Role: chatRoleAssistant, Content: message.Content, ToolCalls: calls})
		case agent.MessageRoleTool:
			if message.ToolCallID == nil {
				return nil, errors.New("groq tool message requires a call ID")
			}
			name := ""
			if message.ToolName != nil {
				name = string(*message.ToolName)
			}
			result = append(result, groqMessage{
				Role:       chatRoleTool,
				Content:    message.Content,
				ToolCallID: string(*message.ToolCallID),
				Name:       name,
			})
		default:
			return nil, fmt.Errorf("unsupported Groq message role %q", message.Role)
		}
	}
	return result, nil
}

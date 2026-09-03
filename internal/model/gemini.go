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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/superdurable/superagent/internal/agent"
)

const (
	defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	geminiRoleUser       = "user"
	geminiRoleModel      = "model"
)

// GeminiClient implements the native Gemini generateContent boundary.
type GeminiClient struct {
	credentials *CredentialStore
	httpClient  *http.Client
	baseURL     *url.URL
}

// NewGeminiClient constructs a Gemini adapter.
func NewGeminiClient(credentials *CredentialStore, httpClient *http.Client, baseURL string) (*GeminiClient, error) {
	if credentials == nil {
		panic("Gemini credential store is required")
	}
	if httpClient == nil {
		panic("Gemini HTTP client is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultGeminiBaseURL
	}
	parsed, err := parseProviderBaseURL("Gemini", baseURL)
	if err != nil {
		return nil, err
	}
	return &GeminiClient{credentials: credentials, httpClient: httpClient, baseURL: parsed}, nil
}

// Complete sends one typed Gemini generateContent request.
func (client *GeminiClient) Complete(ctx context.Context, request agent.ModelRequest) (agent.ModelReply, error) {
	if err := validateModelRequest(request, agent.ProviderGemini); err != nil {
		return agent.ModelReply{}, err
	}
	modelName, err := request.Config.Model.ProviderModel()
	if err != nil {
		return agent.ModelReply{}, err
	}
	modelName = strings.TrimPrefix(modelName, "models/")
	system, contents, err := geminiContents(request.Config.SystemPrompt, request.Messages)
	if err != nil {
		return agent.ModelReply{}, err
	}
	declarations := make([]geminiFunctionDeclaration, 0, len(request.Tools))
	for _, definition := range request.Tools {
		declarations = append(declarations, geminiFunctionDeclaration{
			Name:        string(definition.Name),
			Description: definition.Description,
			Parameters:  json.RawMessage(definition.InputSchema),
		})
	}
	payload := geminiRequest{
		SystemInstruction: geminiContent{Parts: []geminiPart{{Text: system}}},
		Contents:          contents,
	}
	if len(declarations) > 0 {
		payload.Tools = []geminiTool{{FunctionDeclarations: declarations}}
	}
	if request.ForcedTool != "" {
		if !hasDefinition(request.Tools, request.ForcedTool) {
			return agent.ModelReply{}, fmt.Errorf("forced tool %q is not available", request.ForcedTool)
		}
		payload.ToolConfig = &geminiToolConfig{}
		payload.ToolConfig.FunctionCallingConfig.Mode = "ANY"
		payload.ToolConfig.FunctionCallingConfig.AllowedFunctionNames = []string{string(request.ForcedTool)}
	}
	var response geminiResponse
	if err := doProviderJSON(
		ctx,
		client.httpClient,
		"Gemini",
		providerURL(client.baseURL, "models/"+url.PathEscape(modelName)+":generateContent"),
		http.Header{"X-Goog-Api-Key": []string{client.credentials.APIKey(request.FlowID, agent.ProviderGemini)}},
		payload,
		&response,
	); err != nil {
		return agent.ModelReply{}, err
	}
	return consumeGeminiResponse(response, request)
}

// Summarize compacts application messages with Gemini.
func (client *GeminiClient) Summarize(ctx context.Context, request agent.SummarizeRequest) (string, error) {
	model := request.Config.Model
	if request.Config.CompactionModel != nil {
		model = *request.Config.CompactionModel
	}
	provider, err := model.Provider()
	if err != nil {
		return "", err
	}
	if provider != agent.ProviderGemini {
		return "", fmt.Errorf("gemini adapter cannot summarize with provider %q", provider)
	}
	modelName, err := model.ProviderModel()
	if err != nil {
		return "", err
	}
	modelName = strings.TrimPrefix(modelName, "models/")
	transcript, err := json.Marshal(request.Messages)
	if err != nil {
		return "", fmt.Errorf("encode compaction transcript: %w", err)
	}
	var response geminiResponse
	if err := doProviderJSON(
		ctx,
		client.httpClient,
		"Gemini",
		providerURL(client.baseURL, "models/"+url.PathEscape(modelName)+":generateContent"),
		http.Header{"X-Goog-Api-Key": []string{client.credentials.APIKey(request.FlowID, agent.ProviderGemini)}},
		geminiRequest{
			SystemInstruction: geminiContent{Parts: []geminiPart{{Text: compactionInstruction}}},
			Contents: []geminiContent{{Role: geminiRoleUser, Parts: []geminiPart{{
				Text: "Previous summary:\n" + request.PreviousSummary + "\n\nMessages:\n" + string(transcript),
			}}}},
		},
		&response,
	); err != nil {
		return "", err
	}
	if len(response.Candidates) != 1 {
		return "", fmt.Errorf("gemini returned %d candidates, want exactly one", len(response.Candidates))
	}
	var summary strings.Builder
	for _, part := range response.Candidates[0].Content.Parts {
		summary.WriteString(part.Text)
	}
	if strings.TrimSpace(summary.String()) == "" {
		return "", errors.New("gemini compaction response was empty")
	}
	return strings.TrimSpace(summary.String()), nil
}

// CountTokens returns a conservative provider-neutral estimate.
func (*GeminiClient) CountTokens(_ agent.Model, messages []agent.AgentMessage) int {
	return estimatedTokens(messages)
}

type geminiRequest struct {
	SystemInstruction geminiContent     `json:"systemInstruction"`
	Contents          []geminiContent   `json:"contents"`
	Tools             []geminiTool      `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig `json:"toolConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type geminiToolConfig struct {
	FunctionCallingConfig struct {
		Mode                 string   `json:"mode"`
		AllowedFunctionNames []string `json:"allowedFunctionNames"`
	} `json:"functionCallingConfig"`
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
}

func geminiContents(systemPrompt string, messages []agent.AgentMessage) (string, []geminiContent, error) {
	systemParts := []string{systemPrompt}
	result := make([]geminiContent, 0, len(messages))
	for _, message := range withoutOrphanToolOutputs(messages) {
		switch message.Role {
		case agent.MessageRoleSystem:
			systemParts = append(systemParts, message.Content)
		case agent.MessageRoleUser:
			appendGeminiContent(&result, geminiRoleUser, geminiPart{Text: message.Content})
		case agent.MessageRoleAssistant:
			parts := make([]geminiPart, 0, len(message.ToolCalls)+1)
			if message.Content != "" {
				parts = append(parts, geminiPart{Text: message.Content})
			}
			for _, call := range message.ToolCalls {
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{
					Name: string(call.Name),
					Args: json.RawMessage(call.Arguments),
				}})
			}
			if len(parts) > 0 {
				result = append(result, geminiContent{Role: geminiRoleModel, Parts: parts})
			}
		case agent.MessageRoleTool:
			if message.ToolName == nil {
				return "", nil, errors.New("gemini tool message requires a tool name")
			}
			response := json.RawMessage(message.Content)
			if !json.Valid(response) {
				encoded, err := json.Marshal(struct {
					Result string `json:"result"`
				}{message.Content})
				if err != nil {
					return "", nil, fmt.Errorf("encode Gemini tool result: %w", err)
				}
				response = encoded
			}
			appendGeminiContent(&result, geminiRoleUser, geminiPart{FunctionResponse: &geminiFunctionResponse{
				Name:     string(*message.ToolName),
				Response: response,
			}})
		default:
			return "", nil, fmt.Errorf("unsupported Gemini message role %q", message.Role)
		}
	}
	return strings.Join(systemParts, "\n\n"), result, nil
}

func appendGeminiContent(contents *[]geminiContent, role string, part geminiPart) {
	if len(*contents) > 0 && (*contents)[len(*contents)-1].Role == role {
		(*contents)[len(*contents)-1].Parts = append((*contents)[len(*contents)-1].Parts, part)
		return
	}
	*contents = append(*contents, geminiContent{Role: role, Parts: []geminiPart{part}})
}

func consumeGeminiResponse(response geminiResponse, request agent.ModelRequest) (agent.ModelReply, error) {
	if len(response.Candidates) != 1 {
		return agent.ModelReply{}, fmt.Errorf("gemini returned %d candidates, want exactly one", len(response.Candidates))
	}
	var content strings.Builder
	calls := make([]agent.ToolCall, 0)
	for index, part := range response.Candidates[0].Content.Parts {
		if part.Text != "" {
			content.WriteString(part.Text)
			if err := request.WriteAssistant(part.Text); err != nil {
				return agent.ModelReply{}, fmt.Errorf("write assistant text: %w", err)
			}
		}
		if part.FunctionCall == nil {
			continue
		}
		if strings.TrimSpace(part.FunctionCall.Name) == "" {
			return agent.ModelReply{}, errors.New("gemini returned a function call without a name")
		}
		arguments, err := agent.ParseJSONObject(string(part.FunctionCall.Args))
		if err != nil {
			return agent.ModelReply{}, fmt.Errorf("gemini function %q arguments: %w", part.FunctionCall.Name, err)
		}
		seed := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s", request.FlowID, len(request.Messages), index, part.FunctionCall.Name, arguments)
		digest := sha256.Sum256([]byte(seed))
		calls = append(calls, agent.ToolCall{
			ID:        agent.CallID("call-" + hex.EncodeToString(digest[:16])),
			Name:      agent.ToolName(part.FunctionCall.Name),
			Arguments: arguments,
		})
	}
	if err := writeToolActivities(calls, request.WriteActivity); err != nil {
		return agent.ModelReply{}, err
	}
	return agent.ModelReply{Content: content.String(), ToolCalls: calls}, nil
}

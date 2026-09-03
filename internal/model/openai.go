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
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/superdurable/superagent/internal/agent"
)

const compactionInstruction = "Compact the conversation faithfully. Preserve decisions, user preferences, unresolved work, tool outcomes, identifiers, and facts needed by future turns."

// OpenAIClient implements the stateless OpenAI Responses API boundary.
type OpenAIClient struct {
	credentials *CredentialStore
	httpClient  *http.Client
	baseURL     string
}

// NewOpenAIClient constructs an adapter with an explicitly owned HTTP client.
func NewOpenAIClient(credentials *CredentialStore, httpClient *http.Client, baseURL string) *OpenAIClient {
	if credentials == nil {
		panic("OpenAI credential store is required")
	}
	if httpClient == nil {
		panic("OpenAI HTTP client is required")
	}
	return &OpenAIClient{
		credentials: credentials,
		httpClient:  httpClient,
		baseURL:     strings.TrimSpace(baseURL),
	}
}

// Complete streams one stateless Responses API turn.
func (client *OpenAIClient) Complete(ctx context.Context, request agent.ModelRequest) (agent.ModelReply, error) {
	if err := validateModelRequest(request, agent.ProviderOpenAI); err != nil {
		return agent.ModelReply{}, err
	}
	modelName, err := request.Config.Model.ProviderModel()
	if err != nil {
		return agent.ModelReply{}, err
	}
	input, err := openAIInput(request.Messages)
	if err != nil {
		return agent.ModelReply{}, err
	}
	tools, err := openAITools(request.Tools)
	if err != nil {
		return agent.ModelReply{}, err
	}
	params := responses.ResponseNewParams{
		Instructions: openai.String(request.Config.SystemPrompt),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		Model: modelName,
		Include: []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		},
		Reasoning: shared.ReasoningParam{
			Summary: shared.ReasoningSummaryAuto,
		},
		Store:             openai.Bool(false),
		ParallelToolCalls: openai.Bool(false),
		Tools:             tools,
	}
	if request.ForcedTool != "" {
		if !hasDefinition(request.Tools, request.ForcedTool) {
			return agent.ModelReply{}, fmt.Errorf("forced tool %q is not available", request.ForcedTool)
		}
		params.ToolChoice.OfFunctionTool = &responses.ToolChoiceFunctionParam{
			Name: string(request.ForcedTool),
		}
	}

	sdkClient := client.newSDKClient(request.FlowID)
	stream := sdkClient.Responses.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()
	return consumeOpenAIStream(stream, request)
}

// Summarize compacts application history without persisting provider-side state.
func (client *OpenAIClient) Summarize(ctx context.Context, request agent.SummarizeRequest) (string, error) {
	model := request.Config.Model
	if request.Config.CompactionModel != nil {
		model = *request.Config.CompactionModel
	}
	provider, err := model.Provider()
	if err != nil {
		return "", err
	}
	if provider != agent.ProviderOpenAI {
		return "", fmt.Errorf("OpenAI adapter cannot summarize with provider %q", provider)
	}
	modelName, err := model.ProviderModel()
	if err != nil {
		return "", err
	}
	transcript, err := json.Marshal(request.Messages)
	if err != nil {
		return "", fmt.Errorf("encode compaction transcript: %w", err)
	}
	prompt := "Previous summary:\n" + request.PreviousSummary + "\n\nMessages:\n" + string(transcript)
	sdkClient := client.newSDKClient(request.FlowID)
	response, err := sdkClient.Responses.New(ctx, responses.ResponseNewParams{
		Instructions: openai.String(compactionInstruction),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: param.NewOpt(prompt),
		},
		Model: modelName,
		Store: openai.Bool(false),
	})
	if err != nil {
		return "", fmt.Errorf("OpenAI compaction request: %w", err)
	}
	if response.Status != responses.ResponseStatusCompleted {
		return "", newOpenAIResponseError(response)
	}
	summary := strings.TrimSpace(response.OutputText())
	if summary == "" {
		return "", errors.New("OpenAI compaction response was empty")
	}
	return summary, nil
}

// CountTokens returns a conservative local estimate used only to trigger compaction.
func (*OpenAIClient) CountTokens(_ agent.Model, messages []agent.AgentMessage) int {
	return estimatedTokens(messages)
}

func (client *OpenAIClient) newSDKClient(flowID agent.FlowID) openai.Client {
	options := []option.RequestOption{
		option.WithAPIKey(client.credentials.APIKey(flowID, agent.ProviderOpenAI)),
		option.WithHTTPClient(client.httpClient),
		option.WithMaxRetries(0),
	}
	if client.baseURL != "" {
		options = append(options, option.WithBaseURL(client.baseURL))
	}
	return openai.NewClient(options...)
}

func validateModelRequest(request agent.ModelRequest, expected agent.Provider) error {
	provider, err := request.Config.Model.Provider()
	if err != nil {
		return err
	}
	if provider != expected {
		return fmt.Errorf("%s adapter cannot complete with provider %q", expected, provider)
	}
	if request.WriteAssistant == nil || request.WriteReasoning == nil || request.WriteActivity == nil {
		return errors.New("model stream writers are required")
	}
	return nil
}

func openAIInput(messages []agent.AgentMessage) (responses.ResponseInputParam, error) {
	filtered := withoutOrphanToolOutputs(messages)
	result := make(responses.ResponseInputParam, 0, len(filtered))
	for _, message := range filtered {
		if message.Role == agent.MessageRoleTool {
			if message.ToolCallID == nil || strings.TrimSpace(string(*message.ToolCallID)) == "" {
				return nil, errors.New("tool message requires a tool call ID")
			}
			item := responses.ResponseInputItemParamOfFunctionCallOutput(message.Content)
			item.OfFunctionCallOutput.CallID = openai.String(string(*message.ToolCallID))
			result = append(result, item)
			continue
		}
		for _, contextItem := range message.ProviderContextItems {
			if contextItem.Provider != agent.ProviderOpenAI {
				continue
			}
			var item responses.ResponseInputItemUnionParam
			if err := json.Unmarshal([]byte(contextItem.Item), &item); err != nil {
				return nil, fmt.Errorf("decode OpenAI replay item: %w", err)
			}
			if item.OfReasoning == nil {
				return nil, errors.New("OpenAI replay item must be reasoning state")
			}
			result = append(result, item)
		}
		if message.Content != "" {
			role, err := openAIRole(message.Role)
			if err != nil {
				return nil, err
			}
			result = append(result, responses.ResponseInputItemParamOfMessage(message.Content, role))
		}
		for _, call := range message.ToolCalls {
			if _, err := agent.ParseJSONObject(call.Arguments.String()); err != nil {
				return nil, fmt.Errorf("tool call %q arguments: %w", call.ID, err)
			}
			result = append(result, responses.ResponseInputItemParamOfFunctionCall(
				call.Arguments.String(),
				string(call.ID),
				string(call.Name),
			))
		}
	}
	return result, nil
}

func openAIRole(role agent.MessageRole) (responses.EasyInputMessageRole, error) {
	switch role {
	case agent.MessageRoleSystem:
		return responses.EasyInputMessageRoleSystem, nil
	case agent.MessageRoleUser:
		return responses.EasyInputMessageRoleUser, nil
	case agent.MessageRoleAssistant:
		return responses.EasyInputMessageRoleAssistant, nil
	case agent.MessageRoleTool:
		return "", errors.New("tool messages must be encoded as function call outputs")
	default:
		return "", fmt.Errorf("unsupported OpenAI message role %q", role)
	}
}

func openAITools(definitions []agent.ToolDefinition) ([]responses.ToolUnionParam, error) {
	result := make([]responses.ToolUnionParam, 0, len(definitions))
	for _, definition := range definitions {
		var parameters map[string]any
		if err := json.Unmarshal([]byte(definition.InputSchema), &parameters); err != nil {
			return nil, fmt.Errorf("decode tool %q schema: %w", definition.Name, err)
		}
		tool := responses.ToolParamOfFunction(string(definition.Name), parameters, false)
		tool.OfFunction.Description = openai.String(definition.Description)
		result = append(result, tool)
	}
	return result, nil
}

type openAIResponseStream interface {
	Next() bool
	Current() responses.ResponseStreamEventUnion
	Err() error
	Close() error
}

func consumeOpenAIStream(stream openAIResponseStream, request agent.ModelRequest) (agent.ModelReply, error) {
	var content strings.Builder
	toolCalls := make(map[int64]agent.ToolCall)
	providerItems := make([]agent.ProviderContextItem, 0)
	completed := false
	for stream.Next() {
		event := stream.Current()
		switch value := event.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			if err := writeTextDelta(&content, value.Delta, request.WriteAssistant); err != nil {
				return agent.ModelReply{}, err
			}
		case responses.ResponseRefusalDeltaEvent:
			if err := writeTextDelta(&content, value.Delta, request.WriteAssistant); err != nil {
				return agent.ModelReply{}, err
			}
		case responses.ResponseReasoningSummaryTextDeltaEvent:
			if value.Delta != "" {
				if err := request.WriteReasoning(value.Delta); err != nil {
					return agent.ModelReply{}, fmt.Errorf("write reasoning summary: %w", err)
				}
			}
		case responses.ResponseOutputItemDoneEvent:
			switch item := value.Item.AsAny().(type) {
			case responses.ResponseReasoningItem:
				encoded, err := encodeReasoningItem(item)
				if err != nil {
					return agent.ModelReply{}, err
				}
				providerItems = append(providerItems, agent.ProviderContextItem{
					Provider: agent.ProviderOpenAI,
					Item:     encoded,
				})
			case responses.ResponseFunctionToolCall:
				call, err := openAIToolCall(item)
				if err != nil {
					return agent.ModelReply{}, err
				}
				if _, duplicate := toolCalls[value.OutputIndex]; duplicate {
					return agent.ModelReply{}, fmt.Errorf("OpenAI returned duplicate output index %d", value.OutputIndex)
				}
				toolCalls[value.OutputIndex] = call
				if err := writeToolActivities([]agent.ToolCall{call}, request.WriteActivity); err != nil {
					return agent.ModelReply{}, err
				}
			}
		case responses.ResponseCompletedEvent:
			completed = true
		case responses.ResponseFailedEvent:
			return agent.ModelReply{}, newOpenAIResponseError(&value.Response)
		case responses.ResponseIncompleteEvent:
			return agent.ModelReply{}, newOpenAIResponseError(&value.Response)
		case responses.ResponseErrorEvent:
			return agent.ModelReply{}, fmt.Errorf("OpenAI stream error %q", value.Code)
		}
	}
	if err := stream.Err(); err != nil {
		return agent.ModelReply{}, fmt.Errorf("read OpenAI response stream: %w", err)
	}
	if !completed {
		return agent.ModelReply{}, errors.New("OpenAI response stream ended without a completed event")
	}
	ordered := make([]agent.ToolCall, 0, len(toolCalls))
	for index := int64(0); len(ordered) < len(toolCalls); index++ {
		if call, found := toolCalls[index]; found {
			ordered = append(ordered, call)
		}
		if index > int64(len(toolCalls))+1024 {
			return agent.ModelReply{}, errors.New("OpenAI returned invalid sparse tool indexes")
		}
	}
	return agent.ModelReply{
		Content:              content.String(),
		ToolCalls:            ordered,
		ProviderContextItems: providerItems,
	}, nil
}

func writeTextDelta(content *strings.Builder, delta string, write agent.TextWriter) error {
	if delta == "" {
		return nil
	}
	content.WriteString(delta)
	if err := write(delta); err != nil {
		return fmt.Errorf("write assistant text: %w", err)
	}
	return nil
}

func openAIToolCall(item responses.ResponseFunctionToolCall) (agent.ToolCall, error) {
	if strings.TrimSpace(item.CallID) == "" {
		return agent.ToolCall{}, errors.New("OpenAI returned a function call without a call ID")
	}
	if strings.TrimSpace(item.Name) == "" {
		return agent.ToolCall{}, errors.New("OpenAI returned a function call without a name")
	}
	arguments, err := agent.ParseJSONObject(item.Arguments)
	if err != nil {
		return agent.ToolCall{}, fmt.Errorf("OpenAI function %q arguments: %w", item.Name, err)
	}
	return agent.ToolCall{
		ID:        agent.CallID(item.CallID),
		Name:      agent.ToolName(item.Name),
		Arguments: arguments,
	}, nil
}

func encodeReasoningItem(item responses.ResponseReasoningItem) (agent.JSONValue, error) {
	if strings.TrimSpace(item.ID) == "" || !item.JSON.Summary.Valid() || !item.JSON.Type.Valid() {
		return "", errors.New("OpenAI returned incomplete reasoning state")
	}
	type reasoningState struct {
		ID               string                                   `json:"id"`
		Summary          []responses.ResponseReasoningItemSummary `json:"summary"`
		Type             string                                   `json:"type"`
		Content          []responses.ResponseReasoningItemContent `json:"content,omitempty"`
		EncryptedContent string                                   `json:"encrypted_content,omitempty"`
		Status           responses.ResponseReasoningItemStatus    `json:"status,omitempty"`
	}
	encoded, err := json.Marshal(reasoningState{
		ID:               item.ID,
		Summary:          item.Summary,
		Type:             "reasoning",
		Content:          item.Content,
		EncryptedContent: item.EncryptedContent,
		Status:           item.Status,
	})
	if err != nil {
		return "", fmt.Errorf("encode OpenAI reasoning state: %w", err)
	}
	value, err := agent.ParseJSONValue(string(encoded))
	if err != nil {
		return "", fmt.Errorf("validate OpenAI reasoning state: %w", err)
	}
	return value, nil
}

func newOpenAIResponseError(response *responses.Response) error {
	if response == nil {
		return errors.New("OpenAI returned an empty unsuccessful response")
	}
	if response.Error.Code != "" {
		return fmt.Errorf("OpenAI response failed with code %q", response.Error.Code)
	}
	if response.IncompleteDetails.Reason != "" {
		return fmt.Errorf("OpenAI response incomplete: %s", response.IncompleteDetails.Reason)
	}
	return fmt.Errorf("OpenAI response ended with status %q", response.Status)
}

func hasDefinition(definitions []agent.ToolDefinition, name agent.ToolName) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func withoutOrphanToolOutputs(messages []agent.AgentMessage) []agent.AgentMessage {
	knownCalls := make(map[agent.CallID]struct{})
	result := make([]agent.AgentMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == agent.MessageRoleTool {
			if message.ToolCallID == nil {
				continue
			}
			if _, found := knownCalls[*message.ToolCallID]; !found {
				continue
			}
			delete(knownCalls, *message.ToolCallID)
		} else {
			for _, call := range message.ToolCalls {
				knownCalls[call.ID] = struct{}{}
			}
		}
		result = append(result, message)
	}
	return result
}

func estimatedTokens(messages []agent.AgentMessage) int {
	total := 0
	for _, message := range messages {
		bytes := len(message.Content)
		for _, item := range message.ProviderContextItems {
			bytes += len(item.Item)
		}
		count := (bytes + 3) / 4
		if count < 1 {
			count = 1
		}
		total += count
	}
	return total
}

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

	"github.com/superdurable/superagent/internal/agent"
)

// Client routes provider-qualified models to concrete provider adapters.
type Client struct {
	mock      *MockClient
	openAI    *OpenAIClient
	anthropic agent.ModelClient
	gemini    agent.ModelClient
	groq      agent.ModelClient
}

// NewClient constructs the provider router.
func NewClient(
	mock *MockClient,
	openAI *OpenAIClient,
	anthropic agent.ModelClient,
	gemini agent.ModelClient,
	groq agent.ModelClient,
) *Client {
	if mock == nil {
		panic("mock model client is required")
	}
	if openAI == nil {
		panic("OpenAI model client is required")
	}
	return &Client{
		mock:      mock,
		openAI:    openAI,
		anthropic: anthropic,
		gemini:    gemini,
		groq:      groq,
	}
}

// Complete routes one provider-qualified completion.
func (client *Client) Complete(ctx context.Context, request agent.ModelRequest) (agent.ModelReply, error) {
	provider, err := request.Config.Model.Provider()
	if err != nil {
		return agent.ModelReply{}, err
	}
	adapter, err := client.adapter(provider)
	if err != nil {
		return agent.ModelReply{}, err
	}
	return adapter.Complete(ctx, request)
}

// Summarize routes compaction to its explicitly configured provider.
func (client *Client) Summarize(ctx context.Context, request agent.SummarizeRequest) (string, error) {
	model := request.Config.Model
	if request.Config.CompactionModel != nil {
		model = *request.Config.CompactionModel
	}
	provider, err := model.Provider()
	if err != nil {
		return "", err
	}
	adapter, err := client.adapter(provider)
	if err != nil {
		return "", err
	}
	return adapter.Summarize(ctx, request)
}

// CountTokens routes the provider-specific estimate and falls back conservatively.
func (client *Client) CountTokens(model agent.Model, messages []agent.AgentMessage) int {
	provider, err := model.Provider()
	if err != nil {
		return estimatedTokens(messages)
	}
	adapter, err := client.adapter(provider)
	if err != nil {
		return estimatedTokens(messages)
	}
	return adapter.CountTokens(model, messages)
}

func (client *Client) adapter(provider agent.Provider) (agent.ModelClient, error) {
	switch provider {
	case agent.ProviderMock:
		return client.mock, nil
	case agent.ProviderOpenAI:
		return client.openAI, nil
	case agent.ProviderAnthropic:
		return requiredAdapter(provider, client.anthropic)
	case agent.ProviderGemini:
		return requiredAdapter(provider, client.gemini)
	case agent.ProviderGroq:
		return requiredAdapter(provider, client.groq)
	default:
		return nil, &agent.EnumValidationError{Type: "Provider", Value: string(provider)}
	}
}

func requiredAdapter(provider agent.Provider, adapter agent.ModelClient) (agent.ModelClient, error) {
	if adapter == nil {
		return nil, fmt.Errorf("provider %q is not configured in this process", provider)
	}
	return adapter, nil
}

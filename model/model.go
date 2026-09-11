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

// Package model exposes SuperAgent's provider router, provider adapters, and
// in-memory credential store to embedding applications.
package model

import (
	"net/http"

	"github.com/superdurable/superagent/agent"
	modelinternal "github.com/superdurable/superagent/internal/model"
)

// Public aliases expose the existing provider implementations without a
// parallel model abstraction or conversion layer.
type (
	Client          = modelinternal.Client
	CredentialStore = modelinternal.CredentialStore
	MockClient      = modelinternal.MockClient
	OpenAIClient    = modelinternal.OpenAIClient
	AnthropicClient = modelinternal.AnthropicClient
	GeminiClient    = modelinternal.GeminiClient
	GroqClient      = modelinternal.GroqClient
)

// NewCredentialStore creates an empty process-memory credential store.
func NewCredentialStore() *CredentialStore {
	return modelinternal.NewCredentialStore()
}

// NewClient constructs the provider-qualified model router.
func NewClient(
	mock *MockClient,
	openAI *OpenAIClient,
	anthropic agent.ModelClient,
	gemini agent.ModelClient,
	groq agent.ModelClient,
) *Client {
	return modelinternal.NewClient(mock, openAI, anthropic, gemini, groq)
}

// NewMockClient creates the deterministic credential-free provider.
func NewMockClient() *MockClient {
	return modelinternal.NewMockClient()
}

// NewOpenAIClient constructs an OpenAI Responses API adapter.
func NewOpenAIClient(
	credentials *CredentialStore,
	httpClient *http.Client,
	baseURL string,
) *OpenAIClient {
	return modelinternal.NewOpenAIClient(credentials, httpClient, baseURL)
}

// NewAnthropicClient constructs an Anthropic Messages API adapter.
func NewAnthropicClient(
	credentials *CredentialStore,
	httpClient *http.Client,
	baseURL string,
) (*AnthropicClient, error) {
	return modelinternal.NewAnthropicClient(credentials, httpClient, baseURL)
}

// NewGeminiClient constructs a Gemini generateContent adapter.
func NewGeminiClient(
	credentials *CredentialStore,
	httpClient *http.Client,
	baseURL string,
) (*GeminiClient, error) {
	return modelinternal.NewGeminiClient(credentials, httpClient, baseURL)
}

// NewGroqClient constructs a Groq chat-completions adapter.
func NewGroqClient(
	credentials *CredentialStore,
	httpClient *http.Client,
	baseURL string,
) (*GroqClient, error) {
	return modelinternal.NewGroqClient(credentials, httpClient, baseURL)
}

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

package model_test

import (
	"net/http"
	"testing"

	"github.com/superdurable/superagent/agent"
	"github.com/superdurable/superagent/model"
)

func TestPublicProviderFacadeConstructsExistingRouter(t *testing.T) {
	t.Parallel()

	credentials := model.NewCredentialStore()
	if err := credentials.SetDefaultAPIKey(agent.ProviderOpenAI, "process-key"); err != nil {
		t.Fatalf("set default credential: %v", err)
	}
	flowID := agent.FlowID("agent-flow-1")
	if err := credentials.SetAPIKey(flowID, agent.ProviderOpenAI, "flow-key"); err != nil {
		t.Fatalf("set Flow credential: %v", err)
	}
	if got := credentials.APIKey(flowID, agent.ProviderOpenAI); got != "flow-key" {
		t.Fatalf("Flow credential = %q, want flow-key", got)
	}

	httpClient := &http.Client{}
	anthropic, err := model.NewAnthropicClient(credentials, httpClient, "")
	if err != nil {
		t.Fatalf("construct Anthropic adapter: %v", err)
	}
	gemini, err := model.NewGeminiClient(credentials, httpClient, "")
	if err != nil {
		t.Fatalf("construct Gemini adapter: %v", err)
	}
	groq, err := model.NewGroqClient(credentials, httpClient, "")
	if err != nil {
		t.Fatalf("construct Groq adapter: %v", err)
	}
	router := model.NewClient(
		model.NewMockClient(),
		model.NewOpenAIClient(credentials, httpClient, ""),
		anthropic,
		gemini,
		groq,
	)
	var _ agent.ModelClient = router
	if got := router.CountTokens(
		agent.Model("mock/test"),
		[]agent.AgentMessage{{Content: "test"}},
	); got != 1 {
		t.Fatalf("mock token count = %d, want 1", got)
	}
}

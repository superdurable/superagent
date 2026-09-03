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
	"testing"

	"github.com/superdurable/superagent/internal/agent"
)

func TestCredentialStoreScopesKeysByFlowAndProvider(t *testing.T) {
	store := NewCredentialStore()
	flowID := agent.FlowID("flow-1")
	if err := store.SetAPIKey(flowID, agent.ProviderOpenAI, "openai-key"); err != nil {
		t.Fatalf("SetAPIKey() error = %v", err)
	}
	if err := store.SetAPIKey(flowID, agent.ProviderAnthropic, "anthropic-key"); err != nil {
		t.Fatalf("SetAPIKey() error = %v", err)
	}
	if got := store.APIKey(flowID, agent.ProviderOpenAI); got != "openai-key" {
		t.Fatalf("OpenAI APIKey() = %q", got)
	}
	if got := store.APIKey(flowID, agent.ProviderAnthropic); got != "anthropic-key" {
		t.Fatalf("Anthropic APIKey() = %q", got)
	}
	store.DeleteFlow(flowID)
	if got := store.APIKey(flowID, agent.ProviderOpenAI); got != "" {
		t.Fatalf("APIKey() after DeleteFlow() = %q", got)
	}
}

func TestCredentialStoreRejectsControlCharacters(t *testing.T) {
	store := NewCredentialStore()
	err := store.SetAPIKey(agent.FlowID("flow-1"), agent.ProviderOpenAI, "key\nvalue")
	if err == nil {
		t.Fatal("SetAPIKey() error = nil")
	}
}

func TestCredentialStoreFallsBackToDefaultAfterFlowDeletion(t *testing.T) {
	t.Parallel()
	store := NewCredentialStore()
	if err := store.SetDefaultAPIKey(agent.ProviderOpenAI, "default-key"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAPIKey("flow-1", agent.ProviderOpenAI, "flow-key"); err != nil {
		t.Fatal(err)
	}
	if got := store.APIKey("flow-1", agent.ProviderOpenAI); got != "flow-key" {
		t.Fatalf("Flow key = %q", got)
	}
	store.DeleteFlow("flow-1")
	if got := store.APIKey("flow-1", agent.ProviderOpenAI); got != "default-key" {
		t.Fatalf("default key = %q", got)
	}
}

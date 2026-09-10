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

package agent

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestAgentConfigDefaultsValidate(t *testing.T) {
	config := NewAgentConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if config.Model != DefaultModel || config.MaxContextTokens != 32_000 || config.MessageRetentionLimit != 1_000 {
		t.Fatalf("unexpected defaults: %+v", config)
	}
}

func TestAgentConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AgentConfig)
	}{
		{"empty model", func(config *AgentConfig) { config.Model = " " }},
		{"empty prompt", func(config *AgentConfig) { config.SystemPrompt = "" }},
		{"zero context", func(config *AgentConfig) { config.MaxContextTokens = 0 }},
		{"inverted fractions", func(config *AgentConfig) { config.CompactionKeepFraction = 0.9 }},
		{"NaN fraction", func(config *AgentConfig) { config.CompactionKeepFraction = math.NaN() }},
		{"zero retention", func(config *AgentConfig) { config.MessageRetentionLimit = 0 }},
		{"retention below current window", func(config *AgentConfig) { config.MessageRetentionLimit = 10 }},
		{"retention not chunk aligned", func(config *AgentConfig) { config.MessageRetentionLimit = 25 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := NewAgentConfig()
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestEnumsRejectUnknownJSONWithTypedError(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		target   json.Unmarshaler
	}{
		{name: "Agent status", typeName: "AgentStatus", target: new(AgentStatus)},
		{name: "Agent interaction status", typeName: "AgentInteractionStatus", target: new(AgentInteractionStatus)},
		{name: "Flow status", typeName: "FlowStatus", target: new(FlowStatus)},
		{name: "Flow error type", typeName: "FlowErrorType", target: new(FlowErrorType)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.target.UnmarshalJSON([]byte(`"unknown"`))
			var validationErr *EnumValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("UnmarshalJSON() error = %T %v", err, err)
			}
			if validationErr.Type != test.typeName || validationErr.Value != "unknown" {
				t.Fatalf("validation error = %+v", validationErr)
			}
		})
	}
}

func TestVisibilityStringQuotesApostrophes(t *testing.T) {
	if got := visibilityString("customer's-flow"); got != `'customer''s-flow'` {
		t.Fatalf("visibilityString() = %q", got)
	}
}

func TestModelRequiresSupportedProviderPrefix(t *testing.T) {
	tests := []struct {
		model    Model
		provider Provider
		hasError bool
	}{
		{model: "mock/dex", provider: ProviderMock},
		{model: "openai/gpt-5-mini", provider: ProviderOpenAI},
		{model: "missing-prefix", hasError: true},
		{model: "unknown/model", hasError: true},
	}
	for _, test := range tests {
		t.Run(string(test.model), func(t *testing.T) {
			provider, err := test.model.Provider()
			if test.hasError {
				if err == nil {
					t.Fatal("Provider() error = nil")
				}
				return
			}
			if err != nil || provider != test.provider {
				t.Fatalf("Provider() = %q, %v", provider, err)
			}
		})
	}
}

func TestJSONObjectRoundTripsAsObject(t *testing.T) {
	object, err := ParseJSONObject(` {"value":1} `)
	if err != nil {
		t.Fatalf("ParseJSONObject() error = %v", err)
	}
	encoded, err := json.Marshal(struct {
		Value JSONObject `json:"value"`
	}{Value: object})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(encoded) != `{"value":{"value":1}}` {
		t.Fatalf("Marshal() = %s", encoded)
	}
	if _, err := ParseJSONObject(`[]`); err == nil {
		t.Fatal("ParseJSONObject(array) error = nil")
	}
}

func TestNewUserMessageValidationProtectsCallerIdentityAndBodyBounds(t *testing.T) {
	t.Parallel()
	valid := UserMessage{MessageID: "message-1", Content: "hello"}
	if err := validateNewUserMessage(valid); err != nil {
		t.Fatalf("valid message: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*UserMessage)
	}{
		{name: "missing ID", mutate: func(message *UserMessage) { message.MessageID = "" }},
		{name: "unsafe ID", mutate: func(message *UserMessage) { message.MessageID = "message id" }},
		{name: "accepted timestamp", mutate: func(message *UserMessage) { message.AcceptedAt = time.Now() }},
		{name: "blank content", mutate: func(message *UserMessage) { message.Content = " \n" }},
		{name: "NUL content", mutate: func(message *UserMessage) { message.Content = "hello\x00world" }},
		{name: "oversized content", mutate: func(message *UserMessage) {
			message.Content = strings.Repeat("x", maximumUserMessageBytes+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := validateNewUserMessage(candidate); err == nil {
				t.Fatal("validation error = nil")
			}
		})
	}
}

func TestRequestAndApplicationContextValidation(t *testing.T) {
	t.Parallel()
	if err := validateRequestID("request-1/path:part"); err != nil {
		t.Fatalf("valid request ID: %v", err)
	}
	for _, requestID := range []RequestID{"", "request id", RequestID(strings.Repeat("x", maximumMessageIDBytes+1))} {
		if err := validateRequestID(requestID); err == nil {
			t.Fatalf("request ID %q was accepted", requestID)
		}
	}
	if err := validateApplicationContext(`{"sandbox_id":"sandbox-1"}`); err != nil {
		t.Fatalf("valid application context: %v", err)
	}
	for _, value := range []string{"context\x00value", strings.Repeat("x", maximumAppContextBytes+1)} {
		if err := validateApplicationContext(value); err == nil {
			t.Fatalf("application context of %d bytes was accepted", len(value))
		}
	}
}

func TestEnsureStartIdentityFingerprintIsGlobal(t *testing.T) {
	t.Parallel()
	request := EnsureStartRequest{
		RequestID:          "request-one",
		Config:             NewAgentConfig(),
		ApplicationContext: `{"workspace_id":"workspace-1"}`,
		InitialMessage: &UserMessage{
			MessageID: "message-one",
			Content:   "build the application",
			PlanMode:  true,
		},
	}
	fingerprint, err := request.identityFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID = "request-two"
	retriedFingerprint, err := request.identityFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if retriedFingerprint != fingerprint {
		t.Fatal("request ID changed the global Agent start identity")
	}
	request.Config.EnabledMCPServers = nil
	request.Config.EnabledTools = nil
	collectionFingerprint, err := request.identityFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if collectionFingerprint != fingerprint {
		t.Fatal("nil configuration collections changed the global Agent start identity")
	}
	request.Config.EnabledMCPServers = []string{}
	request.Config.EnabledTools = []ToolName{}

	mutations := []func(*EnsureStartRequest){
		func(candidate *EnsureStartRequest) { candidate.InitialMessage = nil },
		func(candidate *EnsureStartRequest) { candidate.InitialMessage.MessageID = "message-two" },
		func(candidate *EnsureStartRequest) { candidate.InitialMessage.Content = "build something else" },
		func(candidate *EnsureStartRequest) { candidate.InitialMessage.PlanMode = false },
	}
	for index, mutate := range mutations {
		candidate := request
		initialMessage := *request.InitialMessage
		candidate.InitialMessage = &initialMessage
		mutate(&candidate)
		candidateFingerprint, fingerprintErr := candidate.identityFingerprint()
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		if candidateFingerprint == fingerprint {
			t.Fatalf("identity mutation %d retained the original fingerprint", index)
		}
	}
}

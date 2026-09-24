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
	if config.Model != DefaultModel || config.MaxContextTokens != 32_000 ||
		config.MessageRetentionLimit != 1_000 || config.MaxParallelToolCalls != 4 {
		t.Fatalf("unexpected defaults: %+v", config)
	}
	config.MaxParallelToolCalls = 0
	if config.EffectiveMaxParallelToolCalls() != DefaultMaxParallelToolCalls {
		t.Fatalf("zero-value parallel limit = %d", config.EffectiveMaxParallelToolCalls())
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
		{"negative parallel calls", func(config *AgentConfig) { config.MaxParallelToolCalls = -1 }},
		{"too many parallel calls", func(config *AgentConfig) { config.MaxParallelToolCalls = 33 }},
		{"negative inactivity timeout", func(config *AgentConfig) { config.InactivityTimeoutSeconds = -1 }},
		{"excessive inactivity timeout", func(config *AgentConfig) {
			config.InactivityTimeoutSeconds = int64((366 * 24 * time.Hour) / time.Second)
		}},
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

func TestNextWaitingInputRoundProtectsJavaScriptSafeIntegerRange(t *testing.T) {
	t.Parallel()
	next, err := nextWaitingInputRound(0)
	if err != nil || next != 1 {
		t.Fatalf("next waiting input round = %d, %v", next, err)
	}
	if _, err := nextWaitingInputRound(MaximumWaitingInputRound); err == nil {
		t.Fatal("maximum waiting input round did not fail")
	}
	if _, err := nextWaitingInputRound(-1); err == nil {
		t.Fatal("negative waiting input round did not fail")
	}
}

func TestAnsweredQuestionsDescriptionDoesNotExposeAnswers(t *testing.T) {
	t.Parallel()
	if got := answeredQuestionsDescription(1); got != "Answered 1 question." {
		t.Fatalf("single answer description = %q", got)
	}
	if got := answeredQuestionsDescription(3); got != "Answered 3 questions." {
		t.Fatalf("multiple answer description = %q", got)
	}
}

func TestTimingFieldsRemainBackwardCompatibleWithLegacyJSON(t *testing.T) {
	t.Parallel()
	var message AgentMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"done","tool_calls":[],"created_at":"2026-09-23T12:00:01Z"}`), &message); err != nil {
		t.Fatal(err)
	}
	if message.StartedAt != nil {
		t.Fatalf("legacy message started at = %v", message.StartedAt)
	}
	if message.AnsweredInputCallID != nil {
		t.Fatalf("legacy message answered input call ID = %v", message.AnsweredInputCallID)
	}
	var pending PendingApproval
	if err := json.Unmarshal([]byte(`{"call_id":"call-1","tool_name":"read_file","arguments":{"path":"README.md"}}`), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.StartedAt != nil {
		t.Fatalf("legacy pending approval started at = %v", pending.StartedAt)
	}

	startedAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	message.StartedAt = &startedAt
	answeredInputCallID := CallID("call-1")
	message.AnsweredInputCallID = &answeredInputCallID
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip AgentMessage
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.StartedAt == nil || !roundTrip.StartedAt.Equal(startedAt) {
		t.Fatalf("round-trip message started at = %v", roundTrip.StartedAt)
	}
	if roundTrip.AnsweredInputCallID == nil || *roundTrip.AnsweredInputCallID != answeredInputCallID {
		t.Fatalf("round-trip answered input call ID = %v", roundTrip.AnsweredInputCallID)
	}
}

func TestEnumsRejectUnknownJSONWithTypedError(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		target   json.Unmarshaler
	}{
		{name: "Agent status", typeName: "AgentStatus", target: new(AgentStatus)},
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

func TestRuntimeMetadataValidation(t *testing.T) {
	if err := validateRuntimeMetadata(""); err != nil {
		t.Fatalf("empty runtime metadata error = %v", err)
	}
	if err := validateRuntimeMetadata(MustJSONObject(`{"resource_id":"resource-1"}`)); err != nil {
		t.Fatalf("valid runtime metadata error = %v", err)
	}
	if err := validateRuntimeMetadata(JSONObject(`[]`)); err == nil {
		t.Fatal("array runtime metadata error = nil")
	}
	oversized := JSONObject(`{"value":"` + strings.Repeat("x", MaximumRuntimeMetadataBytes) + `"}`)
	if err := validateRuntimeMetadata(oversized); err == nil {
		t.Fatal("oversized runtime metadata error = nil")
	}
}

func TestUserMessageInputLimit(t *testing.T) {
	if err := validateNewUserMessage(UserMessage{Content: strings.Repeat("x", MaximumUserMessageContentBytes)}); err != nil {
		t.Fatalf("maximum user message error = %v", err)
	}
	if err := validateNewUserMessage(UserMessage{Content: strings.Repeat("x", MaximumUserMessageContentBytes+1)}); err == nil {
		t.Fatal("oversized user message error = nil")
	}
}

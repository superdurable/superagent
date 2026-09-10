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
			message.Content = strings.Repeat("x", MaximumUserMessageContentBytes+1)
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

func TestUserMessageContentAllowsExactly256KiB(t *testing.T) {
	t.Parallel()
	message := UserMessage{
		MessageID: "message-at-limit",
		Content:   strings.Repeat("x", MaximumUserMessageContentBytes),
	}
	if err := validateNewUserMessage(message); err != nil {
		t.Fatalf("message at content limit: %v", err)
	}
	message.Content += "x"
	if err := validateNewUserMessage(message); err == nil {
		t.Fatal("message beyond content limit was accepted")
	}
}

func TestMutationPreconditionAndCancellationValidation(t *testing.T) {
	t.Parallel()
	negative := MutationRevision(-1)
	if err := validateExpectedRevision(&negative); err == nil {
		t.Fatal("negative expected revision was accepted")
	}
	zero := MutationRevision(0)
	if err := validateExpectedRevision(&zero); err != nil {
		t.Fatalf("zero expected revision: %v", err)
	}
	valid := CancelRequest{RequestID: "cancel-1", Reason: "requested by caller"}
	if err := validateCancelRequest(valid); err != nil {
		t.Fatalf("valid cancellation: %v", err)
	}
	for _, candidate := range []CancelRequest{
		{Reason: valid.Reason},
		{RequestID: valid.RequestID},
		{RequestID: valid.RequestID, Reason: "reason\x00value"},
		{RequestID: valid.RequestID, Reason: strings.Repeat("x", maximumCancelReasonBytes+1)},
	} {
		if err := validateCancelRequest(candidate); err == nil {
			t.Fatalf("invalid cancellation was accepted: %#v", candidate)
		}
	}
}

func TestAnswerQuestionsValidationAndFingerprint(t *testing.T) {
	t.Parallel()
	valid := AnswerQuestionsRequest{
		RequestID: "answer-request-1",
		MessageID: "answer-message-1",
		CallID:    "call-1",
		Answers: []UserInputAnswer{{
			QuestionID: "environment",
			Answer:     "Production",
		}},
	}
	if err := validateAnswerQuestionsRequest(valid); err != nil {
		t.Fatalf("valid answer request: %v", err)
	}
	fingerprint, err := valid.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*AnswerQuestionsRequest)
	}{
		{name: "missing request ID", mutate: func(request *AnswerQuestionsRequest) { request.RequestID = "" }},
		{name: "missing message ID", mutate: func(request *AnswerQuestionsRequest) { request.MessageID = "" }},
		{name: "missing call ID", mutate: func(request *AnswerQuestionsRequest) { request.CallID = "" }},
		{name: "missing answers", mutate: func(request *AnswerQuestionsRequest) { request.Answers = nil }},
		{name: "blank answer", mutate: func(request *AnswerQuestionsRequest) { request.Answers[0].Answer = " " }},
		{name: "duplicate question", mutate: func(request *AnswerQuestionsRequest) {
			request.Answers = append(request.Answers, request.Answers[0])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Answers = append([]UserInputAnswer(nil), valid.Answers...)
			test.mutate(&candidate)
			if validationErr := validateAnswerQuestionsRequest(candidate); validationErr == nil {
				t.Fatal("validation error = nil")
			}
		})
	}
	changed := valid
	changed.Answers = append([]UserInputAnswer(nil), valid.Answers...)
	changed.Answers[0].Answer = "Staging"
	changedFingerprint, err := changed.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changedFingerprint == fingerprint {
		t.Fatal("answer mutation did not change the request fingerprint")
	}
}

func TestCancelFingerprintIncludesReason(t *testing.T) {
	t.Parallel()
	request := CancelRequest{RequestID: "cancel-1", Reason: "first reason"}
	fingerprint, err := request.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	request.Reason = "second reason"
	changed, err := request.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changed == fingerprint {
		t.Fatal("cancellation reason did not change the fingerprint")
	}
	flowID := FlowID("cancel-reservation-flow")
	if cancellationReservationStartRequestID(flowID, changed) ==
		cancellationReservationStartRequestID(flowID, fingerprint) {
		t.Fatal("cancellation reason did not change the reservation start request ID")
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
	if err := validateApplicationContext(`{"resource_id":"resource-1"}`); err != nil {
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
		ApplicationContext: `{"resource_id":"resource-1"}`,
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

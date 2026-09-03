//go:build live

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
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/superdurable/superagent/internal/agent"
)

func TestLiveOpenAIResponses(t *testing.T) {
	apiKey, err := loadDotEnvValue("../../.env", "OPENAI_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	credentials := NewCredentialStore()
	if err := credentials.SetDefaultAPIKey(agent.ProviderOpenAI, apiKey); err != nil {
		t.Fatal(err)
	}
	client := NewOpenAIClient(credentials, &http.Client{Timeout: 2 * time.Minute}, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var streamed strings.Builder
	config := agent.NewAgentConfig()
	config.Model = "openai/gpt-5-mini"
	config.SystemPrompt = "Answer concisely and accurately."
	reply, err := client.Complete(ctx, agent.ModelRequest{
		Config: config,
		Messages: []agent.AgentMessage{{
			Role:    agent.MessageRoleUser,
			Content: "Reply with one short sentence confirming that the Responses API request succeeded.",
		}},
		WriteAssistant: func(delta string) error {
			streamed.WriteString(delta)
			return nil
		},
		WriteReasoning: func(string) error { return nil },
		WriteActivity:  func(agent.AgentEvent) error { return nil },
		FlowID:         "live-openai-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(reply.Content) == "" || reply.Content != streamed.String() {
		t.Fatal("OpenAI response was empty or differed from streamed content")
	}
}

func loadDotEnvValue(path string, name string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !found || strings.TrimSpace(key) != name {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') ||
			(value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if value == "" {
			return "", errors.New(name + " is empty in .env")
		}
		return value, nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New(name + " is missing from .env")
}

//go:build integration

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

package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	transportapi "github.com/superdurable/superagent/internal/api/generated"
	"github.com/superdurable/superagent/internal/app"
	"github.com/superdurable/superagent/internal/config"
)

func TestAgentHTTPServerIntegration(t *testing.T) {
	flowServiceAddress := os.Getenv("DEX_FLOW_SERVICE_ADDRESS")
	if flowServiceAddress == "" {
		flowServiceAddress = "127.0.0.1:8801"
	}
	httpAddress := availableAddress(t)
	workerAddress := availableAddress(t)
	applicationConfig := &config.Config{
		HTTP: &config.HTTP{
			Address: httpAddress, ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout: 75 * time.Second, ShutdownTimeout: 10 * time.Second,
		},
		Dex: &config.Dex{
			FlowServiceAddress: flowServiceAddress,
			WorkerBindAddress:  workerAddress,
			WorkerTarget:       workerAddress,
		},
		BlobCache: &config.BlobCache{Directory: t.TempDir(), MaxBytes: 64 << 20},
		MCP:       &config.MCP{},
		Providers: &config.Providers{
			OpenAI: &config.Provider{}, Anthropic: &config.Provider{},
			Gemini: &config.Provider{}, Groq: &config.Provider{},
			RequestTimeout: 10 * time.Second,
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- app.Run(ctx, applicationConfig, slog.New(slog.DiscardHandler))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-result:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("stop application: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("application did not stop")
		}
	})

	baseURL := "http://" + httpAddress
	waitForReady(t, baseURL)
	flowID := "http-integration-" + randomID(t)
	startBody := &transportapi.StartAgentRequest{
		FlowId: transportapi.FlowID(flowID), Provider: transportapi.ProviderMock, Model: "mock/dex",
		SystemPrompt: "HTTP integration", MaxContextTokens: 32000,
		MessageRetentionLimit: 20, McpEnabled: false,
		EnabledMcpServers: []string{}, EnabledTools: []transportapi.ToolName{},
	}
	requestJSON(t, http.MethodPost, baseURL+"/products/ai-agent/start", startBody, http.StatusCreated, nil)

	waitURL := fmt.Sprintf(
		"%s/products/ai-agent/interaction-status?flowId=%s&expectedStatus=waiting",
		baseURL, flowID,
	)
	var waiting transportapi.AgentInteractionState
	requestJSON(t, http.MethodGet, waitURL, nil, http.StatusOK, &waiting)
	if waiting.Status != transportapi.AgentInteractionStatusWaiting {
		t.Fatalf("initial interaction status = %q", waiting.Status)
	}

	submitted := make(chan error, 1)
	go func() {
		url := fmt.Sprintf(
			"%s/products/ai-agent/interaction-status?flowId=%s&expectedStatus=submitted",
			baseURL, flowID,
		)
		submitted <- requestJSONError(http.MethodGet, url, nil, http.StatusOK, nil)
	}()
	requestJSON(t, http.MethodPost, baseURL+"/products/ai-agent/messages", &transportapi.SendMessageRequest{
		FlowId: transportapi.FlowID(flowID), Content: "through HTTP", PlanMode: false,
	}, http.StatusAccepted, nil)
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
	requestJSON(t, http.MethodGet, waitURL, nil, http.StatusOK, &waiting)

	var snapshot transportapi.AgentSnapshot
	snapshotURL := fmt.Sprintf("%s/products/ai-agent/snapshot?flowId=%s", baseURL, flowID)
	requestJSON(t, http.MethodGet, snapshotURL, nil, http.StatusOK, &snapshot)
	if snapshot.Description.IsNull() || len(snapshot.History.Messages) != 2 {
		t.Fatalf("Snapshot = %#v", snapshot)
	}
	if snapshot.History.Messages[0].Message.Content != "through HTTP" ||
		snapshot.History.Messages[1].Message.Content != "Local demo response: through HTTP" {
		t.Fatalf("renderable history = %#v", snapshot.History.Messages)
	}

	for index := 2; index <= 10; index++ {
		content := fmt.Sprintf("HTTP archive %02d", index)
		requestJSON(t, http.MethodPost, baseURL+"/products/ai-agent/messages", &transportapi.SendMessageRequest{
			FlowId: transportapi.FlowID(flowID), Content: content, PlanMode: false,
		}, http.StatusAccepted, nil)
		requestJSON(t, http.MethodGet, waitURL, nil, http.StatusOK, &waiting)
	}
	requestJSON(t, http.MethodGet, snapshotURL, nil, http.StatusOK, &snapshot)
	if len(snapshot.History.Messages) != 10 || snapshot.History.Messages[0].Sequence != 11 ||
		snapshot.History.NextBeforeSequence.Or(0) != 11 {
		t.Fatalf("current Snapshot history = %#v", snapshot.History)
	}
	var archive transportapi.HistoryPage
	archiveURL := fmt.Sprintf(
		"%s/products/ai-agent/archived-messages?flowId=%s&beforeSequence=11",
		baseURL, flowID,
	)
	requestJSON(t, http.MethodGet, archiveURL, nil, http.StatusOK, &archive)
	if len(archive.Messages) != 10 || archive.Messages[0].Sequence != 1 || archive.Messages[9].Sequence != 10 {
		t.Fatalf("archived history = %#v", archive)
	}
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitForReady(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/readyz") //nolint:noctx // Bounded by the test deadline.
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			t.Fatalf("wait for HTTP server: %v", t.Context().Err())
		}
	}
	t.Fatal("HTTP server did not become ready")
}

func requestJSON(
	t *testing.T,
	method string,
	url string,
	body json.Marshaler,
	wantStatus int,
	destination json.Unmarshaler,
) {
	t.Helper()
	if err := requestJSONError(method, url, body, wantStatus, destination); err != nil {
		t.Fatal(err)
	}
}

func requestJSONError(
	method string,
	url string,
	body json.Marshaler,
	wantStatus int,
	destination json.Unmarshaler,
) error {
	var content io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		content = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, url, content)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != wantStatus {
		return fmt.Errorf("%s %s status = %d, body = %s", method, url, response.StatusCode, encoded)
	}
	if destination != nil {
		return destination.UnmarshalJSON(encoded)
	}
	return nil
}

func randomID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

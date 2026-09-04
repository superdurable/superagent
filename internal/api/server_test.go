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

package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/superdurable/superagent/internal/agent"
	transportapi "github.com/superdurable/superagent/internal/api/generated"
	"github.com/superdurable/superagent/internal/config"
)

func TestHTTPHandlerDoesNotServeFrontendRoutes(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(
		newTestHandler(&fakeAgentService{}, fakeCredentials{}),
		&config.HTTP{},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/products/ai-agent/", "/bundle.js", "/config.json"} {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}
}

func TestPortalEncodesEmptyMCPArrays(t *testing.T) {
	t.Parallel()
	apiHandler := NewHandler(
		&fakeAgentService{},
		emptyToolCatalog{},
		fakeCredentials{},
		func() bool { return true },
		slog.New(slog.DiscardHandler),
	)
	handler, err := NewHTTPHandler(apiHandler, &config.HTTP{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/products/ai-agent/portal",
		nil,
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var portal transportapi.Portal
	if err := portal.UnmarshalJSON(response.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	if portal.McpServers == nil || portal.Tools == nil {
		t.Fatalf("MCP arrays must encode as arrays: %#v", portal)
	}
}

func TestSnapshotResponseCannotBeCached(t *testing.T) {
	t.Parallel()
	service := &fakeAgentService{snapshot: agent.AgentSnapshot{
		RunID:      "run-1",
		FlowStatus: agent.FlowStatusRunning,
		History: agent.HistoryPage{
			Messages: []agent.SequencedMessage{},
		},
		Description: &agent.AgentDescription{
			Status:              agent.AgentStatusInitializing,
			Model:               "mock/reliable",
			AvailableMCPServers: []string{},
			AvailableTools:      []agent.ToolName{},
		},
		Queued:  []agent.PendingUserMessage{},
		Steered: []agent.PendingUserMessage{},
	}}
	apiHandler := newTestHandler(service, fakeCredentials{})
	directResponse, err := apiHandler.GetAgentSnapshot(context.Background(), transportapi.GetAgentSnapshotParams{
		FlowId: "flow-1",
		Limit:  transportapi.NewOptInt(50),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshotResponse, ok := directResponse.(*transportapi.AgentSnapshotHeaders)
	if !ok {
		t.Fatalf("direct response type = %T", directResponse)
	}
	if validationErr := snapshotResponse.Validate(); validationErr != nil {
		t.Fatalf("validate direct response: %v", validationErr)
	}
	handler, err := NewHTTPHandler(
		apiHandler,
		&config.HTTP{},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/products/ai-agent/snapshot?flowId=flow-1",
		nil,
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q", cacheControl)
	}
}

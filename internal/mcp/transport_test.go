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

package mcpregistry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/superdurable/superagent/internal/agent"
)

const (
	stdioHelperDirectory       = "SUPERAGENT_MCP_HELPER_DIRECTORY"
	stdioHelperDirectorySource = "SUPERAGENT_TEST_MCP_HELPER_DIRECTORY"
)

type fixtureToolInput struct {
	Value string `json:"value"`
}

type fixtureToolOutput struct {
	Echo string `json:"echo"`
}

type fixtureToolHandler = mcpsdk.ToolHandlerFor[fixtureToolInput, fixtureToolOutput]

func TestMain(m *testing.M) {
	directory := os.Getenv(stdioHelperDirectory)
	if directory != "" {
		os.Exit(runStdioHelper(directory))
	}
	os.Exit(m.Run())
}

func TestStreamableHTTPDiscoversEveryPageAndExecutesWithHeaders(t *testing.T) {
	const authorization = "Bearer integration-token"
	t.Setenv("SUPERAGENT_TEST_MCP_AUTHORIZATION", authorization)

	server := newFixtureServer(1, echoFixtureTool)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "alpha"}, echoFixtureTool)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "beta"}, echoFixtureTool)
	handler := mcpsdk.NewStreamableHTTPHandler(func(request *http.Request) *mcpsdk.Server {
		if request.Header.Get("Authorization") != authorization {
			t.Errorf("Authorization header was not injected")
		}
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	registry := newHTTPFixtureRegistry(t, httpServer.URL, map[string]ToolPolicy{})
	if startErr := registry.Start(t.Context()); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(registry.Close)
	if tools := registry.RegisteredTools(); len(tools) != 3 {
		t.Fatalf("discovered tools = %d, want 3", len(tools))
	}

	progress := make([]string, 0, 1)
	result, err := registry.Execute(t.Context(), agent.ToolInvocation{
		Name:      "fixture__echo",
		Arguments: agent.MustJSONObject(`{"value":"hello"}`),
		CallID:    "call-http",
		WriteProgress: func(message string) error {
			progress = append(progress, message)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Outcome != agent.ToolOutcomeSucceeded || !strings.Contains(result.Content, `"echo":"hello"`) {
		t.Fatalf("tool result = %+v", result)
	}
	if len(progress) != 1 || progress[0] != "Calling fixture__echo (attempt 1)." {
		t.Fatalf("progress = %q", progress)
	}
}

func TestStreamableHTTPRetriesReadOnlyTransportFailure(t *testing.T) {
	server := newFixtureServer(1, echoFixtureTool)
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	var rejectedCalls atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read MCP request: %v", err)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"tools/call"`)) && rejectedCalls.CompareAndSwap(0, 1) {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		mcpHandler.ServeHTTP(response, request)
	}))
	t.Cleanup(httpServer.Close)
	readOnly := true
	maximumAttempts := 2
	registry := newHTTPFixtureRegistry(t, httpServer.URL, map[string]ToolPolicy{
		"echo": {
			ReadOnly:          &readOnly,
			TimeoutSeconds:    1,
			MaximumAttempts:   &maximumAttempts,
			RetryTotalSeconds: 3,
		},
	})
	if err := registry.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registry.Close)

	progressCalls := 0
	invocation := fixtureInvocation("call-retry")
	invocation.WriteProgress = func(string) error {
		progressCalls++
		return nil
	}
	result, err := registry.Execute(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if rejectedCalls.Load() != 1 || progressCalls != 2 || result.IsError || result.Outcome != agent.ToolOutcomeSucceeded {
		t.Fatalf("rejected calls = %d, progress calls = %d, result = %+v", rejectedCalls.Load(), progressCalls, result)
	}
}

func TestStreamableHTTPBoundsToolTimeout(t *testing.T) {
	handler := func(
		ctx context.Context,
		_ *mcpsdk.CallToolRequest,
		_ fixtureToolInput,
	) (*mcpsdk.CallToolResult, fixtureToolOutput, error) {
		<-ctx.Done()
		return nil, fixtureToolOutput{}, ctx.Err()
	}
	server := newFixtureServer(1, handler)
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return server }, nil,
	))
	t.Cleanup(httpServer.Close)
	readOnly := true
	maximumAttempts := 1
	registry := newHTTPFixtureRegistry(t, httpServer.URL, map[string]ToolPolicy{
		"echo": {
			ReadOnly:          &readOnly,
			TimeoutSeconds:    0.05,
			MaximumAttempts:   &maximumAttempts,
			RetryTotalSeconds: 0.1,
		},
	})
	if err := registry.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registry.Close)

	started := time.Now()
	result, err := registry.Execute(t.Context(), fixtureInvocation("call-timeout"))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded tool call took %s", elapsed)
	}
	if !result.IsError || result.Outcome != agent.ToolOutcomeKnownFailure {
		t.Fatalf("timeout result = %+v", result)
	}
}

func TestStdioDiscoversExecutesAndReapsEachProcess(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(stdioHelperDirectorySource, directory)
	registry, err := NewRegistry([]ServerConfig{{
		Name:      "fixture",
		Transport: TransportStdio,
		Command:   os.Args[0],
		Environment: map[string]string{
			stdioHelperDirectory: stdioHelperDirectorySource,
		},
	}}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if startErr := registry.Start(t.Context()); startErr != nil {
		t.Fatal(startErr)
	}
	result, err := registry.Execute(t.Context(), fixtureInvocation("call-stdio"))
	if err != nil {
		t.Fatal(err)
	}
	registry.Close()
	if result.IsError || result.Outcome != agent.ToolOutcomeSucceeded {
		t.Fatalf("stdio result = %+v", result)
	}
	waitForHelperProcesses(t, directory, 2)
}

func newHTTPFixtureRegistry(t *testing.T, endpoint string, policies map[string]ToolPolicy) *Registry {
	t.Helper()
	registry, err := NewRegistry([]ServerConfig{{
		Name:      "fixture",
		Transport: TransportStreamableHTTP,
		URL:       endpoint,
		Headers:   map[string]string{"Authorization": "SUPERAGENT_TEST_MCP_AUTHORIZATION"},
		Tools:     policies,
	}}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := os.LookupEnv("SUPERAGENT_TEST_MCP_AUTHORIZATION"); !found {
		t.Setenv("SUPERAGENT_TEST_MCP_AUTHORIZATION", "fixture-token")
	}
	return registry
}

func newFixtureServer(pageSize int, handler fixtureToolHandler) *mcpsdk.Server {
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "superagent-test", Version: "1.0.0"},
		&mcpsdk.ServerOptions{PageSize: pageSize},
	)
	mcpsdk.AddTool[fixtureToolInput, fixtureToolOutput](
		server,
		&mcpsdk.Tool{Name: "echo", Description: "Echo fixture input."},
		handler,
	)
	return server
}

func echoFixtureTool(
	_ context.Context,
	_ *mcpsdk.CallToolRequest,
	input fixtureToolInput,
) (*mcpsdk.CallToolResult, fixtureToolOutput, error) {
	return nil, fixtureToolOutput{Echo: input.Value}, nil
}

func fixtureInvocation(callID agent.CallID) agent.ToolInvocation {
	return agent.ToolInvocation{
		Name:          "fixture__echo",
		Arguments:     agent.MustJSONObject(`{"value":"fixture"}`),
		CallID:        callID,
		WriteProgress: func(string) error { return nil },
	}
}

func runStdioHelper(directory string) int {
	pid := os.Getpid()
	started := filepath.Join(directory, fmt.Sprintf("started-%d", pid))
	done := filepath.Join(directory, fmt.Sprintf("done-%d", pid))
	if err := os.WriteFile(started, nil, 0o600); err != nil {
		return 2
	}
	defer func() { _ = os.WriteFile(done, nil, 0o600) }()
	server := newFixtureServer(1, echoFixtureTool)
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		return 3
	}
	return 0
}

func waitForHelperProcesses(t *testing.T, directory string, minimum int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		started, startedErr := filepath.Glob(filepath.Join(directory, "started-*"))
		done, doneErr := filepath.Glob(filepath.Join(directory, "done-*"))
		if startedErr != nil || doneErr != nil {
			t.Fatalf("read helper process markers: %v", errors.Join(startedErr, doneErr))
		}
		if len(started) >= minimum && len(done) == len(started) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stdio MCP subprocesses were not reaped")
}
